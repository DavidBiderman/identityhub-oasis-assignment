package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/temporal"

	"github.com/dbiderman/identityhub/backend/internal/capabilities/issuetracker"
	"github.com/dbiderman/identityhub/backend/internal/connections"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

// Activities holds everything the durable steps need. Activities are where all
// I/O lives: workflow code only decides, so that replay stays deterministic.
type Activities struct {
	Pool        *pgxpool.Pool
	Connections *connections.Service
	Summarizer  Summarizer
	Blog        BlogReader
	Log         *slog.Logger
}

// ConfigureInput names the connection a workflow is about to use.
type ConfigureInput struct {
	OrgID         uuid.UUID      `json:"orgId"`
	AccountID     uuid.UUID      `json:"accountId"`
	ConnectorType connector.Type `json:"connectorType"`
}

func (in ConfigureInput) scope() store.Scope {
	return store.Scope{OrgID: in.OrgID, AccountID: in.AccountID}
}

// ConfiguredConnection is what Configure reports back.
//
// It carries no credential and no configuration. The decrypted configuration
// exists only inside the activity that produced it and is discarded when that
// activity returns -- which is the whole reason configuring is an activity and
// not a workflow step. What comes back is the identifier of the connection that
// was configured, so the ticket recorded later can name the integration that
// filed it.
type ConfiguredConnection struct {
	ConnectionID  uuid.UUID      `json:"connectionId"`
	ConnectorType connector.Type `json:"connectorType"`
}

// Configure decodes an account's stored connection and confirms it is usable.
//
// It is a step of its own because it is a step of its own: reading the row,
// decrypting it, decoding it into the connector's own type and checking the
// connector can file findings are what has to be true before any of the rest is
// worth attempting. Failing here costs one database read and one decrypt;
// failing later costs a provider round trip, or in the digest a model call per
// post.
func (a *Activities) Configure(ctx context.Context, in ConfigureInput) (ConfiguredConnection, error) {
	conn, err := a.Connections.Configure(ctx, in.scope(), in.ConnectorType)
	if err != nil {
		return ConfiguredConnection{}, classifyForRetry(err)
	}
	return ConfiguredConnection{
		ConnectionID:  conn.ID,
		ConnectorType: conn.Connector.Type(),
	}, nil
}

// ResolveTarget reports where a digest should file when nothing named a place.
//
// DIGEST_PROJECT_KEY is an override, not a requirement. Most accounts have one
// project the connected credential can file into, and making an operator look up
// its key before the feature works at all is friction for no benefit -- the
// brief asks for the stack to be runnable in the easiest way possible, and a
// bonus feature that silently does nothing until a variable is set is not that.
//
// It resolves at run time rather than when the schedule is created, so an
// account that gains its first project tomorrow does not need the schedule
// rebuilding.
func (a *Activities) ResolveTarget(ctx context.Context, in ConfigureInput) (string, error) {
	conn, err := a.Connections.Configure(ctx, in.scope(), in.ConnectorType)
	if err != nil {
		return "", classifyForRetry(err)
	}

	targets, err := conn.ListTargets(ctx, "", 1)
	if err != nil {
		return "", classifyForRetry(err)
	}
	if len(targets) == 0 {
		return "", temporal.NewNonRetryableApplicationError(
			"The connected account can file into no project, so the digest has nowhere to put a ticket.",
			"not_found", nil)
	}
	return targets[0].Key, nil
}

// ClaimResult reports whether the caller owns an idempotency key.
type ClaimResult struct {
	Claimed  bool                   `json:"claimed"`
	TicketID uuid.UUID              `json:"ticketId"`
	Ticket   issuetracker.TicketRef `json:"ticket"`
}

// RecordTicketInput carries the upstream result to the recording step.
type RecordTicketInput struct {
	Request      FileTicketInput        `json:"request"`
	Ticket       issuetracker.TicketRef `json:"ticket"`
	ConnectionID uuid.UUID              `json:"connectionId"`
}

// RecordResult describes the locally recorded ticket.
//
// It carries the recorded timestamp and source rather than only an identifier,
// because those are properties of our record -- the provider does not know how
// a ticket was filed, and its own creation time is not what the recent tickets
// list orders by.
type RecordResult struct {
	TicketID  uuid.UUID `json:"ticketId"`
	CreatedAt time.Time `json:"createdAt"`
	Source    string    `json:"source"`
}

func (in FileTicketInput) scope() store.Scope {
	return store.Scope{OrgID: in.OrgID, AccountID: in.AccountID}
}

// requestHash fingerprints the finding so that reusing an idempotency key with
// a different body is detected rather than silently returning the first result.
func (in FileTicketInput) requestHash() []byte {
	encoded, _ := json.Marshal(in.Finding)
	sum := sha256.Sum256(encoded)
	return sum[:]
}

// ClaimIdempotency reserves the caller's idempotency key.
func (a *Activities) ClaimIdempotency(ctx context.Context, in FileTicketInput) (ClaimResult, error) {
	claimed, ticketID, err := a.claimKey(ctx, in.scope(), in.IdempotencyKey, in.requestHash())
	if errors.Is(err, errIdempotencyMismatch) {
		// Reusing a key with a different body is the caller's mistake and will
		// never succeed on retry.
		return ClaimResult{}, permanent(err, "idempotency_mismatch")
	}
	if err != nil {
		return ClaimResult{}, err
	}
	if claimed {
		return ClaimResult{Claimed: true}, nil
	}

	// The key was already claimed. If the original attempt finished, return its
	// ticket; if it is still running, this request has nothing to do.
	//
	// Non-retryable, like its sibling above. Retrying inside the activity would
	// only wait for the other request, and the retry policy outlives the
	// handler's synchronous wait -- so the caller would get 202 "queued" for a
	// workflow that is about to fail, instead of the 409 that says another
	// request holds the key. Whether to try again is the caller's decision, and
	// this is the sentence that lets them make it.
	if ticketID == nil {
		return ClaimResult{}, permanent(errIdempotencyInProgress, "idempotency_in_progress")
	}

	scope := in.scope()
	ticket, err := store.Read(a.Pool).TicketByID(ctx, sqlcgen.TicketByIDParams{
		OrgID: scope.OrgID, AccountID: scope.AccountID, ID: *ticketID,
	})
	if err != nil {
		return ClaimResult{}, fmt.Errorf("read ticket %s: %w", *ticketID, store.MapError(err))
	}
	return ClaimResult{
		TicketID: ticket.ID,
		Ticket: issuetracker.TicketRef{
			Key:         ticket.IssueKey,
			URL:         ticket.IssueUrl,
			Summary:     ticket.Summary,
			CreatedAt:   ticket.CreatedAt,
			ContainerID: ticket.ProjectKey,
		},
	}, nil
}

// ReleaseIdempotency drops a claim whose work failed, so a transient failure
// does not lock the key permanently.
func (a *Activities) ReleaseIdempotency(ctx context.Context, in FileTicketInput) error {
	return a.releaseKey(ctx, in.scope(), in.IdempotencyKey)
}

// FileTicketUpstream creates the ticket in the connected provider.
//
// This is where a credential is decrypted, and the only place. The plaintext
// lives inside this call and is returned to no one: the result carries a
// ticket reference, which is not sensitive.
func (a *Activities) FileTicketUpstream(ctx context.Context, in FileTicketInput) (issuetracker.TicketRef, error) {
	scope := in.scope()

	conn, err := a.Connections.Configure(ctx, scope, in.ConnectorType)
	if err != nil {
		return issuetracker.TicketRef{}, classifyForRetry(err)
	}

	ref, err := conn.FileTicket(ctx, issuetracker.NewFinding{
		TargetKey:   in.Finding.TargetKey,
		KindID:      in.Finding.KindID,
		Title:       in.Finding.Title,
		Description: in.Finding.Description,
		Labels:      in.Finding.Labels,
	})
	if err != nil {
		// The connection is marked here because a rejected credential is a fact
		// about this attempt. The audit event is not: this activity runs up to
		// five times and cannot tell a failed attempt from a failed action, so
		// the workflow writes it once, through RecordFilingFailure below.
		a.Connections.NoteFailure(ctx, scope, conn.ID, err)
		return issuetracker.TicketRef{}, classifyForRetry(err)
	}

	a.Connections.NoteSuccess(ctx, scope, conn.ID)
	return ref, nil
}

// FilingFailureInput carries the one terminal failure to the audit trail.
type FilingFailureInput struct {
	Request FileTicketInput `json:"request"`
	Reason  string          `json:"reason"`
}

// RecordFilingFailure appends the audit event for a finding that was not filed.
//
// It is an activity of its own, called by the workflow, because the workflow is
// the only layer that knows an attempt was the last one. Written from inside
// FileTicketUpstream it produced one row per retry -- five for a single click,
// into a table the application role may not UPDATE or DELETE.
func (a *Activities) RecordFilingFailure(ctx context.Context, in FilingFailureInput) error {
	a.audit(ctx, in.Request.scope(), in.Request, store.ActionTicketFailed, map[string]any{
		"targetKey": in.Request.Finding.TargetKey,
		"reason":    in.Reason,
	})
	return nil
}

// RecordTicket writes the ticket to our own database, which is the source of
// truth for "created from this app".
func (a *Activities) RecordTicket(ctx context.Context, in RecordTicketInput) (RecordResult, error) {
	scope := in.Request.scope()

	var ticket sqlcgen.Ticket
	err := store.InTx(ctx, a.Pool, func(q *sqlcgen.Queries) error {
		var err error
		ticket, err = q.InsertTicket(ctx, sqlcgen.InsertTicketParams{
			OrgID:         scope.OrgID,
			AccountID:     scope.AccountID,
			ConnectionID:  store.NullableUUID(in.ConnectionID),
			ProjectKey:    in.Ticket.ContainerID,
			IssueKey:      in.Ticket.Key,
			IssueID:       in.Ticket.ExternalID,
			IssueUrl:      in.Ticket.URL,
			Summary:       in.Ticket.Summary,
			Source:        in.Request.Source,
			CreatedByUser: userActor(in.Request),
			CreatedByKey:  apiKeyActor(in.Request),
			JiraCreatedAt: nullableTime(in.Ticket.CreatedAt),
		})
		return err
	})
	err = store.MapError(err)

	// A conflict means this issue key is already recorded -- normally because a
	// previous attempt of this activity succeeded and then failed before
	// reporting success. That is a converged outcome, not an error.
	//
	// The activity must still finish its remaining work. An earlier version
	// returned here, which left the idempotency key claimed but never
	// completed: every later replay then saw a request "in progress" that would
	// never finish, and the caller could not retry with that key again.
	duplicate := errors.Is(err, store.ErrConflict)
	if duplicate {
		existing, lookupErr := store.Read(a.Pool).TicketByIssueKey(ctx, sqlcgen.TicketByIssueKeyParams{
			OrgID: scope.OrgID, AccountID: scope.AccountID, IssueKey: in.Ticket.Key,
		})
		if lookupErr != nil {
			return RecordResult{}, fmt.Errorf("read ticket %s: %w", in.Ticket.Key, store.MapError(lookupErr))
		}
		ticket = existing
	} else if err != nil {
		return RecordResult{}, fmt.Errorf("record ticket %s: %w", in.Ticket.Key, err)
	}

	if in.Request.IdempotencyKey != "" {
		if err := a.completeKey(ctx, scope, in.Request.IdempotencyKey, ticket.ID); err != nil {
			return RecordResult{}, err
		}
	}

	// Only a genuinely new ticket is announced, so a converging retry does not
	// write a second audit event for one action.
	if !duplicate {
		a.audit(ctx, scope, in.Request, store.ActionTicketCreated, map[string]any{
			"issueKey":  ticket.IssueKey,
			"targetKey": ticket.ProjectKey,
			"source":    ticket.Source,
		})
	}
	return RecordResult{
		TicketID:  ticket.ID,
		CreatedAt: ticket.CreatedAt,
		Source:    ticket.Source,
	}, nil
}

// audit records what a workflow did. A failure here is logged, never returned:
// losing an audit line is bad, and failing a filed ticket over it is worse.
func (a *Activities) audit(ctx context.Context, sc store.Scope, in FileTicketInput, action string, metadata map[string]any) {
	encoded, err := json.Marshal(metadata)
	if err == nil {
		err = store.InTx(ctx, a.Pool, func(q *sqlcgen.Queries) error {
			return q.AppendAuditEvent(ctx, sqlcgen.AppendAuditEventParams{
				OrgID:     sc.OrgID,
				AccountID: sc.AccountID,
				ActorType: string(in.ActorType),
				ActorID:   store.NullableUUID(in.ActorID),
				Action:    action,
				Target:    in.Finding.TargetKey,
				Metadata:  encoded,
			})
		})
	}
	if err != nil && a.Log != nil {
		a.Log.Warn("could not write audit event", "action", action, "error", err)
	}
}

// nullableUUID maps the zero UUID to SQL NULL, because these columns are
// foreign keys and no row has that identifier.
//
// The two actor columns below are exclusive: a ticket was filed by a person or
// by a key, and the other column is null.
func userActor(in FileTicketInput) *uuid.UUID {
	if in.ActorType == store.ActorUser {
		return store.NullableUUID(in.ActorID)
	}
	return nil
}

func apiKeyActor(in FileTicketInput) *uuid.UUID {
	if in.ActorType == store.ActorAPIKey {
		return store.NullableUUID(in.ActorID)
	}
	return nil
}

// nullableTime maps the zero time to SQL NULL: a provider that did not report
// a creation time is absent, not the year zero.
func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// classifyForRetry converts a connector failure into a Temporal error that
// carries the retry decision, and the message with it.
//
// This is why the connector error taxonomy exists: without it every upstream
// failure would look alike, and a rejected credential would be retried five
// times before surfacing an error that was never going to change.
func classifyForRetry(err error) error {
	if err == nil {
		return nil
	}

	// A missing connection is its own class, and it is not a connector error:
	// there is no connector to have produced one.
	if errors.Is(err, connections.ErrNotConnected) {
		return temporal.NewNonRetryableApplicationError(err.Error(), "not_connected", err)
	}

	var ce *connector.Error
	if errors.As(err, &ce) {
		if ce.Retryable() {
			return err
		}
		return temporal.NewNonRetryableApplicationError(ce.Error(), errorType(ce), err)
	}
	return err
}

// errorType names the failure class, for the Temporal UI and for the API layer.
//
// Every kind that can reach this function has a case. Two do not appear --
// rate limited and unavailable -- because both are Retryable, so
// classifyForRetry returns before calling this. The default is therefore
// reached only by a sentinel added to the taxonomy without a case here, or by a
// nil Kind, and "provider_error" is the safe answer to both.
//
// It used to be reached by two kinds that do arrive here -- a configuration
// this application cannot decrypt, and an action a connector does not implement
// -- which came out as a generic provider error the API answered with 502 and
// "It will be retried automatically". The workflow had just marked them
// non-retryable, so that sentence was false.
func errorType(ce *connector.Error) string {
	switch {
	case errors.Is(ce.Kind, connector.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(ce.Kind, connector.ErrForbidden):
		return "forbidden"
	case errors.Is(ce.Kind, connector.ErrNotFound):
		return "not_found"
	case errors.Is(ce.Kind, connector.ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(ce.Kind, connector.ErrConfigInvalid):
		return "configuration"
	case errors.Is(ce.Kind, connector.ErrUnsupportedAction):
		return "unsupported"
	default:
		return "provider_error"
	}
}

// isConflict reports a uniqueness violation, which for a replayed activity
// means a previous attempt already did the work.
func isConflict(err error) bool {
	return errors.Is(err, store.ErrConflict)
}

// permanent marks an error as one that retrying cannot fix.
//
// The message carried across is the error's own. An activity does not rewrite
// it, for the same reason the HTTP layer does not: whatever failed is what knew
// what to say, and Temporal is only the wire it travels over.
func permanent(err error, kind string) error {
	return temporal.NewNonRetryableApplicationError(err.Error(), kind, err)
}
