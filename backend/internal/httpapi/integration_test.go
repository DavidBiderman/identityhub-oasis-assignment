package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/dbiderman/identityhub/backend/api"
	"github.com/dbiderman/identityhub/backend/internal/auth"
	"github.com/dbiderman/identityhub/backend/internal/cache"
	"github.com/dbiderman/identityhub/backend/internal/config"
	"github.com/dbiderman/identityhub/backend/internal/connections"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/connector/jira"
	"github.com/dbiderman/identityhub/backend/internal/crypto"
	"github.com/dbiderman/identityhub/backend/internal/httpapi"
	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
	"github.com/dbiderman/identityhub/backend/internal/workflows"
)

// This exercises the whole stack: real HTTP handlers, real PostgreSQL, real
// KMS, a real Temporal worker running real workflows, and a stub Jira. Only the
// provider is faked, because everything else is what is under test.
//
// It is skipped unless its dependencies are configured, so `go test ./...`
// still runs on a machine with nothing started.

type harness struct {
	t    *testing.T
	base string
	site string
	pool *pgxpool.Pool

	// The people this run seeded, in its own organization: an administrator, a
	// non-administrator in the same account, and an administrator in a sibling
	// account. Tests name them through the harness, so nothing here depends on
	// the demo data being present or leaves it changed.
	admin   string
	member  string
	sibling string

	// Kept so cleanup can find the Temporal schedules this run created.
	orgID      uuid.UUID
	accountIDs []uuid.UUID

	// Whether this harness enabled the identity provider stand-in, so a test
	// can assert the endpoint answers accordingly.
	tokens *auth.Tokens

	// prefix namespaces the issue keys this harness mints. Tests share one
	// database and one account, so without it every test would start at NHI-1
	// and collide on the unique (org, account, issue key) index.
	prefix string

	// router resolves a request to the operation the document describes, so a
	// response can be checked against what that operation says it returns.
	router routers.Router

	mu       sync.Mutex
	created  []string
	requests []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	for _, key := range []string{
		"TEST_DATABASE_URL", "AWS_ENDPOINT_URL", "TEST_TEMPORAL_HOSTPORT", "TEST_REDIS_URL",
	} {
		if os.Getenv(key) == "" {
			t.Skipf("%s not set; skipping end-to-end test", key)
		}
	}

	// The identity provider stand-in is on, as it is in development. A test can
	// read the flag to assert the endpoint answers accordingly.
	h := &harness{
		t: t, prefix: strings.ToUpper(uuid.NewString()[:4]),
	}
	h.tokens = auth.NewTokens([]byte(testSigningKey), testIssuer, testAudience, time.Hour)

	stub := httptest.NewTLSServer(http.HandlerFunc(h.serveJira))
	t.Cleanup(stub.Close)
	h.site = stub.URL

	document, err := openapi3.NewLoader().LoadFromData(api.Spec)
	if err != nil {
		t.Fatalf("read the OpenAPI document: %v", err)
	}
	if h.router, err = gorillamux.NewRouter(document); err != nil {
		t.Fatalf("route the OpenAPI document: %v", err)
	}

	ctx := context.Background()

	pool, err := store.OpenPool(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)
	h.pool = pool

	keeper, err := crypto.OpenKeeper(ctx, "awskms://alias/identityhub")
	if err != nil {
		t.Fatalf("open keeper: %v", err)
	}
	t.Cleanup(func() { _ = keeper.Close() })

	// The connector gets the stub's HTTP client, which trusts its certificate.
	// Nothing else about the connector is altered: it still validates the
	// config, still requires https, and still speaks the real Jira API shapes.
	registry := connector.NewRegistry()
	jira.Register(registry, stub.Client())

	temporalClient, err := client.Dial(client.Options{
		HostPort:  os.Getenv("TEST_TEMPORAL_HOSTPORT"),
		Namespace: "identityhub",
	})
	if err != nil {
		t.Fatalf("dial temporal: %v", err)
	}
	t.Cleanup(temporalClient.Close)

	// Connecting an integration creates a recurring digest schedule, and a
	// schedule outlives the database rows it was created from: deleting the
	// test's organization does not remove it, so without this a test run leaves
	// an orphan in Temporal that fires daily against an account that no longer
	// exists. Cleaning the database is not enough when the state is somewhere
	// else.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		for _, id := range h.scheduleIDs() {
			_ = temporalClient.ScheduleClient().GetHandle(ctx, id).Delete(ctx)
		}
	})

	// A Redis database of its own, so a run cannot see -- or expire -- the
	// sign-outs of the stack the developer is using. Opened the way the binary
	// opens it, timeouts and all, so what the suite exercises is the client the
	// API actually runs with.
	redisClient, err := cache.Open(ctx, os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatalf("open redis: %v", err)
	}
	t.Cleanup(func() { _ = redisClient.Close() })

	connections := connections.New(pool, keeper, registry)
	queue := "identityhub-test-" + uuid.NewString()[:8]

	w := worker.New(temporalClient, queue, worker.Options{})
	w.RegisterWorkflow(workflows.FileTicket)
	w.RegisterActivity(&workflows.Activities{Pool: pool, Connections: connections})
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(w.Stop)

	api := httpapi.New(httpapi.Deps{
		Log:         config.NewLogger(config.Development),
		Pool:        pool,
		APIKeys:     auth.NewAPIKeys(pool),
		Revocations: auth.NewRevocations(redisClient),
		Connections: connections,
		Registry:    registry,
		Temporal:    temporalClient,
		TaskQueue:   queue,
		PublicURL:   "http://localhost",

		UserResolver:  h.tokens,
		SignIn:        auth.NewSignIn(pool, h.tokens),
		TokenIssuer:   testIssuer,
		TokenAudience: testAudience,
	})

	// Its own people, before anything asks for a token.
	h.seedTenancy()

	routes, err := api.Routes()
	if err != nil {
		t.Fatalf("build routes from the OpenAPI document: %v", err)
	}
	srv := httptest.NewServer(routes)
	t.Cleanup(srv.Close)
	h.base = srv.URL
	return h
}

// serveJira is a stub Atlassian site.
func (h *harness) serveJira(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.requests = append(h.requests, r.Method+" "+r.URL.Path)
	h.mu.Unlock()

	if !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
		writeJSON(w, 401, map[string]any{"errorMessages": []string{"Client must be authenticated"}})
		return
	}
	w.Header().Set("Content-Type", "application/json")

	switch {
	case strings.HasSuffix(r.URL.Path, "/myself"):
		writeJSON(w, 200, map[string]any{
			"accountId": "5b10a", "emailAddress": "svc-jira@acme.test",
			"displayName": "Acme Service Account"})

	case strings.Contains(r.URL.Path, "/project/search"):
		writeJSON(w, 200, map[string]any{"values": []map[string]any{
			{"id": "10000", "key": "NHI", "name": "NHI Findings"},
			{"id": "10001", "key": "OPS", "name": "Operations"},
		}})

	case strings.Contains(r.URL.Path, "createmeta") && strings.Contains(r.URL.Path, "issuetypes"):
		writeJSON(w, 200, map[string]any{"issueTypes": []map[string]any{
			{"id": "10002", "name": "Task", "subtask": false},
			{"id": "10004", "name": "Sub-task", "subtask": true},
		}})

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rest/api/3/issue"):
		var payload struct {
			Fields struct {
				Summary     string `json:"summary"`
				Description any    `json:"description"`
			} `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if _, isADF := payload.Fields.Description.(map[string]any); !isADF {
			writeJSON(w, 400, map[string]any{
				"errors": map[string]string{"description": "Operation value must be an Atlassian Document"}})
			return
		}
		h.mu.Lock()
		key := fmt.Sprintf("%s-%d", h.prefix, len(h.created)+1)
		h.created = append(h.created, payload.Fields.Summary)
		h.mu.Unlock()
		writeJSON(w, 201, map[string]any{"id": "1" + key, "key": key})

	default:
		writeJSON(w, 404, map[string]any{"errorMessages": []string{"Not found"}})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// issuesCreated reports how many issues the stub filed.
func (h *harness) issuesCreated() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.created)
}

// The issuer and audience the test resolver expects. A token minted for
// anything else must be rejected, which is one of the things under test.
const (
	testSigningKey = "a-signing-key-for-tests-only"
	testIssuer     = "https://identityhub.test"
	testAudience   = "identityhub"
)

// client returns a plain HTTP client. There are no cookies: identity travels
// as a bearer token, exactly as it does from a real provider.
func (h *harness) client() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// tokenFor mints an access token for a seeded person, standing in for the
// identity provider.
func (h *harness) tokenFor(email string, roles ...auth.Role) string {
	h.t.Helper()

	person := h.person(email)
	token, _, err := h.tokens.Issue(auth.Identity{
		Subject:     person.Subject,
		Email:       person.Email,
		DisplayName: person.DisplayName,
		OrgID:       person.OrgID.String(),
		AccountID:   person.AccountID.String(),
		Roles:       roles,
	})
	if err != nil {
		h.t.Fatalf("issue a token for %s: %v", email, err)
	}
	return token
}

// person looks a seeded identity up the way signing in does.
//
// Through UserForSignIn rather than a scan of every user in every organization:
// that query existed for the development identities endpoint, which is gone, and
// an unscoped cross-tenant read has no business surviving in the query set for
// the convenience of a test harness.
func (h *harness) person(email string) sqlcgen.UserForSignInRow {
	h.t.Helper()

	person, err := store.Read(h.pool).UserForSignIn(context.Background(), email)
	if err != nil {
		h.t.Fatalf("no seeded identity for %s: %v", email, err)
	}
	return person
}

// claimsFor returns the claim set a valid token for a seeded person carries,
// so a test can mutate one field and assert the rejection.
func (h *harness) claimsFor(email string) map[string]any {
	h.t.Helper()

	person := h.person(email)
	return map[string]any{
		"jti":        uuid.NewString(),
		"sub":        person.Subject,
		"email":      person.Email,
		"name":       person.DisplayName,
		"org_id":     person.OrgID.String(),
		"account_id": person.AccountID.String(),
		"roles":      []string{"admin"},
		"iss":        testIssuer,
		"aud":        testAudience,
		"exp":        time.Now().Add(time.Hour).Unix(),
	}
}

// as returns a client and the headers that authenticate it as one person.
func (h *harness) as(email string, roles ...auth.Role) (*http.Client, map[string]string) {
	return h.client(), map[string]string{"Authorization": "Bearer " + h.tokenFor(email, roles...)}
}

// do issues a request and decodes the response.
func (h *harness) do(c *http.Client, method, path, body string, headers map[string]string) (int, map[string]any) {
	h.t.Helper()

	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequest(method, h.base+path, reader)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read %s %s: %v", method, path, err)
	}

	// Every response every test makes is checked against the published
	// document. This is what makes api/openapi.yaml a source of truth for
	// shapes rather than a second description of them: a field renamed in a
	// handler and not in the spec now fails whichever test touched it.
	h.validateAgainstSpec(req, resp, payload)

	// Every response is {"data": …} or {"error": …}. Tests assert on whichever
	// is present, so the envelope is unwrapped here rather than in each one.
	var envelope struct {
		Data  map[string]any `json:"data"`
		Error map[string]any `json:"error"`
	}
	_ = json.Unmarshal(payload, &envelope)

	if envelope.Error != nil {
		return resp.StatusCode, envelope.Error
	}
	if envelope.Data != nil {
		return resp.StatusCode, envelope.Data
	}
	return resp.StatusCode, map[string]any{}
}

// validateAgainstSpec checks one response against the OpenAPI document.
//
// The document already decides routing and authorization at startup. Without
// this it decided nothing about shapes -- the generated model types have no
// callers, and handlers answer with their own structs -- so the two could drift
// apart silently and only the frontend would find out. This closes that.
func (h *harness) validateAgainstSpec(req *http.Request, resp *http.Response, body []byte) {
	h.t.Helper()

	route, pathParams, err := h.router.FindRoute(req)
	if err == nil && route.Operation != nil {
		// Every request the suite makes resolves to an operation in the
		// document. Recording which ones is what lets TestMain say whether the
		// suite covered all of them, rather than covering whatever it happened
		// to cover.
		recordExercised(route.Operation.OperationID)
	}
	if err != nil {
		// Echo answers for a path the document does not describe, which is what
		// a deliberate 404 or 405 probe is asking for. Anything else reaching
		// here is a request to an endpoint that should have been in the
		// document, and saying so is the point of this check.
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			return
		}
		h.t.Errorf("%s %s answered %d but is not in the OpenAPI document",
			req.Method, req.URL.Path, resp.StatusCode)
		return
	}

	err = openapi3filter.ValidateResponse(req.Context(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: pathParams,
			Route:      route,
			// The document's security requirements are enforced by the server's
			// own middleware, which is what the guard tests cover. Re-checking
			// them here would only assert that this harness sent a header.
			Options: &openapi3filter.Options{
				AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error {
					return nil
				},
			},
		},
		Status: resp.StatusCode,
		Header: resp.Header,
		Body:   io.NopCloser(bytes.NewReader(body)),
	})
	if err != nil {
		h.t.Errorf("%s %s answered %d in a shape the document does not describe: %v\n%s",
			req.Method, req.URL.Path, resp.StatusCode, err, body)
	}
}

// connectJira points an account at the stub site.
func (h *harness) connectJira(c *http.Client, headers map[string]string) {
	h.t.Helper()
	body := fmt.Sprintf(
		`{"connectorType":"jira","config":{"siteUrl":%q,"email":"svc-jira@acme.test","apiToken":"ATATT-secret-token"}}`,
		h.site)
	status, resp := h.do(c, "POST", "/api/connections", body, headers)
	if status != http.StatusCreated {
		h.t.Fatalf("connect jira: status %d, body %v", status, resp)
	}
}

// seedTenancy creates an organization of this test run's own, with two
// accounts in it and a person in each.
//
// Its own, and not the demo data, because these tests connect an integration
// and file tickets. Run against the seeded account they leave it pointing at a
// stub server that stops existing when the test does -- so `make test`
// followed by opening the interface showed a broken Jira connection, which is
// a poor thing to hand a reviewer.
//
// The organization is deleted afterwards and every table cascades from it, so
// a run leaves the database as it found it.
func (h *harness) seedTenancy() {
	h.t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		h.t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	suffix := uuid.NewString()[:8]
	var orgID uuid.UUID
	if err := conn.QueryRow(ctx,
		`insert into organizations (name, slug) values ($1, $1) returning id`,
		"e2e-"+suffix).Scan(&orgID); err != nil {
		h.t.Fatalf("seed organization: %v", err)
	}
	h.t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		_, _ = c.Exec(context.Background(), `delete from organizations where id = $1`, orgID)
	})

	// Two accounts in one organization: the pair that a single-layer tenancy
	// check would let see each other.
	account := func(label string) uuid.UUID {
		var id uuid.UUID
		if err := conn.QueryRow(ctx,
			`insert into accounts (org_id, name, slug) values ($1, $2, $2) returning id`,
			orgID, label+"-"+suffix).Scan(&id); err != nil {
			h.t.Fatalf("seed account %s: %v", label, err)
		}
		return id
	}
	person := func(accountID uuid.UUID, label string) string {
		email := label + "-" + suffix + "@e2e.test"
		if _, err := conn.Exec(ctx,
			`insert into users (org_id, account_id, subject, email, display_name)
			 values ($1, $2, $3, $4, $5)`,
			orgID, accountID, "e2e|"+label+"-"+suffix, email, label); err != nil {
			h.t.Fatalf("seed person %s: %v", label, err)
		}
		return email
	}

	primary := account("primary")
	sibling := account("sibling")

	h.orgID = orgID
	h.accountIDs = []uuid.UUID{primary, sibling}
	h.admin = person(primary, "admin")
	h.member = person(primary, "member")
	h.sibling = person(sibling, "sibling")
}

// scheduleIDs is every digest schedule this run's tenancy could have created.
//
// The identifier is derived from the scope and the connector type, which is
// what makes creation idempotent -- and what lets this reconstruct the same
// identifier without recording it.
func (h *harness) scheduleIDs() []string {
	if h.orgID == uuid.Nil {
		return nil
	}
	ids := make([]string, 0, len(h.accountIDs))
	for _, accountID := range h.accountIDs {
		ids = append(ids, fmt.Sprintf("blog-digest/%s/%s/jira", h.orgID, accountID))
	}
	return ids
}
