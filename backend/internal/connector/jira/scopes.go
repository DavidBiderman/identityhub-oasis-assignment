package jira

import (
	"context"
	"net/http"
	"strings"

	"github.com/dbiderman/identityhub/backend/internal/connector"
)

// What a token has to be allowed to do before a connection is worth storing.
//
// An Atlassian API token comes in two shapes. A classic token carries whatever
// its owner can do, so these all pass. A scoped token carries exactly the
// scopes chosen when it was created, and one created with only read:jira-work
// will connect, list projects, and then fail on the first ticket somebody
// tries to file -- which is the worst moment to discover it.
//
// There is no introspection endpoint for an API token: Atlassian will not tell
// us what a token may do, only answer or refuse when we try. So each capability
// is checked by attempting the smallest request that needs it and reading the
// status.
const (
	scopeReadUser  = "read:jira-user"
	scopeReadWork  = "read:jira-work"
	scopeWriteWork = "write:jira-work"
)

// capability is one thing this product needs a token to be allowed to do.
type capability struct {
	scope string
	// what describes the capability in a sentence, for the error.
	what string
	// probe issues the smallest request that requires the scope and returns
	// the status. A transport failure is reported as an error and stops the
	// check; a status is a decision.
	probe func(ctx context.Context, c *Connector, cfg *Config) (int, error)
}

// requiredCapabilities is the whole of what this connector needs. Each entry is
// one scope, checked once, at connect time.
var requiredCapabilities = []capability{
	{
		scope: scopeReadUser,
		what:  "read the account the token belongs to",
		probe: func(ctx context.Context, c *Connector, cfg *Config) (int, error) {
			return c.status(ctx, cfg, http.MethodGet, "/rest/api/3/myself", "")
		},
	},
	{
		scope: scopeReadWork,
		what:  "list the projects findings can be filed into",
		probe: func(ctx context.Context, c *Connector, cfg *Config) (int, error) {
			return c.status(ctx, cfg, http.MethodGet, "/rest/api/3/project/search?maxResults=1", "")
		},
	},
	{
		scope: scopeWriteWork,
		what:  "file a ticket",
		// A create request with an empty field set. Jira requires a project and
		// an issue type, so this can never create anything -- which is the
		// point: a write scope cannot be tested by reading, and testing it by
		// writing would leave a ticket behind on every connect.
		//
		// What separates the two answers is which layer refuses. A token
		// without the scope is stopped before Jira sees the body, and returns
		// 401 or 403. A token with it reaches validation and returns 400,
		// complaining about the fields that are missing. Anything else is
		// inconclusive and is not treated as a refusal.
		probe: func(ctx context.Context, c *Connector, cfg *Config) (int, error) {
			return c.status(ctx, cfg, http.MethodPost, "/rest/api/3/issue", `{"fields":{}}`)
		},
	},
}

// verifyScopes reports whether the token may do everything this product needs.
//
// It runs at connect time only. Every later call is made with a token that
// passed this, so a 403 afterwards is a Jira permission that changed rather
// than a scope that was never there.
func (c *Connector) verifyScopes(ctx context.Context, cfg *Config) error {
	var missing []capability

	for _, want := range requiredCapabilities {
		status, err := want.probe(ctx, c, cfg)
		if err != nil {
			return connector.Errorf(connector.ErrUnavailable,
				"Could not reach Jira to check what the API token is allowed to do. Try again shortly.")
		}
		// 401 only. By this point discovery has established that the address
		// accepts the credential, so a refusal here is the gateway declining a
		// call the token was not granted.
		//
		// A 403 is a different answer: the credential authenticated and Jira
		// then refused on permissions, which is about what the account may do
		// rather than what the token may ask. That is left to the caller, which
		// reports it as a permissions problem and names the operation.
		if status == http.StatusUnauthorized {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	// Named against apiToken, because that is the field to change: the scopes
	// are chosen when the token is created and cannot be added to an existing
	// one.
	return connector.InvalidConfig("apiToken", missingScopeMessage(missing))
}

// missingScopeMessage says which scopes are absent and what each one was for.
func missingScopeMessage(missing []capability) string {
	reasons := make([]string, len(missing))
	for i, m := range missing {
		reasons[i] = m.scope + ", to " + m.what
	}
	return "This API token is missing scopes it needs: " + strings.Join(reasons, "; ") +
		". Scopes are chosen when a token is created and cannot be added to an existing " +
		"one, so create a new token granting read:jira-user, read:jira-work and " +
		"write:jira-work."
}

// status issues an authenticated request against the resolved address and
// reports the status only. The body is discarded: what it says is Jira's
// business, and the status is the whole signal here.
func (c *Connector) status(ctx context.Context, cfg *Config, method, path string, body string) (int, error) {
	base, err := cfg.baseURL()
	if err != nil {
		return 0, err
	}
	return c.statusAt(ctx, base, cfg, method, path, body)
}
