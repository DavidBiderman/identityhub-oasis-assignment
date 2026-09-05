package httpapi_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/dbiderman/identityhub/backend/api"
)

// The authorization policy, pinned.
//
// api/openapi.yaml decides which credential every endpoint requires: Routes
// reads each operation's security section at startup and builds its middleware
// from it. That makes the document the policy, and a policy in a YAML file is
// one an editor can change without touching a line of Go.
//
// The quiet failure is the one worth guarding. Deleting an operation's security
// section does not make it public -- it makes it inherit the document default,
// which is a plain access token. An administrator-only endpoint silently becomes
// available to every signed-in member, the build passes, and the only test that
// would notice is one that happens to assert that endpoint's authorization.
//
// So the complete map is written down here. Change the document and this fails
// until the expectation is changed too, which is the point: a privilege change
// should be a decision somebody made, not a diff nobody read.
var policy = map[string]string{
	"health": "public", // an orchestrator must be able to ask without credentials
	"ready":  "public", // the same
	"signIn": "public", // it is how a caller obtains a credential

	// A person's token, and nothing more.
	"whoAmI":          "accessToken",
	"signOut":         "accessToken",
	"listConnections": "accessToken",
	"listTargets":     "accessToken",
	"listKinds":       "accessToken",
	"createTicket":    "accessToken",
	"listTickets":     "accessToken",
	"listAuditEvents": "accessToken",
	"getDigest":       "accessToken",
	"runDigest":       "accessToken",

	// Administrator only. These change what an account is connected to, or mint
	// and revoke the credentials other systems authenticate with.
	"connect":      "accessToken:admin",
	"disconnect":   "accessToken:admin",
	"listAPIKeys":  "accessToken:admin",
	"createAPIKey": "accessToken:admin",
	"revokeAPIKey": "accessToken:admin",

	// The public API, for scanners and CI pipelines.
	"createFinding": "apiKey:findings:write",

	// Either credential, because either can have filed the ticket: the 201 on
	// createFinding returns a Location pointing here, and that header is
	// answered by an API key.
	"getTicket": "accessToken or apiKey",
}

func TestEveryOperationRequiresWhatThePolicySays(t *testing.T) {
	t.Parallel()

	document, err := openapi3.NewLoader().LoadFromData(api.Spec)
	if err != nil {
		t.Fatalf("read the OpenAPI document: %v", err)
	}

	found := map[string]string{}
	for _, item := range document.Paths.Map() {
		for _, op := range item.Operations() {
			found[op.OperationID] = requirementOf(document, op)
		}
	}

	for id, actual := range found {
		switch expected, known := policy[id]; {
		case !known:
			t.Errorf("operation %q is not in the policy map. Add it with what it "+
				"requires (%q) so that changing it later is a deliberate edit.", id, actual)
		case expected != actual:
			t.Errorf("operation %q requires %q, the policy says %q.\n"+
				"If the change is intended, update the map. If it is not, this is a "+
				"privilege change nobody asked for.", id, actual, expected)
		}
	}

	for id := range policy {
		if _, exists := found[id]; !exists {
			t.Errorf("the policy names %q, which the document no longer has", id)
		}
	}
}

// requirementOf renders an operation's security section as one comparable
// string. An operation with no section of its own inherits the document's,
// which is exactly the inheritance this test exists to make visible.
func requirementOf(document *openapi3.T, op *openapi3.Operation) string {
	requirements := document.Security
	if op.Security != nil {
		requirements = *op.Security
	}

	parts := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		for scheme, scopes := range requirement {
			if len(scopes) == 0 {
				parts = append(parts, scheme)
				continue
			}
			parts = append(parts, scheme+":"+strings.Join(scopes, ","))
		}
	}
	if len(parts) == 0 {
		return "public"
	}
	sort.Strings(parts)
	return strings.Join(parts, " or ")
}
