// Package workflows contains the durable parts of IdentityHub.
//
// Two operations run here, and only two: filing a ticket, which is a multi-step
// saga across a system we do not control, and the blog digest, which is a
// recurring job. Reads are not workflows -- a query against our own database
// has nothing to recover from and nothing worth replaying.
//
// # Credentials never enter workflow history
//
// Temporal persists workflow arguments and activity results in its own
// datastore. A decrypted credential passed as a workflow argument, or returned
// from an activity, would be written to a second system with different backup,
// retention and access properties than our own, and would sit there for the
// history retention period.
//
// So workflows carry a tenancy scope and a connection type, never a credential.
// Decryption happens inside the activity that needs it and the plaintext
// crosses no activity boundary in either direction.
package workflows

import (
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dbiderman/identityhub/backend/internal/capabilities/issuetracker"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/store"
)

// TaskQueue is the queue both the API and the worker use.
const TaskQueue = "identityhub"

// FileTicketInput starts a ticket-filing workflow.
//
// Note what is absent: any credential, and any connector-specific payload. The
// activity resolves the connection from the scope and connector type.
type FileTicketInput struct {
	OrgID          uuid.UUID       `json:"orgId"`
	AccountID      uuid.UUID       `json:"accountId"`
	ConnectorType  connector.Type  `json:"connectorType"`
	Finding        NewFinding      `json:"finding"`
	Source         string          `json:"source"`
	ActorType      store.ActorType `json:"actorType"`
	ActorID        uuid.UUID       `json:"actorId"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
}

// NewFinding is the finding to file, in the product's vocabulary.
type NewFinding struct {
	TargetKey   string   `json:"targetKey"`
	KindID      string   `json:"kindId,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels,omitempty"`
}

// FileTicketResult is what the caller gets back.
type FileTicketResult struct {
	TicketID  uuid.UUID              `json:"ticketId"`
	Ticket    issuetracker.TicketRef `json:"ticket"`
	CreatedAt time.Time              `json:"createdAt"`
	Source    string                 `json:"source"`
	Replayed  bool                   `json:"replayed"`
}

// bestEffort bounds a step whose failure must not fail the workflow.
//
// "Best effort" has to mean bounded effort. Temporal's default is unlimited
// attempts, so a step that is only allowed to fail silently was in fact allowed
// to retry forever -- and the two steps that use this run on the failure path,
// where the resources they need are the ones most likely to be exhausted.
//
// The specific hazard: auditFilingFailure runs before releaseClaim, so an audit
// activity that kept timing out would retry indefinitely and never reach the
// release. The caller's Idempotency-Key would stay claimed permanently, and
// every replay of it would get 409 for good -- exactly the lock-out that
// releaseClaim exists to prevent. A ceiling on attempts and a ceiling on total
// time close that.
var bestEffort = workflow.ActivityOptions{
	StartToCloseTimeout:    10 * time.Second,
	ScheduleToCloseTimeout: 30 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2.0,
		MaximumAttempts:    3,
	},
}

// retryOnTransientOnly retries only failures that could plausibly succeed later.
//
// A rejected credential, a missing project or a malformed request will fail the
// same way every time; retrying them wastes a retry budget and delays the error
// the user needs to see. The connector error taxonomy is what makes this
// distinction available, and activities mark permanent failures accordingly.
var retryOnTransientOnly = &temporal.RetryPolicy{
	InitialInterval:    time.Second,
	BackoffCoefficient: 2.0,
	MaximumInterval:    time.Minute,
	MaximumAttempts:    5,
}

// FileTicket files a finding and records it.
//
// The saga is worth durability because steps 2 and 3 straddle a network
// boundary: a crash between them would leave a ticket in the tracker that the
// recent tickets view would never show.
func FileTicket(ctx workflow.Context, in FileTicketInput) (FileTicketResult, error) {
	// The tenancy is checked here, once, because this is where it re-enters the
	// application: the API validated it when it built a Principal, but the
	// input arrived over a wire as JSON and everything below now trusts it.
	// A workflow that cannot say which account it is for will never succeed, so
	// it fails immediately rather than after five retries of the first activity.
	if err := in.scope().Validate(); err != nil {
		return FileTicketResult{}, temporal.NewNonRetryableApplicationError(
			"This workflow was started without a complete tenancy.", "invalid_input", err)
	}

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         retryOnTransientOnly,
	})

	// A nil *Activities is deliberate and safe. Temporal resolves an activity
	// by name; a.Configure below is a method value used only to supply that
	// name at compile time, and the worker invokes it on the instance it was
	// registered with. This nil is never dereferenced.
	var a *Activities

	// 1. Claim the idempotency key, if the caller supplied one. A replay of a
	//    completed request returns the original ticket rather than filing a
	//    second one.
	if in.IdempotencyKey != "" {
		var claim ClaimResult
		if err := workflow.ExecuteActivity(ctx, a.ClaimIdempotency, in).Get(ctx, &claim); err != nil {
			return FileTicketResult{}, err
		}
		if !claim.Claimed {
			return FileTicketResult{
				TicketID:  claim.TicketID,
				Ticket:    claim.Ticket,
				CreatedAt: claim.Ticket.CreatedAt,
				Source:    in.Source,
				Replayed:  true,
			}, nil
		}
	}

	// 2. Configure the connection: decode the stored credential into something
	//    the connector can act on, and confirm it can file findings. A broken
	//    connection fails here, before a provider is contacted.
	var conn ConfiguredConnection
	if err := workflow.ExecuteActivity(ctx, a.Configure, ConfigureInput{
		OrgID: in.OrgID, AccountID: in.AccountID, ConnectorType: in.ConnectorType,
	}).Get(ctx, &conn); err != nil {
		releaseClaim(ctx, a, in)
		return FileTicketResult{}, err
	}

	// 3. File the ticket upstream.
	var ref issuetracker.TicketRef
	if err := workflow.ExecuteActivity(ctx, a.FileTicketUpstream, in).Get(ctx, &ref); err != nil {
		auditFilingFailure(ctx, a, in, err)
		releaseClaim(ctx, a, in)
		return FileTicketResult{}, err
	}

	// 4. Record it locally. This is the step that makes the ticket visible in
	//    the product, and the reason the saga is durable at all.
	var recorded RecordResult
	if err := workflow.ExecuteActivity(ctx, a.RecordTicket, RecordTicketInput{
		Request:      in,
		Ticket:       ref,
		ConnectionID: conn.ConnectionID,
	}).Get(ctx, &recorded); err != nil {
		return FileTicketResult{}, err
	}

	return FileTicketResult{
		TicketID:  recorded.TicketID,
		Ticket:    ref,
		CreatedAt: recorded.CreatedAt,
		Source:    recorded.Source,
	}, nil
}

// auditFilingFailure records that a finding was not filed, exactly once.
//
// The activity that fails cannot do this: it runs up to five times and every
// attempt looks the same from inside. Only the workflow sees the outcome, and
// it sees it once -- whether the error was non-retryable on the first attempt
// or transient until the last. Best effort, like releaseClaim: a missing audit
// row must not turn a failed filing into a failed workflow.
func auditFilingFailure(ctx workflow.Context, a *Activities, in FileTicketInput, cause error) {
	audit := workflow.WithActivityOptions(ctx, bestEffort)
	_ = workflow.ExecuteActivity(audit, a.RecordFilingFailure, FilingFailureInput{
		Request: in, Reason: cause.Error(),
	}).Get(audit, nil)
}

// releaseClaim frees an idempotency key whose work did not finish, so a
// transient failure does not lock the caller out of it forever.
//
// It runs on its own timeout because the context that failed may already be
// cancelled, and a best-effort cleanup on a dead context does nothing.
func releaseClaim(ctx workflow.Context, a *Activities, in FileTicketInput) {
	if in.IdempotencyKey == "" {
		return
	}
	release := workflow.WithActivityOptions(ctx, bestEffort)
	_ = workflow.ExecuteActivity(release, a.ReleaseIdempotency, in).Get(release, nil)
}
