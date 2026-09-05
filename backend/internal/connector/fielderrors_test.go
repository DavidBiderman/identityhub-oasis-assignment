package connector_test

import (
	"errors"
	"testing"

	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/connector/jira"
)

// A rejected configuration must name the field it is about, structurally, so
// the HTTP layer can attach it to the input rather than parsing it out of a
// message.
func TestConfigRejectionNamesItsField(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ config, field string }{
		"plaintext transport": {`{"siteUrl":"http://acme.atlassian.net","email":"a@b.co","apiToken":"t"}`, "siteUrl"},
		"site with path":      {`{"siteUrl":"https://acme.atlassian.net/jira","email":"a@b.co","apiToken":"t"}`, "siteUrl"},
		"missing site":        {`{"email":"a@b.co","apiToken":"t"}`, "siteUrl"},
		"malformed email":     {`{"siteUrl":"https://acme.atlassian.net","email":"nope","apiToken":"t"}`, "email"},
		"missing email":       {`{"siteUrl":"https://acme.atlassian.net","apiToken":"t"}`, "email"},
		"missing token":       {`{"siteUrl":"https://acme.atlassian.net","email":"a@b.co"}`, "apiToken"},
	}

	r := connector.NewRegistry()
	jira.Register(r, nil)

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// The registry resolves the type; the connector decodes the
			// bytes. Two calls, because they are two jobs.
			conn, err := r.Get(jira.ConnectorType)
			if err != nil {
				t.Fatalf("get connector: %v", err)
			}
			_, err = conn.DecodeConfig([]byte(tc.config))
			if !errors.Is(err, connector.ErrConfigInvalid) {
				t.Fatalf("got %v, want ErrConfigInvalid", err)
			}

			var ce *connector.Error
			if !errors.As(err, &ce) {
				t.Fatalf("error was not classified: %v", err)
			}
			detail, ok := ce.Fields[tc.field]
			if !ok {
				t.Fatalf("error names fields %v, want %q among them", ce.Fields, tc.field)
			}
			if detail == "" {
				t.Errorf("field %q has no explanation", tc.field)
			}
		})
	}
}
