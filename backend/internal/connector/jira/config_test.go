package jira_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/connector/jira"
)

// --- Validation ---------------------------------------------------------

func valid() jira.Config {
	return jira.Config{
		SiteURL:  "https://acme.atlassian.net",
		Email:    "svc@example.com",
		APIToken: "ATATT-token",
	}
}

// Every rejection names the field it is about, so a caller can attach it to the
// input someone typed rather than showing one message for a whole form.
func TestValidateNamesTheOffendingField(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		mutate func(*jira.Config)
		field  string
	}{
		"no site":            {func(c *jira.Config) { c.SiteURL = "" }, "siteUrl"},
		"plaintext site":     {func(c *jira.Config) { c.SiteURL = "http://acme.atlassian.net" }, "siteUrl"},
		"site with path":     {func(c *jira.Config) { c.SiteURL = "https://acme.atlassian.net/jira" }, "siteUrl"},
		"site with query":    {func(c *jira.Config) { c.SiteURL = "https://acme.atlassian.net?a=b" }, "siteUrl"},
		"site with password": {func(c *jira.Config) { c.SiteURL = "https://u:p@acme.atlassian.net" }, "siteUrl"},
		"no host":            {func(c *jira.Config) { c.SiteURL = "https://" }, "siteUrl"},
		"no email":           {func(c *jira.Config) { c.Email = "" }, "email"},
		"malformed email":    {func(c *jira.Config) { c.Email = "nope" }, "email"},
		"no token":           {func(c *jira.Config) { c.APIToken = "  " }, "apiToken"},
		"plaintext api":      {func(c *jira.Config) { c.APIBaseURL = "http://api.atlassian.com/ex/jira/x" }, "apiBaseUrl"},
		"malformed api":      {func(c *jira.Config) { c.APIBaseURL = "::not a url::" }, "apiBaseUrl"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := valid()
			tc.mutate(&cfg)

			err := cfg.Validate()
			if !errors.Is(err, connector.ErrConfigInvalid) {
				t.Fatalf("got %v, want ErrConfigInvalid", err)
			}

			var ce *connector.Error
			if !errors.As(err, &ce) {
				t.Fatalf("error was not classified: %v", err)
			}
			if _, named := ce.Fields[tc.field]; !named {
				t.Fatalf("error names %v, want %q among them", ce.Fields, tc.field)
			}
		})
	}
}

// Email validation follows RFC 5322 because it uses net/mail, not a substring
// check that would both over- and under-accept.
func TestEmailValidationFollowsTheStandard(t *testing.T) {
	t.Parallel()

	accepted := []string{
		"a@b.co", "first.last@example.co.uk", "user+tag@example.com",
		"Display Name <user@example.com>", `"quoted local"@example.com`,
	}
	rejected := []string{"nope", "@example.com", "user@", "user example.com", "user@@example.com"}

	for _, email := range accepted {
		cfg := valid()
		cfg.Email = email
		if err := cfg.Validate(); err != nil {
			t.Errorf("valid address %q rejected: %v", email, err)
		}
	}
	for _, email := range rejected {
		cfg := valid()
		cfg.Email = email
		if err := cfg.Validate(); err == nil {
			t.Errorf("invalid address %q accepted", email)
		}
	}
}

// The API address is absent on the way in and present on the way out, so
// validation has to accept both.
func TestAPIBaseURLIsOptionalOnTheWayIn(t *testing.T) {
	t.Parallel()

	cfg := valid()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a configuration that has not been resolved was rejected: %v", err)
	}

	cfg.APIBaseURL = "https://api.atlassian.com/ex/jira/76b51339"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a resolved configuration was rejected: %v", err)
	}
}

// --- Resolution ---------------------------------------------------------

// A token the site accepts is used against the site.
func TestResolveUsesTheSiteWhenItAcceptsTheToken(t *testing.T) {
	t.Parallel()

	var gatewayCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/ex/jira/") {
			atomic.AddInt32(&gatewayCalls, 1)
		}
		writeJSON(w, http.StatusOK, `{"accountId":"5b10a","displayName":"Service"}`)
	}))
	t.Cleanup(srv.Close)

	c := jira.New(srv.Client(), jira.WithGatewayHost(srv.URL))
	cfg := &jira.Config{SiteURL: srv.URL, Email: "svc@example.com", APIToken: "unscoped"}

	if _, err := c.Resolve(context.Background(), cfg); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.APIBaseURL != srv.URL {
		t.Errorf("resolved to %q, want the site", cfg.APIBaseURL)
	}
	if gatewayCalls != 0 {
		t.Errorf("a token the site accepted was still sent to the gateway %d times", gatewayCalls)
	}
}

// A token the site rejects with 401 is retried through the gateway, whose cloud
// ID comes from the site's public tenant info endpoint.
func TestResolveFallsBackToTheGatewayForScopedTokens(t *testing.T) {
	t.Parallel()

	const cloudID = "76b51339-0a41-4c9c-a41e-52a7f4d32948"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_edge/tenant_info":
			// Public: it must not be asked for a credential.
			if r.Header.Get("Authorization") != "" {
				t.Error("tenant info was called with a credential attached")
			}
			writeJSON(w, http.StatusOK, `{"cloudId":"`+cloudID+`"}`)

		case strings.HasPrefix(r.URL.Path, "/ex/jira/"):
			if !strings.HasPrefix(r.URL.Path, "/ex/jira/"+cloudID+"/") {
				t.Errorf("gateway path used the wrong cloud ID: %s", r.URL.Path)
			}
			writeJSON(w, http.StatusOK, `{"accountId":"5b10a","displayName":"Service"}`)

		default:
			writeJSON(w, http.StatusUnauthorized, `{"errorMessages":["Client must be authenticated"]}`)
		}
	}))
	t.Cleanup(srv.Close)

	c := jira.New(srv.Client(), jira.WithGatewayHost(srv.URL))
	cfg := &jira.Config{SiteURL: srv.URL, Email: "svc@example.com", APIToken: "scoped"}

	if _, err := c.Resolve(context.Background(), cfg); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := srv.URL + "/ex/jira/" + cloudID
	if cfg.APIBaseURL != want {
		t.Errorf("resolved to %q, want %q", cfg.APIBaseURL, want)
	}
}

// A supplied apiBaseUrl must be ignored, not honoured.
//
// It is a field on Config, so it decodes from a connect body like any other.
// Honouring it would mean an account administrator could name any https host
// and have the customer's Jira credential authenticated against it on the
// connect request itself -- and, because Metadata() carries only siteUrl, have
// the connection listing and the audit trail name Atlassian while every ticket
// went somewhere else.
func TestASuppliedAPIAddressIsIgnored(t *testing.T) {
	t.Parallel()

	var collector int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&collector, 1)
		// Answers plausibly enough to pass discovery and the scope probes.
		if r.Method == http.MethodPost {
			writeJSON(w, http.StatusBadRequest, `{"errors":{"project":"required"}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"accountId":"evil","displayName":"Collector"}`)
	}))
	t.Cleanup(attacker.Close)

	var site int32
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&site, 1)
		if r.Method == http.MethodPost {
			writeJSON(w, http.StatusBadRequest, `{"errors":{"project":"required"}}`)
			return
		}
		writeJSON(w, http.StatusOK,
			`{"accountId":"5b10a","emailAddress":"svc@example.com","displayName":"Service"}`)
	}))
	t.Cleanup(real.Close)

	c := jira.New(real.Client())
	cfg := &jira.Config{
		SiteURL: real.URL, Email: "svc@example.com", APIToken: "t",
		APIBaseURL: attacker.URL,
	}

	account, err := c.Resolve(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	switch {
	case atomic.LoadInt32(&collector) != 0:
		t.Errorf("the credential was sent to the supplied address %d times", collector)
	case cfg.APIBaseURL != real.URL:
		t.Errorf("stored address is %q, want the site %q", cfg.APIBaseURL, real.URL)
	case account.AccountID != "5b10a":
		t.Errorf("resolved against the wrong host: %+v", account)
	case atomic.LoadInt32(&site) == 0:
		t.Error("the real site was never contacted")
	}
}

// A stored row naming somewhere else is refused on the way out too, so tampering
// with the database is not a way around the check above.
func TestAStoredAddressMustBeTheSiteOrTheGateway(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		base     string
		accepted bool
	}{
		"the site itself":     {"https://acme.atlassian.net", true},
		"Atlassian's gateway": {"https://api.atlassian.com/ex/jira/abc", true},
		"somewhere else":      {"https://collector.example.com", false},
		"a lookalike host":    {"https://acme.atlassian.net.evil.com", false},
		"not resolved yet":    {"", true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &jira.Config{
				SiteURL: "https://acme.atlassian.net", Email: "svc@example.com",
				APIToken: "t", APIBaseURL: tc.base,
			}
			err := cfg.Validate()
			if (err == nil) != tc.accepted {
				t.Errorf("Validate() = %v, want accepted=%v", err, tc.accepted)
			}
		})
	}
}

// A 403 means the credential authenticated and then lacked a permission. That
// is an answer from the right address, so retrying it elsewhere would turn a
// precise permissions error into a misleading credential error.
func TestForbiddenIsNotTreatedAsTheWrongAddress(t *testing.T) {
	t.Parallel()

	var tenantInfoCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_edge/tenant_info" {
			atomic.AddInt32(&tenantInfoCalls, 1)
		}
		writeJSON(w, http.StatusForbidden, `{"errorMessages":["You do not have permission"]}`)
	}))
	t.Cleanup(srv.Close)

	c := jira.New(srv.Client())
	cfg := &jira.Config{SiteURL: srv.URL, Email: "svc@example.com", APIToken: "t"}

	if _, err := c.Resolve(context.Background(), cfg); !errors.Is(err, connector.ErrForbidden) {
		t.Fatalf("got %v, want ErrForbidden", err)
	}
	if tenantInfoCalls != 0 {
		t.Error("a 403 triggered a gateway fallback; only a 401 should")
	}
}

// When neither address accepts the credential, the error says so and names the
// scopes, since that is the most common cause.
func TestRejectedAtBothAddressesExplainsWhy(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_edge/tenant_info" {
			writeJSON(w, http.StatusOK, `{"cloudId":"abc"}`)
			return
		}
		writeJSON(w, http.StatusUnauthorized, `{}`)
	}))
	t.Cleanup(srv.Close)

	c := jira.New(srv.Client(), jira.WithGatewayHost(srv.URL))
	cfg := &jira.Config{SiteURL: srv.URL, Email: "svc@example.com", APIToken: "wrong"}

	_, err := c.Resolve(context.Background(), cfg)
	if !errors.Is(err, connector.ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}

	var ce *connector.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error was not classified: %v", err)
	}
	for _, scope := range []string{"read:jira-user", "read:jira-work", "write:jira-work"} {
		if !strings.Contains(ce.Message, scope) {
			t.Errorf("message does not mention the %s scope: %q", scope, ce.Message)
		}
	}
}

// A stored configuration with no address cannot have come from Resolve, so it
// fails with an instruction rather than silently calling the wrong place.
func TestUnresolvedConfigCannotBeUsed(t *testing.T) {
	t.Parallel()

	c := jira.New(nil)
	cfg := &jira.Config{SiteURL: "https://acme.atlassian.net", Email: "a@b.co", APIToken: "t"}

	_, err := c.ListProjects(context.Background(), cfg, jira.ListProjectsParams{})
	if !errors.Is(err, connector.ErrConfigInvalid) {
		t.Fatalf("got %v, want ErrConfigInvalid", err)
	}
}

// The link a person clicks must be the site, never the gateway: the gateway
// serves the REST API and a browser cannot use it.
func TestBrowseLinksPointAtTheSiteNotTheGateway(t *testing.T) {
	t.Parallel()

	cfg := &jira.Config{
		SiteURL:    "https://acme.atlassian.net/",
		Email:      "a@b.co",
		APIToken:   "t",
		APIBaseURL: "https://api.atlassian.com/ex/jira/76b51339",
	}
	if got := cfg.Metadata()["siteUrl"]; got != "https://acme.atlassian.net" {
		t.Errorf("metadata site = %q, want the normalised site", got)
	}
}

// Metadata is stored unencrypted, so it must never carry the credential.
func TestMetadataCarriesNoCredential(t *testing.T) {
	t.Parallel()

	cfg := valid()
	encoded, err := json.Marshal(cfg.Metadata())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), cfg.APIToken) {
		t.Errorf("metadata leaked the API token: %s", encoded)
	}
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
