package workflows_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/dbiderman/identityhub/backend/internal/blog"
	"github.com/dbiderman/identityhub/backend/internal/capabilities/issuetracker"
	"github.com/dbiderman/identityhub/backend/internal/workflows"
)

// These run on Temporal's test environment: real workflow code, mocked
// activities. They assert the orchestration decisions -- what runs, in what
// order, and what happens when a step fails -- which is the part worth having
// a workflow engine for at all.

func input() workflows.FileTicketInput {
	return workflows.FileTicketInput{
		OrgID:         uuid.New(),
		AccountID:     uuid.New(),
		ConnectorType: "jira",
		Source:        "ui",
		ActorType:     "user",
		ActorID:       uuid.New(),
		Finding: workflows.NewFinding{
			TargetKey:   "NHI",
			Title:       "Stale Service Account: svc-deploy-prod",
			Description: "Last used 400 days ago.",
		},
	}
}

func TestFileTicketFilesThenRecords(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.FileTicketUpstream)
	env.RegisterActivity(a.RecordTicket)

	ticketID := uuid.New()
	ref := issuetracker.TicketRef{Key: "NHI-1", URL: "https://acme.atlassian.net/browse/NHI-1"}

	env.OnActivity(a.Configure, mock.Anything, mock.Anything).
		Return(workflows.ConfiguredConnection{ConnectionID: uuid.New(), ConnectorType: "jira"}, nil).Once()
	env.OnActivity(a.FileTicketUpstream, mock.Anything, mock.Anything).Return(ref, nil).Once()
	env.OnActivity(a.RecordTicket, mock.Anything, mock.Anything).
		Return(workflows.RecordResult{TicketID: ticketID}, nil).Once()

	env.ExecuteWorkflow(workflows.FileTicket, input())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result workflows.FileTicketResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, ticketID, result.TicketID)
	require.Equal(t, "NHI-1", result.Ticket.Key)
	require.False(t, result.Replayed)
	env.AssertExpectations(t)
}

// A replay of a completed request must return the original ticket rather than
// filing a second one. This is the guarantee that lets a CI job retry safely.
func TestFileTicketReplayReturnsTheOriginalTicket(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.ClaimIdempotency)
	env.RegisterActivity(a.FileTicketUpstream)
	env.RegisterActivity(a.RecordTicket)

	original := uuid.New()
	env.OnActivity(a.ClaimIdempotency, mock.Anything, mock.Anything).Return(
		workflows.ClaimResult{
			Claimed:  false,
			TicketID: original,
			Ticket:   issuetracker.TicketRef{Key: "NHI-1"},
		}, nil).Once()

	in := input()
	in.IdempotencyKey = "ci-run-42"
	env.ExecuteWorkflow(workflows.FileTicket, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result workflows.FileTicketResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.True(t, result.Replayed, "a replay must be reported as such")
	require.Equal(t, original, result.TicketID)

	// Nothing was filed upstream.
	env.AssertNotCalled(t, "FileTicketUpstream", mock.Anything, mock.Anything)
	env.AssertNotCalled(t, "RecordTicket", mock.Anything, mock.Anything)
}

// A failure after the claim must release it, so a transient error does not lock
// the caller out of retrying with the same key.
func TestFileTicketReleasesItsClaimOnFailure(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.ClaimIdempotency)
	env.RegisterActivity(a.FileTicketUpstream)
	env.RegisterActivity(a.ReleaseIdempotency)
	env.RegisterActivity(a.RecordFilingFailure)

	env.OnActivity(a.ClaimIdempotency, mock.Anything, mock.Anything).
		Return(workflows.ClaimResult{Claimed: true}, nil).Once()
	env.OnActivity(a.Configure, mock.Anything, mock.Anything).
		Return(workflows.ConfiguredConnection{ConnectionID: uuid.New(), ConnectorType: "jira"}, nil).Once()
	env.OnActivity(a.FileTicketUpstream, mock.Anything, mock.Anything).
		Return(issuetracker.TicketRef{}, temporal.NewNonRetryableApplicationError(
			"Jira rejected the stored credential.", "unauthorized", nil)).Once()
	env.OnActivity(a.ReleaseIdempotency, mock.Anything, mock.Anything).Return(nil).Once()
	// Once, not once per attempt. The audit table is append-only -- the
	// application role has no UPDATE or DELETE on it -- so a row written per
	// retry can never be tidied away.
	env.OnActivity(a.RecordFilingFailure, mock.Anything, mock.Anything).Return(nil).Once()

	in := input()
	in.IdempotencyKey = "ci-run-43"
	env.ExecuteWorkflow(workflows.FileTicket, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// A rejected credential must fail immediately. Retrying it would delay the
// error the user needs to see without any chance of succeeding.
func TestFileTicketDoesNotRetryAPermanentFailure(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.FileTicketUpstream)
	env.RegisterActivity(a.RecordFilingFailure)

	attempts := 0
	env.OnActivity(a.Configure, mock.Anything, mock.Anything).
		Return(workflows.ConfiguredConnection{ConnectionID: uuid.New(), ConnectorType: "jira"}, nil).Once()
	env.OnActivity(a.FileTicketUpstream, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { attempts++ }).
		Return(issuetracker.TicketRef{}, temporal.NewNonRetryableApplicationError(
			"Jira rejected the stored credential.", "unauthorized", nil))
	// Mocked, not merely registered. Without this the workflow runs the real
	// activity against a nil *Activities, and what that does -- panic, error,
	// or retry until something gives up -- is not a property this test should
	// depend on. The sibling tests mock it; this one now does too.
	env.OnActivity(a.RecordFilingFailure, mock.Anything, mock.Anything).Return(nil).Once()

	env.ExecuteWorkflow(workflows.FileTicket, input())

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.Equal(t, 1, attempts, "a permanent failure must not be retried")
}

// A transient failure must be retried, and a later attempt that succeeds must
// carry the workflow through. This is the whole reason for durable execution.
func TestFileTicketRetriesATransientFailure(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.FileTicketUpstream)
	env.RegisterActivity(a.RecordTicket)

	ref := issuetracker.TicketRef{Key: "NHI-2"}
	attempts := 0
	env.OnActivity(a.Configure, mock.Anything, mock.Anything).
		Return(workflows.ConfiguredConnection{ConnectionID: uuid.New(), ConnectorType: "jira"}, nil).Once()
	env.OnActivity(a.FileTicketUpstream, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { attempts++ }).
		Return(func(context.Context, workflows.FileTicketInput) (issuetracker.TicketRef, error) {
			if attempts < 3 {
				return issuetracker.TicketRef{}, errors.New("jira is unavailable")
			}
			return ref, nil
		})
	env.OnActivity(a.RecordTicket, mock.Anything, mock.Anything).
		Return(workflows.RecordResult{TicketID: uuid.New()}, nil).Once()

	env.ExecuteWorkflow(workflows.FileTicket, input())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 3, attempts, "a transient failure should have been retried")
}

// A finding that could not be filed is audited once, not once per attempt.
//
// audit_events has UPDATE and DELETE revoked from the application role, so a
// row written per retry is permanent noise: one outage would put five
// ticket.failed entries in front of whoever reads the trail. The activity
// cannot tell its last attempt from its first, so the workflow writes this.
func TestATransientOutageIsAuditedOnceNotOncePerAttempt(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.FileTicketUpstream)
	env.RegisterActivity(a.RecordFilingFailure)

	attempts := 0
	env.OnActivity(a.Configure, mock.Anything, mock.Anything).
		Return(workflows.ConfiguredConnection{ConnectionID: uuid.New(), ConnectorType: "jira"}, nil).Once()
	env.OnActivity(a.FileTicketUpstream, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { attempts++ }).
		Return(issuetracker.TicketRef{}, errors.New("jira is unavailable"))

	audited := 0
	env.OnActivity(a.RecordFilingFailure, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { audited++ }).
		Return(nil)

	env.ExecuteWorkflow(workflows.FileTicket, input())

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.Greater(t, attempts, 1, "a transient failure should have been retried")
	require.Equal(t, 1, audited, "one failed filing is one audit event")
}

// A failing audit must not strand the caller's idempotency key.
//
// auditFilingFailure runs before releaseClaim, and both are best effort. With
// Temporal's default retry policy -- unlimited attempts -- an audit activity
// that kept failing would retry forever and the release would never run. The
// key would stay claimed permanently, and every replay of it would get 409 for
// good: precisely the lock-out releaseClaim exists to prevent. Bounding the
// attempts is what makes "best effort" mean bounded effort.
func TestAFailingAuditStillReleasesTheIdempotencyKey(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.ClaimIdempotency)
	env.RegisterActivity(a.FileTicketUpstream)
	env.RegisterActivity(a.RecordFilingFailure)
	env.RegisterActivity(a.ReleaseIdempotency)

	env.OnActivity(a.ClaimIdempotency, mock.Anything, mock.Anything).
		Return(workflows.ClaimResult{Claimed: true}, nil).Once()
	env.OnActivity(a.Configure, mock.Anything, mock.Anything).
		Return(workflows.ConfiguredConnection{ConnectionID: uuid.New(), ConnectorType: "jira"}, nil).Once()
	env.OnActivity(a.FileTicketUpstream, mock.Anything, mock.Anything).
		Return(issuetracker.TicketRef{}, temporal.NewNonRetryableApplicationError(
			"Jira rejected the stored credential.", "unauthorized", nil)).Once()

	// The audit never succeeds, however many times it is asked.
	audits := 0
	env.OnActivity(a.RecordFilingFailure, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { audits++ }).
		Return(errors.New("the audit table is unreachable"))

	released := false
	env.OnActivity(a.ReleaseIdempotency, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { released = true }).
		Return(nil).Once()

	in := input()
	in.IdempotencyKey = "ci-run-audit-fails"
	env.ExecuteWorkflow(workflows.FileTicket, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.True(t, released,
		"the idempotency key was never released, so the caller is locked out of it forever")
	require.LessOrEqual(t, audits, 3, "the audit retried past its ceiling")
}

// One post that cannot be filed must not stop the rest of the digest.
func TestBlogDigestContinuesPastAFailedPost(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.ResolveTarget)
	env.RegisterActivity(a.FetchNewPosts)
	env.RegisterActivity(a.FilePost)

	posts := []blog.Post{
		{URL: "https://example.test/a", Title: "A"},
		{URL: "https://example.test/b", Title: "B"},
		{URL: "https://example.test/c", Title: "C"},
	}
	env.OnActivity(a.Configure, mock.Anything, mock.Anything).
		Return(workflows.ConfiguredConnection{ConnectionID: uuid.New(), ConnectorType: "jira"}, nil).Once()
	env.OnActivity(a.FetchNewPosts, mock.Anything, mock.Anything).Return(posts, nil).Once()

	env.OnActivity(a.FilePost, mock.Anything, mock.Anything).
		Return(workflows.FiledPost{IssueKey: "NHI-10"}, nil).Once()
	env.OnActivity(a.FilePost, mock.Anything, mock.Anything).
		Return(workflows.FiledPost{}, temporal.NewNonRetryableApplicationError(
			"summariser refused", "refused", nil)).Once()
	env.OnActivity(a.FilePost, mock.Anything, mock.Anything).
		Return(workflows.FiledPost{IssueKey: "NHI-12"}, nil).Once()

	env.ExecuteWorkflow(workflows.BlogDigest, workflows.DigestInput{
		OrgID: uuid.New(), AccountID: uuid.New(),
		ConnectorType: "jira", TargetKey: "NHI", MaxPosts: 5,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result workflows.DigestResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, 3, result.Considered)
	require.Equal(t, []string{"NHI-10", "NHI-12"}, result.Filed)
	require.Equal(t, 1, result.Skipped)
}

// A post that cannot be filed is one audit event, not one per attempt.
//
// The failure is the only thing the audit trail can say about a digest that
// filed nothing: without it a run that failed every night showed no rows at
// all, which reads as a schedule that never fired. FilePost retries, so the
// event is written by the workflow, which sees the outcome once.
func TestADigestPostThatCannotBeFiledIsAuditedOnceNotOncePerAttempt(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.ResolveTarget)
	env.RegisterActivity(a.FetchNewPosts)
	env.RegisterActivity(a.FilePost)
	env.RegisterActivity(a.RecordDigestFailure)

	doomed := blog.Post{URL: "https://example.test/a", Title: "A"}
	next := blog.Post{URL: "https://example.test/b", Title: "B"}

	env.OnActivity(a.Configure, mock.Anything, mock.Anything).
		Return(workflows.ConfiguredConnection{ConnectionID: uuid.New(), ConnectorType: "jira"}, nil).Once()
	env.OnActivity(a.FetchNewPosts, mock.Anything, mock.Anything).
		Return([]blog.Post{doomed, next}, nil).Once()

	attempts := 0
	env.OnActivity(a.FilePost, mock.Anything, mock.MatchedBy(func(in workflows.FilePostInput) bool {
		return in.Post.URL == doomed.URL
	})).Run(func(mock.Arguments) { attempts++ }).
		Return(workflows.FiledPost{}, errors.New("jira is unavailable"))
	env.OnActivity(a.FilePost, mock.Anything, mock.MatchedBy(func(in workflows.FilePostInput) bool {
		return in.Post.URL == next.URL
	})).Return(workflows.FiledPost{IssueKey: "NHI-2"}, nil).Once()

	audited := 0
	var recorded workflows.DigestFailureInput
	env.OnActivity(a.RecordDigestFailure, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			audited++
			recorded = args.Get(1).(workflows.DigestFailureInput)
		}).
		Return(nil)

	env.ExecuteWorkflow(workflows.BlogDigest, workflows.DigestInput{
		OrgID: uuid.New(), AccountID: uuid.New(),
		ConnectorType: "jira", TargetKey: "NHI", MaxPosts: 5,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Greater(t, attempts, 1, "a transient failure should have been retried")
	require.Equal(t, 1, audited, "one post that could not be filed is one audit event")
	require.Equal(t, doomed.URL, recorded.Post.URL, "the audit names the post the run gave up on")
	require.Contains(t, recorded.Reason, "jira is unavailable")

	// The digest carries on: the post after the failure is still filed.
	var result workflows.DigestResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, []string{"NHI-2"}, result.Filed)
	require.Equal(t, 1, result.Skipped)
}

// An empty feed is a normal outcome, not an error.
func TestBlogDigestWithNothingNew(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.ResolveTarget)
	env.RegisterActivity(a.FetchNewPosts)
	env.RegisterActivity(a.FilePost)

	env.OnActivity(a.Configure, mock.Anything, mock.Anything).
		Return(workflows.ConfiguredConnection{ConnectionID: uuid.New(), ConnectorType: "jira"}, nil).Once()
	env.OnActivity(a.FetchNewPosts, mock.Anything, mock.Anything).
		Return([]blog.Post{}, nil).Once()

	env.ExecuteWorkflow(workflows.BlogDigest, workflows.DigestInput{
		OrgID: uuid.New(), AccountID: uuid.New(), ConnectorType: "jira", TargetKey: "NHI",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertNotCalled(t, "FilePost", mock.Anything, mock.Anything)
}

// A credential must never appear in a workflow argument or result, because
// Temporal persists both in its own datastore, where they would outlive the
// request and sit outside the key service entirely.
//
// The check is reflection over an allow-list rather than a struct literal. It
// used to be a keyed literal with a comment claiming that adding a field would
// break it -- which is false about Go: a keyed literal is not exhaustive, and
// adding `JiraToken string` would have compiled without a murmur. Anyone adding
// a field now has to come here and say what it is.
func TestNothingPersistedInWorkflowHistoryCanCarryACredential(t *testing.T) {
	allowed := map[reflect.Type]map[string]string{
		reflect.TypeFor[workflows.FileTicketInput](): {
			"OrgID":          "tenancy, from the token",
			"AccountID":      "tenancy, from the token",
			"ConnectorType":  "names which credential to load, and is not one",
			"Finding":        "the ticket's own text, supplied by the caller",
			"Source":         "ui, api or automation",
			"ActorType":      "user, api_key or system",
			"ActorID":        "our own row id for the caller",
			"IdempotencyKey": "the caller's own key",
		},
		reflect.TypeFor[workflows.ConfigureInput](): {
			"OrgID":         "tenancy",
			"AccountID":     "tenancy",
			"ConnectorType": "names which credential to load",
		},
		reflect.TypeFor[workflows.ConfiguredConnection](): {
			"ConnectionID":  "our own row id for the connection",
			"ConnectorType": "which integration was configured",
		},
		reflect.TypeFor[workflows.RecordTicketInput](): {
			"Request":      "the FileTicketInput above",
			"Ticket":       "what the provider returned: a key, a URL, a summary",
			"ConnectionID": "our own row id for the connection",
		},
		reflect.TypeFor[workflows.DigestInput](): {
			"OrgID":         "tenancy",
			"AccountID":     "tenancy",
			"ConnectorType": "names which credential to load",
			"TargetKey":     "the project the digest files into",
			"MaxPosts":      "a bound on one run",
		},
	}

	for typ, fields := range allowed {
		for i := range typ.NumField() {
			name := typ.Field(i).Name
			require.Contains(t, fields, name,
				"%s.%s is new, and everything on this type is persisted in workflow "+
					"history. Confirm it carries no credential, then list it here.", typ.Name(), name)
		}
	}
}

// A digest with no configured project files into the account's first one, so
// the feature works without an operator having to look up a project key.
func TestTheDigestFilesSomewhereWhenNothingNamedAPlace(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.ResolveTarget)
	env.RegisterActivity(a.FetchNewPosts)
	env.RegisterActivity(a.FilePost)

	env.OnActivity(a.Configure, mock.Anything, mock.Anything).
		Return(workflows.ConfiguredConnection{ConnectionID: uuid.New(), ConnectorType: "jira"}, nil).Once()
	env.OnActivity(a.ResolveTarget, mock.Anything, mock.Anything).
		Return("SCRUM", nil).Once()
	env.OnActivity(a.FetchNewPosts, mock.Anything, mock.Anything).
		Return([]blog.Post{{URL: "https://oasis.security/blog/one", Title: "One"}}, nil).Once()

	// What the resolved key is for: the post is filed into it.
	env.OnActivity(a.FilePost, mock.Anything, mock.MatchedBy(func(in workflows.FilePostInput) bool {
		return in.Digest.TargetKey == "SCRUM"
	})).Return(workflows.FiledPost{IssueKey: "SCRUM-1"}, nil).Once()

	env.ExecuteWorkflow(workflows.BlogDigest, workflows.DigestInput{
		OrgID: uuid.New(), AccountID: uuid.New(), ConnectorType: "jira",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// A configured project is an override and must not be second-guessed.
func TestAConfiguredProjectIsNotResolvedAgain(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()

	var a *workflows.Activities
	env.RegisterActivity(a.Configure)
	env.RegisterActivity(a.ResolveTarget)
	env.RegisterActivity(a.FetchNewPosts)

	env.OnActivity(a.Configure, mock.Anything, mock.Anything).
		Return(workflows.ConfiguredConnection{ConnectionID: uuid.New(), ConnectorType: "jira"}, nil).Once()
	env.OnActivity(a.FetchNewPosts, mock.Anything, mock.Anything).
		Return([]blog.Post{}, nil).Once()

	env.ExecuteWorkflow(workflows.BlogDigest, workflows.DigestInput{
		OrgID: uuid.New(), AccountID: uuid.New(), ConnectorType: "jira", TargetKey: "NHI",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertNotCalled(t, "ResolveTarget", mock.Anything, mock.Anything)
}
