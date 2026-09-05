package workflows

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

// Claiming an idempotency key is three statements that have to agree with each
// other, so it is tested against a real database rather than a mock: what is
// being asserted is that the transaction and the unique index behave, and
// neither exists in a fake.

func TestClaimingAKeyIsExactlyOnceAndReplayable(t *testing.T) {
	a, scope := activitiesWithDB(t)
	ctx := context.Background()
	hash := []byte("request-hash")

	claimed, ticketID, err := a.claimKey(ctx, scope, "key-1", hash)
	if err != nil || !claimed || ticketID != nil {
		t.Fatalf("first claim: claimed=%v ticket=%v err=%v", claimed, ticketID, err)
	}

	// A replay before the work finished must not claim it a second time, and
	// must not report a result that does not exist yet.
	claimed, ticketID, err = a.claimKey(ctx, scope, "key-1", hash)
	if err != nil || claimed || ticketID != nil {
		t.Fatalf("replay before completion: claimed=%v ticket=%v err=%v", claimed, ticketID, err)
	}

	ticket := insertTicket(t, a, scope, "NHI-9")
	if err := a.completeKey(ctx, scope, "key-1", ticket); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Now a replay gets the original ticket back rather than filing a second.
	claimed, ticketID, err = a.claimKey(ctx, scope, "key-1", hash)
	if err != nil || claimed || ticketID == nil || *ticketID != ticket {
		t.Fatalf("replay after completion: claimed=%v ticket=%v err=%v", claimed, ticketID, err)
	}
}

func TestReusingAKeyWithADifferentBodyIsRefused(t *testing.T) {
	a, scope := activitiesWithDB(t)
	ctx := context.Background()

	if _, _, err := a.claimKey(ctx, scope, "key-2", []byte("original")); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// Returning the first result would be wrong and filing a second ticket
	// would defeat the key, so this is the one case that is an error.
	_, _, err := a.claimKey(ctx, scope, "key-2", []byte("different"))
	if !errors.Is(err, errIdempotencyMismatch) {
		t.Fatalf("mismatched replay: got %v, want errIdempotencyMismatch", err)
	}
}

func TestReleasingAKeyLetsTheCallerRetryWithIt(t *testing.T) {
	a, scope := activitiesWithDB(t)
	ctx := context.Background()
	hash := []byte("request-hash")

	if _, _, err := a.claimKey(ctx, scope, "key-3", hash); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := a.releaseKey(ctx, scope, "key-3"); err != nil {
		t.Fatalf("release: %v", err)
	}

	// A transient failure must not lock the caller out of their own key.
	claimed, _, err := a.claimKey(ctx, scope, "key-3", hash)
	if err != nil || !claimed {
		t.Fatalf("claim after release: claimed=%v err=%v", claimed, err)
	}
}

// activitiesWithDB returns Activities bound to the test database, and a scope
// with a freshly seeded organization and account to work in.
func activitiesWithDB(t *testing.T) (*Activities, store.Scope) {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration tests")
	}
	pool, err := store.OpenPool(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open the pool: %v", err)
	}
	t.Cleanup(pool.Close)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	suffix := uuid.NewString()[:8]
	var orgID, accountID uuid.UUID
	if err := conn.QueryRow(ctx,
		`insert into organizations (name, slug) values ($1, $1) returning id`,
		"org-"+suffix).Scan(&orgID); err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		_, _ = c.Exec(context.Background(), `delete from organizations where id = $1`, orgID)
	})
	if err := conn.QueryRow(ctx,
		`insert into accounts (org_id, name, slug) values ($1, $2, $2) returning id`,
		orgID, "acct-"+suffix).Scan(&accountID); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	return &Activities{Pool: pool}, store.Scope{OrgID: orgID, AccountID: accountID}
}

func insertTicket(t *testing.T, a *Activities, sc store.Scope, issueKey string) uuid.UUID {
	t.Helper()

	ctx := context.Background()
	var id uuid.UUID
	err := store.InTx(ctx, a.Pool, func(q *sqlcgen.Queries) error {
		row, err := q.InsertTicket(ctx, sqlcgen.InsertTicketParams{
			OrgID:      sc.OrgID,
			AccountID:  sc.AccountID,
			ProjectKey: "NHI",
			IssueKey:   issueKey,
			IssueUrl:   "https://example.test/browse/" + issueKey,
			Summary:    "Stale service account",
			Source:     store.SourceAPI,
		})
		id = row.ID
		return err
	})
	if err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	return id
}
