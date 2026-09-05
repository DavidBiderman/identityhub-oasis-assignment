package httpapi_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"

	"github.com/dbiderman/identityhub/backend/api"
	"github.com/dbiderman/identityhub/backend/internal/auth"
)

// The full journey the brief describes: arrive with a token, connect Jira, pick
// a project, file a finding, and see it in the recent tickets list with a link.
func TestUIJourney(t *testing.T) {
	h := newHarness(t)

	admin, hdr := h.as(h.admin, auth.RoleAdmin)
	h.connectJira(admin, hdr)

	status, body := h.do(admin, "GET", "/api/projects", "", hdr)
	if status != http.StatusOK {
		t.Fatalf("list projects: %d %v", status, body)
	}
	if projects, _ := body["projects"].([]any); len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}

	// Issue types are discovered per project, and subtasks excluded because
	// they cannot be created standalone.
	status, body = h.do(admin, "GET", "/api/projects/NHI/issue-types", "", hdr)
	if status != http.StatusOK {
		t.Fatalf("list issue types: %d %v", status, body)
	}
	if kinds, _ := body["issueTypes"].([]any); len(kinds) != 1 {
		t.Fatalf("got %d issue types, want only the creatable one", len(kinds))
	}

	status, body = h.do(admin, "POST", "/api/tickets",
		`{"projectKey":"NHI","title":"Stale Service Account: svc-deploy-prod","description":"Last used 400 days ago."}`, hdr)
	if status != http.StatusCreated {
		t.Fatalf("create ticket: %d %v", status, body)
	}
	issueKey, _ := body["issueKey"].(string)
	issueURL, _ := body["issueUrl"].(string)
	if issueKey == "" {
		t.Fatal("created ticket has no issue key")
	}
	if !strings.HasSuffix(issueURL, "/browse/"+issueKey) {
		t.Errorf("issue URL = %q, want a /browse/ link", issueURL)
	}

	status, body = h.do(admin, "GET", "/api/tickets", "", hdr)
	if status != http.StatusOK {
		t.Fatalf("list tickets: %d %v", status, body)
	}
	tickets, _ := body["tickets"].([]any)
	if len(tickets) == 0 {
		t.Fatal("the created ticket does not appear in the recent tickets list")
	}
	first, _ := tickets[0].(map[string]any)
	if first["issueKey"] != issueKey {
		t.Errorf("most recent ticket is %v, want the one just created", first["issueKey"])
	}
	if first["source"] != "ui" {
		t.Errorf("source = %v, want ui", first["source"])
	}
}

// The account boundary, over HTTP: one account's tickets must be invisible to a
// sibling account in the same organization.
func TestAccountsCannotSeeEachOther(t *testing.T) {
	h := newHarness(t)

	platform, platformHdr := h.as(h.admin, auth.RoleAdmin)
	h.connectJira(platform, platformHdr)

	status, body := h.do(platform, "POST", "/api/tickets",
		`{"projectKey":"NHI","title":"Platform finding","description":"Only the platform account should see this."}`,
		platformHdr)
	if status != http.StatusCreated {
		t.Fatalf("create ticket: %d %v", status, body)
	}
	platformKey, _ := body["issueKey"].(string)

	secops, secopsHdr := h.as(h.sibling, auth.RoleAdmin)

	status, body = h.do(secops, "GET", "/api/tickets", "", secopsHdr)
	if status != http.StatusOK {
		t.Fatalf("list tickets as secops: %d %v", status, body)
	}
	for _, raw := range body["tickets"].([]any) {
		if raw.(map[string]any)["issueKey"] == platformKey {
			t.Fatalf("the secops account can see the platform account's ticket %s", platformKey)
		}
	}

	if status, _ = h.do(secops, "GET", "/api/projects", "", secopsHdr); status != http.StatusConflict {
		t.Errorf("projects for an unconnected account: %d, want 409", status)
	}
}

// The central guarantee of the token model: tenancy comes from the token, so
// forging a different account means forging a token.
func TestTenancyComesFromTheToken(t *testing.T) {
	h := newHarness(t)

	// A caller cannot name an account in the request. Nothing rejects the
	// attempt, because there is nothing to reject: the request type has no
	// such field, so the value is read by nobody and the write lands in the
	// account the token names. That is the property worth testing -- a 400
	// would only prove that something checked.
	admin, hdr := h.as(h.admin, auth.RoleAdmin)
	status, body := h.do(admin, "POST", "/api/api-keys",
		`{"name":"tenancy probe","accountId":"00000000-0000-0000-0000-000000000001"}`, hdr)
	if status != http.StatusCreated {
		t.Fatalf("a body naming an account: %d %v, want 201", status, body)
	}
	probe := body["apiKey"].(map[string]any)["keyId"].(string)

	sibling, siblingHdr := h.as(h.sibling, auth.RoleAdmin)
	status, body = h.do(sibling, "GET", "/api/api-keys", "", siblingHdr)
	if status != http.StatusOK {
		t.Fatalf("list api keys as the sibling account: %d %v", status, body)
	}
	for _, raw := range body["apiKeys"].([]any) {
		if raw.(map[string]any)["keyId"] == probe {
			t.Fatalf("the key was created in the account the body named, not the token's")
		}
	}

	// A token for one account reports that account, whatever else is sent.
	status, body = h.do(admin, "GET", "/api/auth/me", "", hdr)
	if status != http.StatusOK {
		t.Fatalf("me: %d %v", status, body)
	}
	fromToken := body["accountId"]

	status, body = h.do(admin, "GET", "/api/auth/me", "",
		merge(hdr, map[string]string{"X-Account-Id": "00000000-0000-0000-0000-000000000001"}))
	if status != http.StatusOK || body["accountId"] != fromToken {
		t.Errorf("a header changed the reported account: %v", body["accountId"])
	}
}

// Tokens are rejected on the checks that survive signature verification: a
// valid signature on a token for another application is still one to refuse.
func TestTokensAreCheckedBeyondTheirSignature(t *testing.T) {
	h := newHarness(t)

	base := h.claimsFor(h.admin)

	cases := map[string]func(map[string]any){
		"expired":            func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"another issuer":     func(c map[string]any) { c["iss"] = "https://someone-else.example" },
		"another audience":   func(c map[string]any) { c["aud"] = "a-different-application" },
		"no account claim":   func(c map[string]any) { delete(c, "account_id") },
		"no org claim":       func(c map[string]any) { delete(c, "org_id") },
		"account not a uuid": func(c map[string]any) { c["account_id"] = "not-a-uuid" },
		"no subject":         func(c map[string]any) { delete(c, "sub") },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			claims := map[string]any{}
			for k, v := range base {
				claims[k] = v
			}
			mutate(claims)

			status, _ := h.do(h.client(), "GET", "/api/auth/me", "",
				map[string]string{"Authorization": "Bearer " + unsignedToken(t, claims)})
			if status != http.StatusUnauthorized {
				t.Fatalf("token with %s was accepted: %d", name, status)
			}
		})
	}

	// A request with no token at all.
	if status, _ := h.do(h.client(), "GET", "/api/auth/me", "", nil); status != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", status)
	}
	// Something that is not a token.
	if status, _ := h.do(h.client(), "GET", "/api/auth/me", "",
		map[string]string{"Authorization": "Bearer nonsense"}); status != http.StatusUnauthorized {
		t.Errorf("malformed token: %d, want 401", status)
	}
}

// Roles come from the token, so an administrator-only route follows the claim
// rather than anything this application stores.
func TestRolesComeFromTheToken(t *testing.T) {
	h := newHarness(t)

	// The same person, without the admin role in their token.
	member, memberHdr := h.as(h.admin, auth.RoleMember)
	if status, _ := h.do(member, "POST", "/api/api-keys", `{"name":"nope"}`, memberHdr); status != http.StatusForbidden {
		t.Errorf("member creating an API key: %d, want 403", status)
	}

	// The same person, with it.
	admin, adminHdr := h.as(h.admin, auth.RoleAdmin)
	if status, _ := h.do(admin, "POST", "/api/api-keys", `{"name":"yes"}`, adminHdr); status != http.StatusCreated {
		t.Errorf("admin creating an API key: %d, want 201", status)
	}
}

// The public API: an external system authenticates with a key, and a retry with
// the same Idempotency-Key must not file a second ticket.
func TestPublicAPIAndIdempotency(t *testing.T) {
	h := newHarness(t)

	admin, hdr := h.as(h.admin, auth.RoleAdmin)
	h.connectJira(admin, hdr)

	status, body := h.do(admin, "POST", "/api/api-keys", `{"name":"ci-pipeline"}`, hdr)
	if status != http.StatusCreated {
		t.Fatalf("create api key: %d %v", status, body)
	}
	secret, _ := body["secret"].(string)
	if !strings.HasPrefix(secret, "ih_") {
		t.Fatalf("api key %q does not have the expected prefix", secret)
	}

	machine := h.client()

	if status, _ = h.do(h.client(), "POST", "/api/v1/findings",
		`{"projectKey":"NHI","title":"t","description":"d"}`, nil); status != http.StatusUnauthorized {
		t.Errorf("unauthenticated public API call: %d, want 401", status)
	}
	if status, _ = h.do(h.client(), "POST", "/api/v1/findings",
		`{"projectKey":"NHI","title":"t","description":"d"}`,
		map[string]string{"Authorization": "Bearer ih_bogus_key"}); status != http.StatusUnauthorized {
		t.Errorf("bad api key: %d, want 401", status)
	}

	before := h.issuesCreated()
	payload := `{"projectKey":"NHI","title":"Overprivileged key: ci-deploy","description":"Found by scanner.","labels":["scanner"]}`
	idempotent := map[string]string{
		"Authorization":   "Bearer " + secret,
		"Idempotency-Key": "scan-run-" + h.prefix,
	}

	status, body = h.do(machine, "POST", "/api/v1/findings", payload, idempotent)
	if status != http.StatusCreated {
		t.Fatalf("first call: %d %v", status, body)
	}
	firstKey, _ := body["issueKey"].(string)
	if body["created"] != true {
		t.Errorf("created = %v on the call that filed the ticket, want true", body["created"])
	}

	status, body = h.do(machine, "POST", "/api/v1/findings", payload, idempotent)
	if status != http.StatusOK {
		t.Fatalf("replay: %d %v, want 200 with the original ticket", status, body)
	}
	if body["issueKey"] != firstKey {
		t.Errorf("replay returned %v, want the original %s", body["issueKey"], firstKey)
	}
	// A replay is a success, and the body says which kind. A client that reads
	// the body rather than the status must still be able to tell that nothing
	// was filed twice.
	if body["created"] != false {
		t.Errorf("created = %v on a replay, want false", body["created"])
	}
	if got := h.issuesCreated() - before; got != 1 {
		t.Errorf("the provider was asked to create %d issues, want exactly 1", got)
	}

	status, body = h.do(machine, "POST", "/api/v1/findings",
		`{"projectKey":"NHI","title":"A different finding","description":"Different."}`, idempotent)
	if status != http.StatusConflict {
		t.Errorf("mismatched replay: %d %v, want 409", status, body)
	}
}

// A stored credential must never come back out of the API.
func TestCredentialNeverLeaves(t *testing.T) {
	h := newHarness(t)

	admin, hdr := h.as(h.admin, auth.RoleAdmin)
	h.connectJira(admin, hdr)

	for _, path := range []string{"/api/connections", "/api/auth/me", "/api/audit"} {
		status, body := h.do(admin, "GET", path, "", hdr)
		if status != http.StatusOK {
			t.Fatalf("GET %s: %d", path, status)
		}
		if strings.Contains(fmt.Sprint(body), "ATATT-secret-token") {
			t.Errorf("%s leaked the API token", path)
		}
	}
}

// unsignedToken builds a token from arbitrary claims, to exercise rejection.
func unsignedToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("encode claims: %v", err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

func merge(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// Every operation the document protects must actually be protected.
//
// This walks api/openapi.yaml rather than listing routes, so an endpoint added
// to the spec is covered the moment it exists. The failure it is looking for is
// the one a hand-written router makes eventually: a route registered outside
// the authenticated group, which then serves anyone.
func TestTheDocumentsSecuritySectionIsEnforced(t *testing.T) {
	h := newHarness(t)

	document, err := openapi3.NewLoader().LoadFromData(api.Spec)
	if err != nil {
		t.Fatalf("read the OpenAPI document: %v", err)
	}

	anonymous := h.client()
	for path, item := range document.Paths.Map() {
		for method, op := range item.Operations() {
			requirements := document.Security
			if op.Security != nil {
				requirements = *op.Security
			}
			if len(requirements) == 0 {
				continue // deliberately public
			}

			t.Run(op.OperationID, func(t *testing.T) {
				// Path parameters are irrelevant: authentication runs before a
				// handler ever sees them, so any syntactically valid value does.
				concrete := strings.NewReplacer(
					"{id}", uuid.NewString(), "{key}", "NHI", "{type}", "jira",
				).Replace(path)

				status, body := h.do(anonymous, method, concrete, "{}", nil)
				if status != http.StatusUnauthorized {
					t.Errorf("%s %s without a credential: %d %v, want 401",
						method, concrete, status, body)
				}
			})
		}
	}
}

// The public set is exactly these three, and adding to it must be deliberate.
//
// TestTheDocumentsSecuritySectionIsEnforced proves that secured operations are
// secured. It cannot prove the *unsecured* list is right, because it skips
// anything with `security: []` as deliberately public — and that is exactly how
// a guard goes missing: the route stops being protected and every test still
// passes. This asserts the other half.
//
// It is a list rather than a rule because there is no rule. Whether an endpoint
// may be anonymous is a judgement about what it returns, and the judgement
// belongs in a review, which is what having to edit this list forces.
// Signing out has to stop the token, not just forget it.
//
// The case that matters is a copy taken before the click: it never touched the
// browser's storage, so clearing sessionStorage does nothing to it. This
// presents the very same token afterwards, which is what a stolen one would.
func TestASignedOutTokenStopsBeingAccepted(t *testing.T) {
	h := newHarness(t)
	c, headers := h.as(h.admin, auth.RoleAdmin)

	if status, _ := h.do(c, http.MethodGet, "/api/auth/me", "", headers); status != http.StatusOK {
		t.Fatalf("before signing out: got %d, want 200", status)
	}

	if status, _ := h.do(c, http.MethodPost, "/api/auth/signout", "", headers); status != http.StatusNoContent {
		t.Fatalf("sign out: got %d, want 204", status)
	}

	status, body := h.do(c, http.MethodGet, "/api/auth/me", "", headers)
	if status != http.StatusUnauthorized {
		t.Fatalf("a signed-out token was still accepted: got %d, want 401", status)
	}
	// do unwraps the envelope, so body is the error object itself.
	if body["code"] != "unauthorized" {
		t.Errorf("error code = %v, want unauthorized", body["code"])
	}

	// The token is refused before any handler runs, so a second sign-out is a
	// 401 rather than a repeat of the first.
	if status, _ := h.do(c, http.MethodPost, "/api/auth/signout", "", headers); status != http.StatusUnauthorized {
		t.Errorf("signing out twice: got %d, want 401", status)
	}

	// A different token for the same person is unaffected. Signing out ends one
	// credential, not every session the person has.
	other, otherHeaders := h.as(h.admin, auth.RoleAdmin)
	if status, _ := h.do(other, http.MethodGet, "/api/auth/me", "", otherHeaders); status != http.StatusOK {
		t.Errorf("another token for the same person: got %d, want 200", status)
	}
}

func TestOnlyTheseOperationsArePublic(t *testing.T) {
	h := newHarness(t)

	public := map[string]string{
		"health": "an orchestrator must be able to ask without credentials",
		"ready":  "the same",
		"signIn": "it is how a caller obtains a credential; there is nothing to authenticate it with yet",
	}

	document, err := openapi3.NewLoader().LoadFromData(api.Spec)
	if err != nil {
		t.Fatalf("read the OpenAPI document: %v", err)
	}

	for _, item := range document.Paths.Map() {
		for _, op := range item.Operations() {
			requirements := document.Security
			if op.Security != nil {
				requirements = *op.Security
			}
			if len(requirements) > 0 {
				continue
			}
			if _, expected := public[op.OperationID]; !expected {
				t.Errorf("%q is public and not on the list. If that is intended, "+
					"add it here with the reason; if not, give it a security section.",
					op.OperationID)
			}
		}
	}

	// The one public endpoint that does something: it has to reject a bad
	// credential rather than being open in the sense of unguarded.
	status, body := h.do(h.client(), http.MethodPost, "/api/auth/signin",
		`{"email":"nobody@example.test","password":"not-a-password"}`, nil)
	if status != http.StatusUnauthorized {
		t.Errorf("sign in with a bad credential: %d %v, want 401", status, body)
	}
}

// The two endpoints an orchestrator depends on, which nothing else exercises.
//
// They are unauthenticated by necessity -- a health check cannot hold a
// credential -- so the only thing standing between them and a mistake is a test
// that actually calls them. Readiness in particular has real logic now: three
// dependencies, each probed on its own deadline so that one hanging check does
// not report its neighbours as unavailable too.
func TestLivenessAndReadinessAnswerWithoutCredentials(t *testing.T) {
	h := newHarness(t)

	if status, body := h.do(h.client(), http.MethodGet, "/healthz", "", nil); status != http.StatusOK {
		t.Errorf("healthz: %d %v, want 200 -- a liveness probe must not need a credential", status, body)
	}

	// Ready answers 200 when its dependencies are reachable and 503 when they
	// are not. Both are correct; what would be wrong is refusing to answer.
	status, _ := h.do(h.client(), http.MethodGet, "/readyz", "", nil)
	if status != http.StatusOK && status != http.StatusServiceUnavailable {
		t.Errorf("readyz: %d, want 200 or 503", status)
	}

	// The suite runs against a working stack, so anything but ready means a
	// dependency this test can name is down.
	if status != http.StatusOK {
		t.Errorf("readyz reported not ready against a running stack")
	}
}

// A route that accepts two credentials has to accept either one.
//
// OpenAPI reads a list of security requirements as OR. An earlier version of
// guards() appended every requirement's middleware into a single chain, which
// made it AND -- and made this route unreachable by anyone, because one
// Authorization header cannot satisfy two authenticators. A person's token
// cleared the access-token middleware and was then refused by the API key one.
//
// getTicket is the only operation with two, and it has two because the 201 from
// POST /api/v1/findings returns a Location pointing here: refusing an API key
// would hand a caller a link it cannot follow.
func TestATicketCanBeReadWithEitherCredential(t *testing.T) {
	h := newHarness(t)

	admin, hdr := h.as(h.admin, auth.RoleAdmin)
	h.connectJira(admin, hdr)

	status, body := h.do(admin, "POST", "/api/api-keys", `{"name":"reader"}`, hdr)
	if status != http.StatusCreated {
		t.Fatalf("create api key: %d %v", status, body)
	}
	secret, _ := body["secret"].(string)
	keyHdr := map[string]string{"Authorization": "Bearer " + secret}

	// File one so there is something to read back.
	status, body = h.do(admin, "POST", "/api/tickets",
		`{"projectKey":"NHI","title":"Readable by either","description":"d"}`, hdr)
	if status != http.StatusCreated {
		t.Fatalf("file a finding: %d %v", status, body)
	}
	id, _ := body["id"].(string)
	path := "/api/tickets/" + id

	// Both credentials reach it, and see the same ticket.
	for name, headers := range map[string]map[string]string{
		"a person's access token": hdr,
		"an API key":              keyHdr,
	} {
		status, body := h.do(h.client(), http.MethodGet, path, "", headers)
		if status != http.StatusOK {
			t.Errorf("%s: %d %v, want 200", name, status, body)
			continue
		}
		if body["id"] != id {
			t.Errorf("%s: got ticket %v, want %s", name, body["id"], id)
		}
	}

	// And neither of the two schemes is a way in without a credential.
	for name, headers := range map[string]map[string]string{
		"no credential": nil,
		"a bad API key": {"Authorization": "Bearer ih_bogus_key"},
		"a bad token":   {"Authorization": "Bearer not-a-token"},
	} {
		if status, _ := h.do(h.client(), http.MethodGet, path, "", headers); status != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, status)
		}
	}
}
