package issuetracker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/dbiderman/identityhub/backend/internal/capabilities/issuetracker"
	"github.com/dbiderman/identityhub/backend/internal/connector"
)

// This package is the one door between the product and any provider, and every
// call through it is a marshal, an Execute, and an unmarshal. These tests are
// about that door: what goes through it, what comes back, and what happens when
// the far side answers something unexpected.
//
// A stub connector is all it takes -- no database, no network, no Jira.

// stub is a connector that records what it was asked and answers what a test
// told it to.
type stub struct {
	actions  []connector.Action
	action   connector.Action
	params   json.RawMessage
	response string
	err      error
}

func (s *stub) Type() connector.Type                          { return "stub" }
func (s *stub) DecodeConfig([]byte) (connector.Config, error) { return nil, nil }
func (s *stub) Requirements() connector.Requirements          { return connector.Requirements{} }

func (s *stub) Actions() []connector.Action {
	if s.actions != nil {
		return s.actions
	}
	return issuetracker.Contract
}

func (s *stub) Execute(_ context.Context, _ connector.Config, req connector.Request) (connector.Response, error) {
	s.action, s.params = req.Action, req.Params
	if s.err != nil {
		return connector.Response{}, s.err
	}
	return connector.Response{Data: json.RawMessage(s.response)}, nil
}

// stubConfig is a credential-bearing configuration, which is what every real
// connector.Config is.
type stubConfig struct {
	Token string `json:"token"`
}

func (stubConfig) ConnectorType() connector.Type { return "stub" }
func (stubConfig) Validate() error               { return nil }
func (stubConfig) Metadata() map[string]string   { return map[string]string{"site": "acme.test"} }

// A Client holds a decrypted credential, and is embedded in
// connections.Connection, which handlers hold. The two ways it could leave this
// process without anybody meaning it to are a response it gets marshalled into
// and a log line it gets printed in, so both are closed and both are checked
// here. Nothing today does either; this is what keeps that true.
func TestAClientCannotLeakItsCredentialByBeingMarshalledOrLogged(t *testing.T) {
	t.Parallel()

	const secret = "atlassian-api-token-do-not-print"
	client := issuetracker.Client{Connector: &stub{}, Config: stubConfig{Token: secret}}

	encoded, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("marshal a client: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Errorf("json = %s, and it carries the credential", encoded)
	}
	if string(encoded) != "{}" {
		t.Errorf("json = %s, want an empty object: neither field may be emitted", encoded)
	}

	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("filing a finding", "connection", client)
	if strings.Contains(logged.String(), secret) {
		t.Errorf("log = %s, and it carries the credential", logged.String())
	}
	// What is left has to be worth logging, or the next person will reach past
	// it for the struct itself.
	if !strings.Contains(logged.String(), "stub") {
		t.Errorf("log = %s, want it to name the connector", logged.String())
	}
}

// A connector that implements four of the five actions is not an issue tracker,
// and has to say so in those words. Discovering it later, from inside a
// workflow, produces "unsupported action list_kinds" instead -- which names the
// symptom rather than the thing that is wrong.
func TestAConnectorMissingOneActionIsNotAnIssueTracker(t *testing.T) {
	t.Parallel()

	for _, missing := range issuetracker.Contract {
		t.Run(string(missing), func(t *testing.T) {
			t.Parallel()

			partial := &stub{}
			for _, action := range issuetracker.Contract {
				if action != missing {
					partial.actions = append(partial.actions, action)
				}
			}

			err := issuetracker.Implements(partial)
			if !errors.Is(err, connector.ErrUnsupportedAction) {
				t.Fatalf("got %v, want ErrUnsupportedAction", err)
			}
		})
	}

	if err := issuetracker.Implements(&stub{}); err != nil {
		t.Errorf("a connector implementing the whole contract was refused: %v", err)
	}
}

// The product's parameters have to arrive as the JSON the contract describes.
// Nothing checks this at compile time -- the two sides declare their own
// structs deliberately -- so it is checked here.
func TestTheProductsParametersCrossAsTheContractsJSON(t *testing.T) {
	t.Parallel()

	c := &stub{response: `[{"id":"1","key":"NHI","name":"NHI Findings"}]`}
	client := issuetracker.Client{Connector: c}

	targets, err := client.ListTargets(context.Background(), "nhi", 25)
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}

	if c.action != issuetracker.ActionListTargets {
		t.Errorf("action = %q, want %q", c.action, issuetracker.ActionListTargets)
	}
	var sent map[string]any
	if err := json.Unmarshal(c.params, &sent); err != nil {
		t.Fatalf("the parameters were not JSON: %v", err)
	}
	if sent["query"] != "nhi" || sent["limit"] != float64(25) {
		t.Errorf("params = %v, want query=nhi limit=25", sent)
	}

	if len(targets) != 1 || targets[0].Key != "NHI" || targets[0].Name != "NHI Findings" {
		t.Errorf("targets = %+v, want the one the connector answered", targets)
	}
}

// A connector that answers in a shape the contract does not describe is a
// defect in the connector, not something a caller can fix. It is reported as an
// unusable integration -- which is a 409 telling somebody to reconnect -- rather
// than as a JSON error, which would surface as a 500 saying nothing.
func TestAnAnswerTheContractDoesNotDescribeIsAnUnusableIntegration(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"not JSON at all":     `<html>upstream proxy error</html>`,
		"the wrong shape":     `{"targets": []}`,
		"a truncated payload": `[{"id":"1",`,
	}

	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client := issuetracker.Client{Connector: &stub{response: answer}}
			_, err := client.ListTargets(context.Background(), "", 10)
			if !errors.Is(err, connector.ErrConfigInvalid) {
				t.Fatalf("got %v, want ErrConfigInvalid", err)
			}
			// The message is what a person reads, so it names the integration
			// and the action rather than quoting a parser.
			if !strings.Contains(err.Error(), "list_targets") {
				t.Errorf("message = %q, want it to name the action", err)
			}
		})
	}
}

// An empty answer is not a malformed one. verify_credentials returns an Owner,
// but an action that legitimately has nothing to say must not be turned into a
// decode failure.
func TestAnEmptyAnswerIsNotAFailure(t *testing.T) {
	t.Parallel()

	client := issuetracker.Client{Connector: &stub{response: ""}}
	if _, err := client.FetchTickets(context.Background(), []string{"NHI-1"}); err != nil {
		t.Fatalf("an empty answer was treated as a failure: %v", err)
	}
}

// A connector's own failure travels out unchanged. This package adds nothing to
// it: the connector is what knew the credential was rejected, and rewriting its
// message here would lose the sentence a person needs.
func TestAConnectorsFailureIsNotRewritten(t *testing.T) {
	t.Parallel()

	rejected := connector.Errorf(connector.ErrUnauthorized,
		"Jira rejected the API token for this site.")
	client := issuetracker.Client{Connector: &stub{err: rejected}}

	_, err := client.Identify(context.Background())
	if !errors.Is(err, connector.ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
	if err.Error() != rejected.Error() {
		t.Errorf("message = %q, want the connector's own %q", err, rejected)
	}
}
