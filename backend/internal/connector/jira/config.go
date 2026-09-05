package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"github.com/dbiderman/identityhub/backend/internal/connector"
)

// ConnectorType is the value stored in connections.connector_type for Jira.
const ConnectorType connector.Type = "jira"

const (
	// tenantInfoPath is the unauthenticated endpoint exposing a site's cloud ID.
	tenantInfoPath = "/_edge/tenant_info"

	// defaultGateway is Atlassian's API gateway.
	defaultGateway = "https://api.atlassian.com"
)

// Config is the Jira connector's stored configuration.
//
// It is the plaintext of the encryption envelope in the connections table, so
// every field is secret by association even though only APIToken is a
// credential on its own. The struct deliberately has no String method, so it
// cannot be printed by a %v in a log line.
type Config struct {
	SiteURL  string `json:"siteUrl"`
	Email    string `json:"email"`
	APIToken string `json:"apiToken"`

	// APIBaseURL is where the REST API is actually reached, which is not always
	// the site.
	//
	// Atlassian serves the same API at two addresses, and which one accepts a
	// credential depends on how the token was created: a token with scopes only
	// works through the gateway, one without only against the site. Using the
	// wrong one returns a bare 401 with nothing to indicate why.
	//
	// It is left empty by whoever fills in the form and resolved once, by
	// Resolve, when the connection is created. So it is a configuration value
	// like the others -- one that a connection cannot be created without, and
	// that no later call has to discover again.
	APIBaseURL string `json:"apiBaseUrl,omitempty"`
}

// ConnectorType implements connector.Config.
func (c *Config) ConnectorType() connector.Type { return ConnectorType }

// Requirements implements connector.Connector.
//
// Three fields, and the help text matters as much as the names: an Atlassian
// API token is created in a place nobody finds by guessing, and the scopes it
// needs are chosen on that same screen. A form that says "API token" and
// nothing else is a form people abandon.
//
// apiBaseUrl is absent on purpose. It is a configuration value, but not one
// anybody types: Resolve discovers it.
func (c *Connector) Requirements() connector.Requirements {
	return connector.Requirements{
		Setup: []connector.Step{
			{
				Text:     "Open the API tokens page for your Atlassian account. It is under Profile, then Security — not inside Jira itself, which is why it is hard to find.",
				URL:      "https://id.atlassian.com/manage-profile/security/api-tokens",
				LinkText: "id.atlassian.com — API tokens",
			},
			{
				Text: "Choose Create API token with scopes. A classic token works too, but it carries everything you can do; a scoped one carries only what IdentityHub needs.",
			},
			{
				Text: "Name it something you will recognise later — this is a non-human identity, and in a year somebody has to know what it was for. IdentityHub filing NHI findings is a reasonable name.",
			},
			{
				Text: "Select the Jira app, then grant exactly three scopes: read:jira-user to see whose token it is, read:jira-work to list projects, and write:jira-work to file tickets. Nothing else is needed, and scopes cannot be added to a token afterwards.",
			},
			{
				Text: "Copy the token before closing the dialog. Atlassian shows it once, and IdentityHub stores only an encrypted copy — neither of us can show it to you again.",
			},
			{
				Text: "Paste it below, with the Jira address you use in a browser and the email of the Atlassian account the token belongs to. Tickets are filed as that person.",
			},
		},
		Fields: []connector.Field{
			{
				Name:     "siteUrl",
				Label:    "Jira site URL",
				Kind:     connector.FieldURL,
				Required: true,
				Help:     "The address you use to reach Jira in a browser.",
				Example:  "https://your-company.atlassian.net",
			},
			{
				Name:     "email",
				Label:    "Atlassian account email",
				Kind:     connector.FieldEmail,
				Required: true,
				Help: "The account the API token belongs to. Tickets are filed as this " +
					"person, so it is worth using a service account rather than your own.",
				Example: "automation@your-company.com",
			},
			{
				Name:     "apiToken",
				Label:    "API token",
				Kind:     connector.FieldSecret,
				Required: true,
				Help: "Created on your Atlassian account, not in Jira. " +
					"If you make a scoped token, grant read:jira-user, read:jira-work " +
					"and write:jira-work and nothing else.",
			},
		},
	}
}

// Validate implements connector.Config. It runs on the way in, before a
// configuration is encrypted, and on the way out, so a row written by an older
// version or corrupted is rejected here rather than producing a confusing
// failure from the Jira API.
//
// Each failure names the field it is about, so a caller can attach it to the
// input someone typed rather than showing one message for a whole form.
func (c *Config) Validate() error {
	if err := validateSiteURL(c.SiteURL); err != nil {
		return err
	}

	// net/mail implements RFC 5322. Checking for an "@" would accept addresses
	// Jira rejects and reject ones it accepts.
	email := strings.TrimSpace(c.Email)
	if email == "" {
		return connector.InvalidConfig("email", "An account email is required.")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return connector.InvalidConfig("email", "This is not a valid email address.")
	}
	if strings.TrimSpace(c.APIToken) == "" {
		return connector.InvalidConfig("apiToken", "An API token is required.")
	}

	// Absent on the way in, required on the way out.
	return validateAPIBaseURL(c.APIBaseURL, c.SiteURL)
}

// validateSiteURL checks the Jira site is an https origin with no path.
func validateSiteURL(raw string) error {
	const field = "siteUrl"

	site := strings.TrimSpace(raw)
	if site == "" {
		return connector.InvalidConfig(field, "A Jira site URL is required.")
	}

	parsed, err := url.Parse(site)
	if err != nil {
		return connector.InvalidConfig(field, "This is not a valid URL.")
	}
	// The credential travels on every request, so plaintext transport is
	// refused. In practice http:// here is a typo rather than a choice.
	if parsed.Scheme != "https" {
		return connector.InvalidConfig(field, "The site URL must use https.")
	}
	if parsed.Host == "" {
		return connector.InvalidConfig(field,
			"The site URL is missing a host, for example https://your-site.atlassian.net")
	}
	if strings.Trim(parsed.Path, "/") != "" {
		return connector.InvalidConfig(field,
			"The site URL must not include a path, for example https://your-site.atlassian.net")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return connector.InvalidConfig(field, "The site URL must not include a query or fragment.")
	}
	if parsed.User != nil {
		return connector.InvalidConfig(field, "The site URL must not embed credentials.")
	}
	return nil
}

// validateAPIBaseURL accepts an empty value, since it is resolved rather than
// supplied, but rejects one that could not have come from Resolve.
//
// Resolve produces exactly two shapes: the site itself, or Atlassian's gateway.
// Anything else in a stored row is corruption or tampering, and is refused on
// the way out rather than used. Connect already ignores whatever arrived, so
// this is the second of two checks and not the only one.
func validateAPIBaseURL(raw, siteURL string) error {
	const field = "apiBaseUrl"

	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return connector.InvalidConfig(field, "The resolved API address is not a valid URL.")
	}
	if parsed.Scheme != "https" {
		return connector.InvalidConfig(field, "The resolved API address must use https.")
	}

	site, err := url.Parse(strings.TrimRight(strings.TrimSpace(siteURL), "/"))
	if err == nil && parsed.Host != site.Host && parsed.Host != gatewayHost(defaultGateway) {
		return connector.InvalidConfig(field,
			"The resolved API address is neither this site nor Atlassian's API gateway.")
	}
	return nil
}

// gatewayHost is the host part of an API gateway address.
func gatewayHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// normalizedSiteURL strips whitespace and any trailing slash.
func (c *Config) normalizedSiteURL() string {
	return strings.TrimRight(strings.TrimSpace(c.SiteURL), "/")
}

// baseURL is the address requests go to. A configuration that has not been
// resolved has none, which is a programming error rather than a user one.
func (c *Config) baseURL() (string, error) {
	if c.APIBaseURL == "" {
		return "", connector.InvalidConfig("apiBaseUrl",
			"This connection was stored without a resolved API address. Reconnect the integration.")
	}
	return c.APIBaseURL, nil
}

// browseURL builds the human-facing link for an issue.
//
// Always the site, never the gateway: the gateway serves the REST API, and a
// person clicking through needs an address their browser can open.
func (c *Config) browseURL(key string) string {
	return c.normalizedSiteURL() + "/browse/" + key
}

// Metadata implements connector.Config, returning the fields that are safe to
// store unencrypted and show in a UI. The API token is deliberately absent.
func (c *Config) Metadata() map[string]string {
	return map[string]string{
		"siteUrl": c.normalizedSiteURL(),
		"email":   strings.TrimSpace(c.Email),
	}
}

// Resolve completes a configuration by discovering where its API lives, and
// confirms the credential works, and confirms it is allowed to do what this
// product needs, while it is there.
//
// The connect flow calls this once, before the configuration is stored. Nothing
// else does: every later call reads the address that was resolved here, with a
// token whose scopes were checked here.
func (c *Connector) Resolve(ctx context.Context, cfg *Config) (Account, error) {
	// Always re-derived, never taken from the request.
	//
	// apiBaseUrl is a field on this struct, so it decodes from a connect body
	// like any other -- and a supplied value used to skip discovery. That made
	// it an instruction to deliver the customer's Jira credential to an
	// arbitrary https host: verifyScopes authenticates against it, a host that
	// answers plausibly passes the check, and the address is then stored. Worse
	// quietly: Metadata() carries only siteUrl, so the connection listing, the
	// audit event and every issue link would still name the real Atlassian site
	// while the tickets went elsewhere.
	//
	// The field stays on the struct because a stored configuration must carry
	// it -- the address is discovered once, here, and read by every later call.
	// What changed is that connect no longer trusts what arrived.
	base, err := c.discoverAPIBaseURL(ctx, cfg)
	if err != nil {
		return Account{}, err
	}
	cfg.APIBaseURL = base

	// What the token is allowed to do, before the connection is stored. A
	// token that can read but not write connects happily and then fails on the
	// first ticket somebody files, which is the worst moment to find out.
	if err := c.verifyScopes(ctx, cfg); err != nil {
		return Account{}, err
	}
	return c.VerifyCredentials(ctx, cfg)
}

// discoverAPIBaseURL tries the site, then the gateway.
func (c *Connector) discoverAPIBaseURL(ctx context.Context, cfg *Config) (string, error) {
	site := cfg.normalizedSiteURL()

	status, err := c.probe(ctx, site, cfg)
	switch {
	case err != nil:
		return "", connector.Errorf(connector.ErrUnavailable,
			"Could not reach the Jira site. Check the site URL and try again.")
	case status != http.StatusUnauthorized:
		// Only a 401 can mean "wrong address". A 403 is a permissions answer
		// from the right one, and retrying that elsewhere would turn a precise
		// error into a misleading credential error.
		return site, nil
	}

	cloudID, err := c.cloudID(ctx, site)
	if err != nil {
		return "", connector.Errorf(connector.ErrUnauthorized,
			"Jira rejected the credential. Check the account email and API token.")
	}

	gateway := c.gateway() + "/ex/jira/" + cloudID
	if status, err := c.probe(ctx, gateway, cfg); err == nil && status == http.StatusOK {
		return gateway, nil
	}

	return "", connector.Errorf(connector.ErrUnauthorized,
		"Jira rejected the credential at both the site and the Atlassian API gateway. "+
			"Check the account email, and that the API token is valid and has the "+
			"read:jira-user, read:jira-work and write:jira-work scopes.")
}

// probe asks whether a credential is accepted at an address, for discovery.
func (c *Connector) probe(ctx context.Context, base string, cfg *Config) (int, error) {
	return c.statusAt(ctx, base, cfg, http.MethodGet, "/rest/api/3/myself", "")
}

// statusAt issues one authenticated request and reports the status only.
//
// It exists because two things here need a status and nothing else: finding
// which of Atlassian's two addresses accepts a credential, and finding out what
// that credential is allowed to do. Both are questions the library's typed
// methods cannot ask, because both need the answer to a request that fails.
func (c *Connector) statusAt(ctx context.Context, base string, cfg *Config, method, path, body string) (int, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth(strings.TrimSpace(cfg.Email), strings.TrimSpace(cfg.APIToken))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// cloudID reads a site's identifier from its public tenant info endpoint. It
// takes no credential, so resolving costs nothing but a request.
func (c *Connector) cloudID(ctx context.Context, site string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, site+tenantInfoPath, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tenant info for %s answered %s", site, resp.Status)
	}

	var info struct {
		CloudID string `json:"cloudId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.CloudID == "" {
		return "", fmt.Errorf("tenant info for %s carried no cloud ID", site)
	}
	return info.CloudID, nil
}

// gateway allows a test to serve both addresses from one server.
func (c *Connector) gateway() string {
	if c.gatewayHost != "" {
		return c.gatewayHost
	}
	return defaultGateway
}
