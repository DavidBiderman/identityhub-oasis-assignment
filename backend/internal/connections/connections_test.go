package connections_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dbiderman/identityhub/backend/internal/capabilities/issuetracker"
	"github.com/dbiderman/identityhub/backend/internal/connections"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/connector/jira"
	"github.com/dbiderman/identityhub/backend/internal/crypto"
	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

// This package is the only one that decrypts a stored credential, and Configure
// is the only sequence that does it: read the row, decrypt, decode, check the
// capability. What is worth testing is the order and the failures, both of
// which need a real database and a real key service -- a decrypt that is not a
// decrypt proves nothing about the step that matters.

func service(t *testing.T) (*connections.Service, *pgxpool.Pool, store.Scope) {
	t.Helper()

	for _, key := range []string{"TEST_DATABASE_URL", "AWS_ENDPOINT_URL"} {
		if os.Getenv(key) == "" {
			t.Skipf("%s not set; skipping", key)
		}
	}

	ctx := context.Background()
	pool, err := store.OpenPool(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)

	keeper, err := crypto.OpenKeeper(ctx, "awskms://alias/identityhub")
	if err != nil {
		t.Fatalf("open keeper: %v", err)
	}
	t.Cleanup(func() { _ = keeper.Close() })

	registry := connector.NewRegistry()
	jira.Register(registry, nil)

	return connections.New(pool, keeper, registry), pool, seedScope(t, pool)
}

// seedScope creates a tenancy of its own and removes it afterwards, so a run
// leaves the demo data exactly as it found it.
func seedScope(t *testing.T, pool *pgxpool.Pool) store.Scope {
	t.Helper()

	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	var scope store.Scope
	err := store.InTx(ctx, pool, func(q *sqlcgen.Queries) error {
		row := pool.QueryRow(ctx,
			`insert into organizations (name, slug) values ($1, $1) returning id`, "conn-"+suffix)
		if err := row.Scan(&scope.OrgID); err != nil {
			return err
		}
		return pool.QueryRow(ctx,
			`insert into accounts (org_id, name, slug) values ($1, $2, $2) returning id`,
			scope.OrgID, "acct-"+suffix).Scan(&scope.AccountID)
	})
	if err != nil {
		t.Fatalf("seed tenancy: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`delete from organizations where id = $1`, scope.OrgID)
	})
	return scope
}

// An account with no connection is a different situation from one whose
// credential was rejected, and the remedy differs: connect, rather than
// reconnect. The API turns this into a 409 the interface can act on.
func TestAnAccountWithNoConnectionSaysSo(t *testing.T) {
	service, _, scope := service(t)

	_, err := service.Configure(context.Background(), scope, "jira")
	if !errors.Is(err, connections.ErrNotConnected) {
		t.Fatalf("got %v, want ErrNotConnected", err)
	}
}

// A row this application cannot decrypt is unusable, not a server error. It
// happens when a key is rotated without re-wrapping, and the answer a person
// needs is "reconnect", not "something went wrong".
func TestARowThatCannotBeDecryptedAsksForAReconnect(t *testing.T) {
	service, pool, scope := service(t)
	ctx := context.Background()

	// Ciphertext from a different key. Nothing in this application can read it.
	err := store.InTx(ctx, pool, func(q *sqlcgen.Queries) error {
		_, err := q.InsertConnection(ctx, sqlcgen.InsertConnectionParams{
			OrgID:           scope.OrgID,
			AccountID:       scope.AccountID,
			ConnectorType:   "jira",
			DisplayName:     "broken",
			Metadata:        []byte(`{}`),
			ConfigEncrypted: []byte("not ciphertext this key produced"),
		})
		return err
	})
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}

	_, err = service.Configure(ctx, scope, "jira")
	if !errors.Is(err, connector.ErrConfigInvalid) {
		t.Fatalf("got %v, want ErrConfigInvalid", err)
	}
}

// The tenancy is not advisory. A connection belongs to one account, and the
// sibling account in the same organization is the case a single-layer check
// would miss.
func TestAConnectionIsInvisibleToAnotherAccount(t *testing.T) {
	service, pool, scope := service(t)
	ctx := context.Background()

	var sibling uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into accounts (org_id, name, slug) values ($1, $2, $2) returning id`,
		scope.OrgID, "sibling-"+uuid.NewString()[:8]).Scan(&sibling); err != nil {
		t.Fatalf("seed sibling account: %v", err)
	}

	err := store.InTx(ctx, pool, func(q *sqlcgen.Queries) error {
		_, err := q.InsertConnection(ctx, sqlcgen.InsertConnectionParams{
			OrgID:           scope.OrgID,
			AccountID:       scope.AccountID,
			ConnectorType:   "jira",
			DisplayName:     "theirs",
			Metadata:        []byte(`{}`),
			ConfigEncrypted: []byte("irrelevant; the read must not get this far"),
		})
		return err
	})
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}

	_, err = service.Configure(ctx, store.Scope{OrgID: scope.OrgID, AccountID: sibling}, "jira")
	if !errors.Is(err, connections.ErrNotConnected) {
		t.Fatalf("the sibling account saw another account's connection: %v", err)
	}
}

// Save verifies a credential before storing it. A credential that has never
// worked would otherwise leave an account in a state that only reveals itself
// when somebody tries to file a ticket.
func TestSaveRefusesACredentialThatDoesNotWork(t *testing.T) {
	service, pool, scope := service(t)
	ctx := context.Background()

	// A well-formed configuration pointing at nothing. Resolving it has to fail
	// before anything is written.
	config := []byte(`{"siteUrl":"https://not-a-real-site.invalid",` +
		`"email":"a@b.co","apiToken":"nope"}`)

	if _, _, err := service.Save(ctx, scope, "jira", config, uuid.Nil); err == nil {
		t.Fatal("a credential that cannot be verified was stored")
	}

	var stored int
	if err := pool.QueryRow(ctx,
		`select count(*) from connections where org_id = $1 and account_id = $2`,
		scope.OrgID, scope.AccountID).Scan(&stored); err != nil {
		t.Fatalf("count connections: %v", err)
	}
	if stored != 0 {
		t.Errorf("%d connections were written for a credential that never worked", stored)
	}
}

// The caller's plaintext buffer is overwritten before Save returns, on every
// path out of it. A credential that lingers in a buffer the caller still holds
// is a credential in a heap dump.
func TestSaveZeroesTheCallersPlaintext(t *testing.T) {
	service, _, scope := service(t)

	config := []byte(`{"siteUrl":"https://not-a-real-site.invalid",` +
		`"email":"a@b.co","apiToken":"the-secret"}`)
	before := make([]byte, len(config))
	copy(before, config)

	// This one fails, which is the path most likely to skip the cleanup.
	_, _, _ = service.Save(context.Background(), scope, "jira", config, uuid.Nil)

	if bytes.Equal(config, before) {
		t.Fatal("the plaintext configuration was left intact after Save returned")
	}
	for i, b := range config {
		if b != 0 {
			t.Fatalf("byte %d is %q, want the buffer zeroed", i, b)
		}
	}
}

// A stored connection whose type is no longer registered is refused, and named.
//
// The row has to exist for this to test anything: without one, Configure
// returns ErrNotConnected from the read and never reaches the registry, so the
// assertion would pass without entering the branch it is about. That happens
// when a connector is removed from a build that still has rows pointing at it.
func TestAStoredConnectionOfAnUnregisteredTypeIsRefused(t *testing.T) {
	service, pool, scope := service(t)
	ctx := context.Background()

	err := store.InTx(ctx, pool, func(q *sqlcgen.Queries) error {
		_, err := q.InsertConnection(ctx, sqlcgen.InsertConnectionParams{
			OrgID:           scope.OrgID,
			AccountID:       scope.AccountID,
			ConnectorType:   "github",
			DisplayName:     "a connector this build does not have",
			Metadata:        []byte(`{}`),
			ConfigEncrypted: []byte("unreachable; the registry fails first"),
		})
		return err
	})
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}

	_, err = service.Configure(ctx, scope, "github")
	switch {
	case err == nil:
		t.Fatal("a connection of an unregistered type was opened")
	case errors.Is(err, connections.ErrNotConnected):
		t.Fatal("the row was not found, so this never reached the registry")
	case !strings.Contains(err.Error(), "github"):
		t.Errorf("message = %q, want it to name the type", err)
	}
}

// The type checked against the contract is the one the row names. A connector
// that is registered but cannot file findings must be refused at Configure,
// not inside a workflow.
func TestConfigureChecksTheIssueTrackerContract(t *testing.T) {
	t.Parallel()

	// jira implements the whole contract, which is what makes it usable here.
	// The negative case is covered offline in the capabilities package, where a
	// stub can declare four of five actions without a database.
	if err := issuetracker.Implements(mustGet(t, "jira")); err != nil {
		t.Fatalf("the Jira connector does not satisfy the contract it is used through: %v", err)
	}
}

func mustGet(t *testing.T, typ connector.Type) connector.Connector {
	t.Helper()

	registry := connector.NewRegistry()
	jira.Register(registry, nil)
	c, err := registry.Get(typ)
	if err != nil {
		t.Fatalf("get %s: %v", typ, err)
	}
	return c
}
