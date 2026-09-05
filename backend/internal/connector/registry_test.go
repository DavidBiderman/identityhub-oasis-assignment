package connector_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/connector/jira"
)

func registry(t *testing.T) *connector.Registry {
	t.Helper()
	r := connector.NewRegistry()
	jira.Register(r, nil)
	return r
}

// Resolving the type and decoding its bytes is the only supported path from a
// stored configuration to a usable connector, and connections.Configure is the
// one place it runs.
func TestStoredBytesBecomeAUsableConfiguration(t *testing.T) {
	t.Parallel()

	plaintext := []byte(`{"siteUrl":"https://acme.atlassian.net","email":"a@b.co","apiToken":"tok"}`)

	conn, cfg, err := configure(t, registry(t), jira.ConnectorType, plaintext)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if conn.Type() != jira.ConnectorType {
		t.Errorf("connector type = %q", conn.Type())
	}

	jiraCfg, ok := cfg.(*jira.Config)
	if !ok {
		t.Fatalf("config type = %T, want *jira.Config", cfg)
	}
	if jiraCfg.SiteURL != "https://acme.atlassian.net" {
		t.Errorf("site URL = %q", jiraCfg.SiteURL)
	}

	// Metadata is stored unencrypted, so it must not carry the credential.
	for k, v := range cfg.Metadata() {
		if strings.Contains(v, "tok") {
			t.Errorf("metadata field %q leaked the API token: %q", k, v)
		}
	}
}

// Decryption succeeding proves the bytes are ours, not that they are the right
// shape. Every one of these must be rejected before any outbound call.
func TestUnusableConfigurationsAreRejected(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":               ``,
		"not json":            `not json at all`,
		"json null":           `null`,
		"missing site":        `{"email":"a@b.co","apiToken":"tok"}`,
		"missing email":       `{"siteUrl":"https://acme.atlassian.net","apiToken":"tok"}`,
		"missing token":       `{"siteUrl":"https://acme.atlassian.net","email":"a@b.co"}`,
		"plaintext transport": `{"siteUrl":"http://acme.atlassian.net","email":"a@b.co","apiToken":"tok"}`,
		"site with path":      `{"siteUrl":"https://acme.atlassian.net/jira","email":"a@b.co","apiToken":"tok"}`,
		"site with creds":     `{"siteUrl":"https://u:p@acme.atlassian.net","email":"a@b.co","apiToken":"tok"}`,
		"another connector":   `{"webhookUrl":"https://hooks.example.com/x","token":"tok"}`,
		"unknown field":       `{"siteUrl":"https://acme.atlassian.net","email":"a@b.co","apiToken":"tok","extra":1}`,
		"trailing data":       `{"siteUrl":"https://acme.atlassian.net","email":"a@b.co","apiToken":"tok"} {}`,
	}

	r := registry(t)
	for name, plaintext := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := configure(t, r, jira.ConnectorType, []byte(plaintext)); !errors.Is(err, connector.ErrConfigInvalid) {
				t.Fatalf("got %v, want ErrConfigInvalid", err)
			}
		})
	}
}

// Email validation uses net/mail (RFC 5322) rather than a substring check, so
// these must follow the standard rather than a hand-rolled approximation.
func TestEmailValidationFollowsRFC5322(t *testing.T) {
	t.Parallel()

	valid := []string{
		"a@b.co",
		"first.last@example.co.uk",
		"user+tag@example.com",
		"Display Name <user@example.com>",
		`"quoted local"@example.com`,
	}
	invalid := []string{
		"nope",
		"@example.com",
		"user@",
		"user example.com",
		"user@@example.com",
		"",
	}

	check := func(email string) error {
		cfg := &jira.Config{SiteURL: "https://acme.atlassian.net", Email: email, APIToken: "tok"}
		return cfg.Validate()
	}
	for _, e := range valid {
		if err := check(e); err != nil {
			t.Errorf("valid address %q rejected: %v", e, err)
		}
	}
	for _, e := range invalid {
		if err := check(e); err == nil {
			t.Errorf("invalid address %q accepted", e)
		}
	}
}

func TestAnUnregisteredTypeIsRejected(t *testing.T) {
	t.Parallel()

	plaintext := []byte(`{"siteUrl":"https://acme.atlassian.net","email":"a@b.co","apiToken":"tok"}`)
	if _, _, err := configure(t, registry(t), "slack", plaintext); !errors.Is(err, connector.ErrConfigInvalid) {
		t.Fatalf("got %v, want ErrConfigInvalid", err)
	}
}

func TestStrictDecodeBoundsInput(t *testing.T) {
	t.Parallel()

	huge := []byte(`{"siteUrl":"https://acme.atlassian.net","email":"a@b.co","apiToken":"` +
		strings.Repeat("x", 70<<10) + `"}`)
	if err := connector.StrictDecode(huge, &jira.Config{}); !errors.Is(err, connector.ErrConfigInvalid) {
		t.Fatalf("oversized config: got %v, want ErrConfigInvalid", err)
	}
}

// Errors must not quote the configuration, since its plaintext is a credential.
func TestDecodeErrorsDoNotLeakTheCredential(t *testing.T) {
	t.Parallel()

	const token = "super-secret-token-value"
	plaintext := []byte(`{"siteUrl":"https://acme.atlassian.net","email":"a@b.co","apiToken":"` + token + `","extra":true}`)

	_, _, err := configure(t, registry(t), jira.ConnectorType, plaintext)
	if err == nil {
		t.Fatal("expected a rejection")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked the credential: %v", err)
	}
}

// A configuration that round-trips must come back the same. StrictDecode is
// the only way in, so this is the whole contract for stored bytes.
func TestConfigRoundTrips(t *testing.T) {
	t.Parallel()

	plaintext := []byte(`{"siteUrl":"https://acme.atlassian.net","email":"a@b.co","apiToken":"tok"}`)

	_, cfg, err := configure(t, registry(t), jira.ConnectorType, plaintext)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if cfg.(*jira.Config).APIToken != "tok" {
		t.Error("the round trip lost the token")
	}
}

func TestRegistryReportsItsTypes(t *testing.T) {
	t.Parallel()

	types := registry(t).Types()
	if len(types) != 1 || types[0] != jira.ConnectorType {
		t.Fatalf("Types() = %v, want [jira]", types)
	}
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("duplicate registration did not panic")
		}
	}()
	r := registry(t)
	jira.Register(r, nil)
}

// configure is the two steps a caller takes to go from stored bytes to a usable
// configuration: the registry resolves the type, the connector decodes its own
// bytes. The Registry used to do both, which made it two things. It mirrors the
// middle of connections.Configure, which is the only caller in the application.
func configure(t *testing.T, r *connector.Registry, typ connector.Type, plaintext []byte) (connector.Connector, connector.Config, error) {
	t.Helper()

	conn, err := r.Get(typ)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := conn.DecodeConfig(plaintext)
	if err != nil {
		return nil, nil, err
	}
	return conn, cfg, nil
}
