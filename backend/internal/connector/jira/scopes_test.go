package jira_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/connector/jira"
)

// An Atlassian API token created with only read scopes connects, lists
// projects, and then fails on the first ticket somebody files. Finding that out
// at connect time is the whole point of these checks.
//
// There is no introspection endpoint for an API token, so each capability is
// checked by attempting the smallest request that needs it. The stub below
// answers 401 for the calls a scoped token would not be granted.

func TestConnectingNamesTheScopesATokenIsMissing(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		refuse []string // paths the token is not scoped for
		want   []string // scopes the error must name
	}{
		"a read-only token": {
			refuse: []string{"POST /rest/api/3/issue"},
			want:   []string{"write:jira-work"},
		},
		"a token that cannot see projects": {
			refuse: []string{"GET /rest/api/3/project/search"},
			want:   []string{"read:jira-work"},
		},
		"a token granted nothing useful": {
			refuse: []string{"GET /rest/api/3/project/search", "POST /rest/api/3/issue"},
			want:   []string{"read:jira-work", "write:jira-work"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, cfg := scopeStub(t, tc.refuse)

			_, err := c.Resolve(context.Background(), cfg)
			if !errors.Is(err, connector.ErrConfigInvalid) {
				t.Fatalf("got %v, want ErrConfigInvalid", err)
			}

			var ce *connector.Error
			if !errors.As(err, &ce) {
				t.Fatalf("error was not classified: %v", err)
			}
			// Named against the field that has to change. Scopes are fixed when
			// a token is created, so the remedy is a new token.
			if _, ok := ce.Fields["apiToken"]; !ok {
				t.Errorf("error names fields %v, want apiToken among them", ce.Fields)
			}
			for _, scope := range tc.want {
				if !strings.Contains(ce.Message, scope) {
					t.Errorf("message does not name %s: %s", scope, ce.Message)
				}
			}
		})
	}
}

// The write check must not create anything. It sends a request Jira cannot
// accept, and reads which layer refused it.
func TestTheWriteCheckCannotFileATicket(t *testing.T) {
	t.Parallel()

	var created int
	c, cfg := scopeStubFunc(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue" {
			body := make([]byte, 64)
			n, _ := r.Body.Read(body)
			if strings.Contains(string(body[:n]), `"fields":{}`) {
				// What Jira answers: it reached validation, so the token was
				// allowed to ask, and nothing was created.
				writeJSON(w, http.StatusBadRequest,
					`{"errors":{"project":"project is required"}}`)
				return true
			}
			created++
		}
		return false
	})

	if _, err := c.Resolve(context.Background(), cfg); err != nil {
		t.Fatalf("resolve with every scope granted: %v", err)
	}
	if created != 0 {
		t.Errorf("the scope check created %d issues; it must create none", created)
	}
}

// A 403 is Jira refusing on permissions, not the gateway refusing on scope. It
// must not be reported as a missing scope, because creating a new token would
// not fix it.
func TestAPermissionsRefusalIsNotReportedAsAMissingScope(t *testing.T) {
	t.Parallel()

	c, cfg := scopeStubFunc(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue" {
			writeJSON(w, http.StatusForbidden, `{"errorMessages":["No CREATE_ISSUES permission"]}`)
			return true
		}
		return false
	})

	// Resolve gets past the scope check; the refusal surfaces from the
	// operation itself, classified as a permissions problem.
	if _, err := c.Resolve(context.Background(), cfg); err != nil {
		var ce *connector.Error
		if errors.As(err, &ce) && strings.Contains(ce.Message, "scope") {
			t.Errorf("a permissions refusal was reported as a scope problem: %s", ce.Message)
		}
	}
}

// scopeStub serves a Jira that grants everything except the named routes, which
// it refuses the way the gateway refuses an out-of-scope call.
func scopeStub(t *testing.T, refuse []string) (*jira.Connector, *jira.Config) {
	t.Helper()

	return scopeStubFunc(t, func(w http.ResponseWriter, r *http.Request) bool {
		for _, route := range refuse {
			if route == r.Method+" "+r.URL.Path {
				writeJSON(w, http.StatusUnauthorized, `{"message":"scope required"}`)
				return true
			}
		}
		return false
	})
}

// scopeStubFunc serves a working Jira, giving handled first refusal on every
// request so a test can answer one route differently.
func scopeStubFunc(t *testing.T, handled func(http.ResponseWriter, *http.Request) bool) (*jira.Connector, *jira.Config) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handled(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/rest/api/3/myself":
			writeJSON(w, http.StatusOK,
				`{"accountId":"5b10a","emailAddress":"svc@example.com","displayName":"Service"}`)
		case r.URL.Path == "/rest/api/3/project/search":
			writeJSON(w, http.StatusOK, `{"values":[{"id":"1","key":"NHI","name":"NHI"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue":
			writeJSON(w, http.StatusBadRequest, `{"errors":{"project":"project is required"}}`)
		default:
			writeJSON(w, http.StatusOK, `{}`)
		}
	}))
	t.Cleanup(srv.Close)

	return jira.New(srv.Client()), &jira.Config{
		SiteURL: srv.URL, Email: "svc@example.com", APIToken: "tok",
	}
}
