package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

// Isolation is enforced by every query filtering on both halves of the tenancy,
// so it has to be tested against a real database. These assert that data written
// under one account is invisible to another -- including a sibling account
// inside the same organization, which is the case a single-layer tenancy check
// would miss.
//
// The tests call the generated queries the way the application does, through the
// pool for a read and through store.InTx for a write, so what is exercised is
// the SQL rather than a layer standing in front of it.

func openPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

// db is the test's own front for the generated queries: the same two calls the
// application makes, named so that the assertions below read as intentions
// rather than as parameter structs.
type db struct {
	pool *pgxpool.Pool
	t    *testing.T
}

func openDB(t *testing.T) db { return db{pool: openPool(t), t: t} }

func (d db) InsertTicket(ctx context.Context, sc store.Scope, in sqlcgen.InsertTicketParams) (sqlcgen.Ticket, error) {
	in.OrgID, in.AccountID = sc.OrgID, sc.AccountID
	var out sqlcgen.Ticket
	err := store.InTx(ctx, d.pool, func(q *sqlcgen.Queries) error {
		var err error
		out, err = q.InsertTicket(ctx, in)
		return err
	})
	return out, store.MapError(err)
}

func (d db) RecentTickets(ctx context.Context, sc store.Scope, project string, limit int32) ([]sqlcgen.Ticket, error) {
	return store.Read(d.pool).RecentTickets(ctx, sqlcgen.RecentTicketsParams{
		OrgID: sc.OrgID, AccountID: sc.AccountID, ProjectKey: project, RowLimit: limit,
	})
}

func (d db) TicketByIssueKey(ctx context.Context, sc store.Scope, key string) (sqlcgen.Ticket, error) {
	row, err := store.Read(d.pool).TicketByIssueKey(ctx, sqlcgen.TicketByIssueKeyParams{
		OrgID: sc.OrgID, AccountID: sc.AccountID, IssueKey: key,
	})
	return row, store.MapError(err)
}

func (d db) ReplaceConnection(ctx context.Context, sc store.Scope, in sqlcgen.InsertConnectionParams) (uuid.UUID, error) {
	in.OrgID, in.AccountID = sc.OrgID, sc.AccountID
	if in.Metadata == nil {
		in.Metadata = json.RawMessage(`{}`)
	}
	var id uuid.UUID
	err := store.InTx(ctx, d.pool, func(q *sqlcgen.Queries) error {
		if _, err := q.RevokeConnectionsOfType(ctx, sqlcgen.RevokeConnectionsOfTypeParams{
			OrgID: sc.OrgID, AccountID: sc.AccountID, ConnectorType: in.ConnectorType,
		}); err != nil {
			return err
		}
		var err error
		id, err = q.InsertConnection(ctx, in)
		return err
	})
	return id, store.MapError(err)
}

func (d db) ActiveConnection(ctx context.Context, sc store.Scope, connectorType string) (sqlcgen.ActiveConnectionRow, error) {
	row, err := store.Read(d.pool).ActiveConnection(ctx, sqlcgen.ActiveConnectionParams{
		OrgID: sc.OrgID, AccountID: sc.AccountID, ConnectorType: connectorType,
	})
	return row, store.MapError(err)
}

func (d db) ListConnections(ctx context.Context, sc store.Scope) ([]sqlcgen.ListConnectionsRow, error) {
	return store.Read(d.pool).ListConnections(ctx, sqlcgen.ListConnectionsParams{
		OrgID: sc.OrgID, AccountID: sc.AccountID,
	})
}

func (d db) UpsertUser(ctx context.Context, sc store.Scope, subject, email, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := store.InTx(ctx, d.pool, func(q *sqlcgen.Queries) error {
		var err error
		id, err = q.UpsertUser(ctx, sqlcgen.UpsertUserParams{
			OrgID: sc.OrgID, AccountID: sc.AccountID,
			Subject: subject, Email: email, DisplayName: name,
		})
		return err
	})
	return id, store.MapError(err)
}

// seedAccount creates an organization (or reuses one) plus an account and a
// user, returning the scope they form.
func seedAccount(t *testing.T, orgID uuid.UUID, label string) (store.Scope, uuid.UUID, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	suffix := uuid.NewString()[:8]
	if orgID == uuid.Nil {
		if err := conn.QueryRow(ctx,
			`insert into organizations (name, slug) values ($1, $2) returning id`,
			"org-"+suffix, "org-"+suffix).Scan(&orgID); err != nil {
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
	}

	var accountID uuid.UUID
	if err := conn.QueryRow(ctx,
		`insert into accounts (org_id, name, slug) values ($1, $2, $3) returning id`,
		orgID, label+"-"+suffix, label+"-"+suffix).Scan(&accountID); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	email := label + "-" + suffix + "@example.test"
	var userID uuid.UUID
	if err := conn.QueryRow(ctx,
		`insert into users (org_id, account_id, subject, email, display_name)
		 values ($1, $2, $3, $4, $5) returning id`,
		orgID, accountID, "dev|"+suffix, email, label).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	return store.Scope{OrgID: orgID, AccountID: accountID}, userID, email
}

func newTicket(project, issue string) sqlcgen.InsertTicketParams {
	return sqlcgen.InsertTicketParams{
		ProjectKey: project, IssueKey: issue,
		IssueUrl: "https://example.test/browse/" + issue,
		Summary:  "Stale service account", Source: store.SourceUI,
	}
}

// The case a single-layer check would miss: two accounts inside one org.
func TestAccountsInSameOrgAreIsolated(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	scopeA, _, _ := seedAccount(t, uuid.Nil, "acct-a")
	scopeB, _, _ := seedAccount(t, scopeA.OrgID, "acct-b")
	if scopeA.OrgID != scopeB.OrgID {
		t.Fatal("test setup: accounts should share an organization")
	}

	if _, err := db.InsertTicket(ctx, scopeA, newTicket("NHI", "NHI-1")); err != nil {
		t.Fatalf("insert into account A: %v", err)
	}
	if _, err := db.InsertTicket(ctx, scopeB, newTicket("NHI", "NHI-2")); err != nil {
		t.Fatalf("insert into account B: %v", err)
	}

	got, err := db.RecentTickets(ctx, scopeA, "", 10)
	if err != nil {
		t.Fatalf("recent tickets: %v", err)
	}
	if len(got) != 1 || got[0].IssueKey != "NHI-1" {
		t.Fatalf("account A saw %d tickets %v, want only NHI-1", len(got), keys(got))
	}

	if _, err := db.TicketByIssueKey(ctx, scopeA, "NHI-2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("account A read account B's ticket: %v", err)
	}
}

func TestOrganizationsAreIsolated(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	scopeA, _, _ := seedAccount(t, uuid.Nil, "org-a")
	scopeB, _, _ := seedAccount(t, uuid.Nil, "org-b")

	if _, err := db.InsertTicket(ctx, scopeA, newTicket("NHI", "NHI-1")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := db.RecentTickets(ctx, scopeB, "", 10)
	if err != nil {
		t.Fatalf("recent tickets: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("organization B saw %v", keys(got))
	}
}

// An incomplete tenancy is refused where one is created -- in the auth
// middleware and at the Temporal boundary -- not here. What this checks is that
// reaching the database with one anyway is still harmless: a half-empty scope
// matches no rows and writes no rows, because both columns are in every
// predicate. It is the backstop behind the check, not the check.
func TestAnIncompleteScopeMatchesAndWritesNothing(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	scope, _, _ := seedAccount(t, uuid.Nil, "acct")

	if _, err := db.InsertTicket(ctx, scope, newTicket("NHI", "NHI-REAL")); err != nil {
		t.Fatalf("seed a ticket: %v", err)
	}

	for name, sc := range map[string]store.Scope{
		"empty":           {},
		"no account":      {OrgID: scope.OrgID},
		"no organization": {AccountID: scope.AccountID},
	} {
		got, err := db.RecentTickets(ctx, sc, "", 10)
		if err != nil {
			t.Errorf("%s scope: %v", name, err)
		}
		if len(got) != 0 {
			t.Errorf("%s scope read %v", name, keys(got))
		}

		// The write is refused by the foreign key on org_id and account_id:
		// there is no such account, so there is nothing to attach a row to.
		if _, err := db.InsertTicket(ctx, sc, newTicket("NHI", "NHI-X")); err == nil {
			t.Errorf("%s scope wrote a ticket", name)
		}
	}
}

// Mixing one account's org with another's account must match nothing, rather
// than falling back to matching on either column alone.
func TestMismatchedScopeHalvesMatchNothing(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	scopeA, _, _ := seedAccount(t, uuid.Nil, "acct-a")
	scopeB, _, _ := seedAccount(t, uuid.Nil, "acct-b")

	if _, err := db.InsertTicket(ctx, scopeA, newTicket("NHI", "NHI-1")); err != nil {
		t.Fatalf("insert: %v", err)
	}

	crossed := store.Scope{OrgID: scopeB.OrgID, AccountID: scopeA.AccountID}
	got, err := db.RecentTickets(ctx, crossed, "", 10)
	if err != nil {
		t.Fatalf("recent tickets: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("crossed scope returned %v", keys(got))
	}
}

func TestConnectionsAreScopedAndCarryNoPlaintext(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	scopeA, userA, _ := seedAccount(t, uuid.Nil, "acct-a")
	scopeB, _, _ := seedAccount(t, scopeA.OrgID, "acct-b")

	id, err := db.ReplaceConnection(ctx, scopeA, sqlcgen.InsertConnectionParams{
		ConnectorType:   "jira",
		DisplayName:     "Acme Jira",
		Metadata:        json.RawMessage(`{"siteUrl":"https://acme.atlassian.net"}`),
		ConfigEncrypted: []byte("ciphertext"),
		CreatedBy:       &userA,
	})
	if err != nil {
		t.Fatalf("replace connection: %v", err)
	}

	if _, err := db.ActiveConnection(ctx, scopeB, "jira"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sibling account read the connection: %v", err)
	}

	got, err := db.ActiveConnection(ctx, scopeA, "jira")
	if err != nil {
		t.Fatalf("active connection: %v", err)
	}
	if got.ID != id || string(got.ConfigEncrypted) != "ciphertext" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	// A listing is what reaches an API response, and its query does not select
	// the encrypted configuration. The type therefore has no field for one --
	// this asserts that what goes on the wire agrees.
	list, err := db.ListConnections(ctx, scopeA)
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d connections, want 1", len(list))
	}
	encoded, err := json.Marshal(list[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"ciphertext", "onfigEncrypted", "rgId", "ccountId"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Errorf("serialised connection contains %q: %s", forbidden, encoded)
		}
	}
}

// Reconnecting must leave exactly one active connection.
func TestReplaceConnectionRevokesThePrevious(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	scope, userID, _ := seedAccount(t, uuid.Nil, "acct")

	mk := func(marker string) sqlcgen.InsertConnectionParams {
		return sqlcgen.InsertConnectionParams{
			ConnectorType:   "jira",
			ConfigEncrypted: []byte(marker),
			CreatedBy:       &userID,
		}
	}
	if _, err := db.ReplaceConnection(ctx, scope, mk("first")); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if _, err := db.ReplaceConnection(ctx, scope, mk("second")); err != nil {
		t.Fatalf("reconnect: %v", err)
	}

	got, err := db.ActiveConnection(ctx, scope, "jira")
	if err != nil {
		t.Fatalf("active connection: %v", err)
	}
	if string(got.ConfigEncrypted) != "second" {
		t.Errorf("active connection is %q, want the reconnected one", got.ConfigEncrypted)
	}
}

// People are a projection of what a token asserts, keyed by subject. This
// application stores no credential for them, so the only behaviour worth
// asserting is that the projection converges.
func TestUpsertUserIsKeyedBySubject(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	scope, _, _ := seedAccount(t, uuid.Nil, "acct")

	subject := "dev|" + uuid.NewString()[:8]

	first, err := db.UpsertUser(ctx, scope, subject, "before@example.test", "Before")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// The provider changed their details. The same subject must resolve to the
	// same row rather than creating a second one.
	second, err := db.UpsertUser(ctx, scope, subject, "after@example.test", "After")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if first != second {
		t.Fatalf("the same subject produced two rows: %s and %s", first, second)
	}
}

// A retried workflow activity must be able to tell that its previous attempt
// already recorded the ticket.
func TestInsertTicketIsIdempotentPerAccount(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	scopeA, _, _ := seedAccount(t, uuid.Nil, "acct-a")
	scopeB, _, _ := seedAccount(t, scopeA.OrgID, "acct-b")

	if _, err := db.InsertTicket(ctx, scopeA, newTicket("NHI", "NHI-1")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.InsertTicket(ctx, scopeA, newTicket("NHI", "NHI-1")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate insert: got %v, want ErrConflict", err)
	}
	// The same issue key in a different account is a different ticket.
	if _, err := db.InsertTicket(ctx, scopeB, newTicket("NHI", "NHI-1")); err != nil {
		t.Fatalf("same key in another account: %v", err)
	}
}

// A provider can hand back a title longer than the column, and losing the
// ticket over it would be the wrong trade. The bound lives in the SQL, next to
// the column width it respects, so this checks the query and not a Go helper.
func TestALongSummaryIsTruncatedRatherThanRejected(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	scope, _, _ := seedAccount(t, uuid.Nil, "acct")

	ticket := newTicket("NHI", "NHI-LONG")
	ticket.Summary = strings.Repeat("x", 900)

	got, err := db.InsertTicket(ctx, scope, ticket)
	if err != nil {
		t.Fatalf("insert a ticket with an over-long summary: %v", err)
	}
	if len(got.Summary) != 500 {
		t.Errorf("stored summary is %d characters, want it bounded at 500", len(got.Summary))
	}
}

func keys(ts []sqlcgen.Ticket) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.IssueKey)
	}
	return out
}
