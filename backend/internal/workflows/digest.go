package workflows

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dbiderman/identityhub/backend/internal/blog"
	"github.com/dbiderman/identityhub/backend/internal/capabilities/issuetracker"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

// BlogReader fetches recent posts. Declared here, where it is consumed, so the
// workflow package depends on a behaviour rather than an implementation.
//
// The behaviour is declared here; the data is not. blog.Post belongs to the
// blog, and naming it keeps the dependency pointing the way dependencies should
// point -- this package already imports everything, and blog now imports
// nothing of ours.
type BlogReader interface {
	Recent(ctx context.Context, limit int) ([]blog.Post, error)
}

// Summarizer condenses a post into a few sentences.
//
// The offline implementation exists so that the test suite and a
// network-isolated `docker compose up` never require an API key: the digest is
// a demonstration, and it should not be the reason the stack fails to run.
type Summarizer interface {
	Summarize(ctx context.Context, p blog.Post) (string, error)
}

// DigestInput identifies whose digest to run. As with FileTicket, it carries a
// tenancy scope and never a credential.
type DigestInput struct {
	OrgID         uuid.UUID      `json:"orgId"`
	AccountID     uuid.UUID      `json:"accountId"`
	ConnectorType connector.Type `json:"connectorType"`
	TargetKey     string         `json:"targetKey"`
	MaxPosts      int            `json:"maxPosts"`
}

// DigestResult reports what a run did.
type DigestResult struct {
	Considered int      `json:"considered"`
	Filed      []string `json:"filed"`
	Skipped    int      `json:"skipped"`
}

func (in DigestInput) scope() store.Scope {
	return store.Scope{OrgID: in.OrgID, AccountID: in.AccountID}
}

// BlogDigest fetches recent posts, skips ones already catalogued, and files a
// ticket for each new one.
//
// The schedule re-reads the same feed every day, so deduplication is what stops
// it filing a fresh ticket for the same post on every run.
func BlogDigest(ctx workflow.Context, in DigestInput) (DigestResult, error) {
	// As in FileTicket: the tenancy re-enters here, so it is checked here.
	if err := in.scope().Validate(); err != nil {
		return DigestResult{}, temporal.NewNonRetryableApplicationError(
			"This schedule was created without a complete tenancy.", "invalid_input", err)
	}

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy:         retryOnTransientOnly,
	})

	// A nil *Activities is deliberate and safe. Temporal resolves an activity
	// by name; a.Configure below is a method value used only to supply that
	// name at compile time, and the worker invokes it on the instance it was
	// registered with. This nil is never dereferenced.
	var a *Activities

	// Configure first. A digest run that cannot file anything should not spend a
	// model call discovering that: summarising happens per post, and the
	// connection is the same for all of them.
	var conn ConfiguredConnection
	if err := workflow.ExecuteActivity(ctx, a.Configure, ConfigureInput{
		OrgID: in.OrgID, AccountID: in.AccountID, ConnectorType: in.ConnectorType,
	}).Get(ctx, &conn); err != nil {
		return DigestResult{}, err
	}

	// Where to file. The configured key wins; without one, the account's first
	// project does, so the digest works without anybody setting a variable.
	if in.TargetKey == "" {
		var resolved string
		if err := workflow.ExecuteActivity(ctx, a.ResolveTarget, ConfigureInput{
			OrgID: in.OrgID, AccountID: in.AccountID, ConnectorType: in.ConnectorType,
		}).Get(ctx, &resolved); err != nil {
			return DigestResult{}, err
		}
		in.TargetKey = resolved
	}

	var newPosts []blog.Post
	if err := workflow.ExecuteActivity(ctx, a.FetchNewPosts, in).Get(ctx, &newPosts); err != nil {
		return DigestResult{}, err
	}

	result := DigestResult{Considered: len(newPosts)}
	for _, post := range newPosts {
		// Each post is filed independently. One post that cannot be summarized
		// or filed must not stop the rest of the digest, so the error is
		// recorded and the loop continues.
		var filed FiledPost
		err := workflow.ExecuteActivity(ctx, a.FilePost, FilePostInput{Digest: in, Post: post}).Get(ctx, &filed)
		if err != nil {
			workflow.GetLogger(ctx).Warn("could not file blog post",
				"url", post.URL, "error", err.Error())
			auditDigestFailure(ctx, a, in, post, err)
			result.Skipped++
			continue
		}
		result.Filed = append(result.Filed, filed.IssueKey)
	}
	return result, nil
}

// auditDigestFailure records that a post was not filed, exactly once.
//
// The same arrangement as auditFilingFailure, for the same reason: FilePost
// runs up to five times and every attempt looks identical from inside it, so
// only the workflow can tell a failed attempt from a post that is not going to
// be filed at all. Without it, a digest that failed every night left the audit
// trail empty, which reads as a digest that never ran.
func auditDigestFailure(ctx workflow.Context, a *Activities, in DigestInput, post blog.Post, cause error) {
	audit := workflow.WithActivityOptions(ctx, bestEffort)
	_ = workflow.ExecuteActivity(audit, a.RecordDigestFailure, DigestFailureInput{
		Digest: in, Post: post, Reason: cause.Error(),
	}).Get(audit, nil)
}

// FilePostInput carries one post through the filing step.
type FilePostInput struct {
	Digest DigestInput `json:"digest"`
	Post   blog.Post   `json:"post"`
}

// DigestFailureInput carries the one terminal failure to the audit trail.
type DigestFailureInput struct {
	Digest DigestInput `json:"digest"`
	Post   blog.Post   `json:"post"`
	Reason string      `json:"reason"`
}

// FiledPost is the outcome of filing one post.
type FiledPost struct {
	IssueKey string `json:"issueKey"`
}

const (
	// defaultDigestPosts is one run's worth on a busy week.
	defaultDigestPosts = 5

	// maxDigestPosts is a ceiling on how many tickets one scheduled run can
	// file. A feed that suddenly lists fifty posts is a feed change, not fifty
	// findings somebody wants in their backlog on a Monday morning.
	maxDigestPosts = 20
)

// FetchNewPosts reads the feed and drops posts already catalogued.
func (a *Activities) FetchNewPosts(ctx context.Context, in DigestInput) ([]blog.Post, error) {
	// Clamped, not collapsed. Asking for 30 used to return 5 -- the default --
	// which reads as the request having been ignored rather than capped.
	limit := min(max(cmp.Or(in.MaxPosts, defaultDigestPosts), 1), maxDigestPosts)

	posts, err := a.Blog.Recent(ctx, limit)
	if err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return nil, nil
	}

	urls := make([]string, 0, len(posts))
	for _, p := range posts {
		urls = append(urls, p.URL)
	}
	scope := in.scope()
	catalogued, err := store.Read(a.Pool).KnownBlogURLs(ctx, sqlcgen.KnownBlogURLsParams{
		OrgID: scope.OrgID, AccountID: scope.AccountID, PostUrls: urls,
	})
	if err != nil {
		return nil, fmt.Errorf("read the catalogued blog posts: %w", err)
	}

	known := make(map[string]bool, len(catalogued))
	for _, u := range catalogued {
		known[u] = true
	}

	fresh := make([]blog.Post, 0, len(posts))
	for _, p := range posts {
		if !known[p.URL] {
			fresh = append(fresh, p)
		}
	}
	return fresh, nil
}

// FilePost summarizes a post, files a ticket for it, and catalogues it.
func (a *Activities) FilePost(ctx context.Context, in FilePostInput) (FiledPost, error) {
	scope := in.Digest.scope()

	summary, err := a.Summarizer.Summarize(ctx, in.Post)
	if err != nil {
		return FiledPost{}, err
	}

	conn, err := a.Connections.Configure(ctx, scope, in.Digest.ConnectorType)
	if err != nil {
		return FiledPost{}, classifyForRetry(err)
	}

	ref, err := conn.FileTicket(ctx, issuetracker.NewFinding{
		TargetKey:   in.Digest.TargetKey,
		Title:       digestTitle(in.Post.Title),
		Description: digestBody(in.Post, summary),
		Labels:      []string{"identityhub", "nhi-blog-digest"},
	})
	if err != nil {
		a.Connections.NoteFailure(ctx, scope, conn.ID, err)
		return FiledPost{}, classifyForRetry(err)
	}

	var ticket sqlcgen.Ticket
	err = store.InTx(ctx, a.Pool, func(q *sqlcgen.Queries) error {
		var err error
		ticket, err = q.InsertTicket(ctx, sqlcgen.InsertTicketParams{
			OrgID:         scope.OrgID,
			AccountID:     scope.AccountID,
			ProjectKey:    in.Digest.TargetKey,
			IssueKey:      ref.Key,
			IssueID:       ref.ExternalID,
			IssueUrl:      ref.URL,
			Summary:       ref.Summary,
			Source:        store.SourceAutomation,
			JiraCreatedAt: nullableTime(ref.CreatedAt),
		})
		return err
	})
	if err = store.MapError(err); err != nil && !isConflict(err) {
		return FiledPost{}, fmt.Errorf("record ticket %s: %w", ref.Key, err)
	}

	// Cataloguing is what makes the next run skip this post. The unique index
	// is the real guarantee: a replayed or concurrent workflow is rejected by
	// the database rather than by application ordering.
	err = store.InTx(ctx, a.Pool, func(q *sqlcgen.Queries) error {
		return q.RecordBlogPost(ctx, sqlcgen.RecordBlogPostParams{
			OrgID:       scope.OrgID,
			AccountID:   scope.AccountID,
			PostUrl:     in.Post.URL,
			Title:       in.Post.Title,
			PublishedAt: nullableTime(in.Post.PublishedAt),
			Summary:     summary,
			TicketID:    store.NullableUUID(ticket.ID),
		})
	})
	if err = store.MapError(err); err != nil && !isConflict(err) {
		return FiledPost{}, fmt.Errorf("catalogue the blog post %s: %w", in.Post.URL, err)
	}

	a.audit(ctx, scope, FileTicketInput{
		ActorType: store.ActorSystem,
		Finding:   NewFinding{TargetKey: in.Digest.TargetKey},
	}, store.ActionDigestRan, map[string]any{
		"issueKey": ref.Key,
		"postUrl":  in.Post.URL,
	})

	return FiledPost{IssueKey: ref.Key}, nil
}

// RecordDigestFailure appends the audit event for a post that was not filed.
//
// It is the digest's counterpart to RecordFilingFailure, and an activity of its
// own for the same reason: FilePost cannot write this, because it does not know
// which of its attempts was the last one. The post is named as well as the
// reason, so the trail says which post the run gave up on rather than only that
// something failed.
func (a *Activities) RecordDigestFailure(ctx context.Context, in DigestFailureInput) error {
	a.audit(ctx, in.Digest.scope(), FileTicketInput{
		ActorType: store.ActorSystem,
		Finding:   NewFinding{TargetKey: in.Digest.TargetKey},
	}, store.ActionDigestFailed, map[string]any{
		"postTitle": in.Post.Title,
		"postUrl":   in.Post.URL,
		"reason":    in.Reason,
	})
	return nil
}

// digestTitle keeps the ticket summary inside Jira's limit while staying
// recognisable in a list.
//
// Counted in runes, not bytes. The input is a blog title: one curly quote or em
// dash and a byte cut lands mid-character, which json.Marshal then replaces
// with U+FFFD on the way to Jira. It also matches what the API's own 255
// "characters" means, in validate.go.
func digestTitle(title string) string {
	const prefix = "NHI Blog Digest: "
	const maxSummary = 255

	title = strings.TrimSpace(title)
	if title == "" {
		title = "New post"
	}
	if utf8.RuneCountInString(prefix)+utf8.RuneCountInString(title) > maxSummary {
		runes := []rune(title)
		title = string(runes[:maxSummary-utf8.RuneCountInString(prefix)-1]) + "…"
	}
	return prefix + title
}

// digestBody assembles the ticket description. The source link comes first so
// a reader can always reach the original, whatever the summary says.
func digestBody(p blog.Post, summary string) string {
	var b strings.Builder
	b.WriteString(p.Title)
	b.WriteString("\n")
	b.WriteString(p.URL)
	if !p.PublishedAt.IsZero() {
		fmt.Fprintf(&b, "\nPublished %s", p.PublishedAt.Format("2 January 2006"))
	}
	b.WriteString("\n\nSummary\n")
	b.WriteString(summary)
	b.WriteString("\n\nThis ticket was created automatically by the IdentityHub NHI Blog Digest.")
	return b.String()
}
