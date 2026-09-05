package jira_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dbiderman/identityhub/backend/internal/capabilities/issuetracker"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/connector/jira"
)

// stub runs an in-process Jira and returns a connector wired to it, plus a
// config pointing at the stub. Tests assert against the real requests the
// adapter builds, so a change in path, payload or auth is caught here rather
// than against a live Atlassian site.
func stub(t *testing.T, handler http.HandlerFunc) (*jira.Connector, *jira.Config) {
	t.Helper()

	// The connector probes /myself once per site and account to learn which
	// Atlassian entry point the credential works against. Answering it here
	// keeps every test focused on the call it is actually about.
	withProbe := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/rest/api/3/myself") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"accountId":"5b10a","emailAddress":"svc@example.com","displayName":"Service"}`))
			return
		}
		handler(w, r)
	}

	srv := httptest.NewServer(http.HandlerFunc(withProbe))
	t.Cleanup(srv.Close)

	c := jira.New(srv.Client())

	// A stored configuration always carries a resolved API address, because
	// that is what Resolve produces at connect time. Tests of the operations
	// start from that state; resolution itself is tested in config_test.go.
	cfg := &jira.Config{
		SiteURL:    srv.URL,
		Email:      "svc@example.com",
		APIToken:   "tok",
		APIBaseURL: srv.URL,
	}
	return c, cfg
}

func decode[T any](t *testing.T, resp connector.Response) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func request(t *testing.T, action connector.Action, params any) connector.Request {
	t.Helper()
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("encode params: %v", err)
	}
	return connector.Request{Action: action, Params: encoded}
}

// Every action the connector declares must be handled by Execute, and every
// action Execute handles must be declared. This is what keeps the string-keyed
// dispatch honest as the connector grows.
func TestDeclaredActionsMatchDispatch(t *testing.T) {
	t.Parallel()

	c, cfg := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	for _, action := range c.Actions() {
		_, err := c.Execute(context.Background(), cfg, connector.Request{Action: action})
		if errors.Is(err, connector.ErrUnsupportedAction) {
			t.Errorf("action %q is declared but not handled by Execute", action)
		}
	}
}

// An action outside this connector's own set must be rejected by the cast, with
// a message naming both the action and the connector type.
func TestUnknownActionIsRejectedWithAUsefulMessage(t *testing.T) {
	t.Parallel()

	reached := false
	c, cfg := stub(t, func(w http.ResponseWriter, r *http.Request) { reached = true })

	_, err := c.Execute(context.Background(), cfg, connector.Request{Action: "delete_everything"})
	if !errors.Is(err, connector.ErrUnsupportedAction) {
		t.Fatalf("got %v, want ErrUnsupportedAction", err)
	}

	var ce *connector.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error was not classified: %v", err)
	}
	for _, want := range []string{"delete_everything", "jira", "cannot perform"} {
		if !strings.Contains(ce.Message, want) {
			t.Errorf("message %q does not mention %q", ce.Message, want)
		}
	}
	if ce.Retryable() {
		t.Error("an unsupported action was reported as retryable")
	}
	if reached {
		t.Error("an unsupported action still reached the network")
	}
}

// The action is resolved before the configuration is used, so a bad request
// never reaches a credential.
func TestUnknownActionIsRejectedBeforeTheConfigIsRead(t *testing.T) {
	t.Parallel()

	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {})

	// A configuration of the wrong type would normally fail the assertion in
	// Execute; the action check must fire first.
	_, err := c.Execute(context.Background(), foreignConfig{}, connector.Request{Action: "nope"})
	if !errors.Is(err, connector.ErrUnsupportedAction) {
		t.Fatalf("got %v, want ErrUnsupportedAction", err)
	}
}

// foreignConfig belongs to no registered connector.
type foreignConfig struct{}

func (foreignConfig) ConnectorType() connector.Type { return "somewhere-else" }
func (foreignConfig) Validate() error               { return nil }
func (foreignConfig) Metadata() map[string]string   { return nil }

func TestCreateIssueSendsADFDescription(t *testing.T) {
	t.Parallel()

	var got map[string]any
	c, cfg := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rest/api/3/issue") || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Basic ") {
			t.Errorf("Authorization = %q, want HTTP Basic", auth)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"10001","key":"NHI-1"}`))
	})

	resp, err := c.Execute(context.Background(), cfg, request(t, jira.ActionFileTicket.Generic(),
		issuetracker.NewFinding{
			TargetKey: "NHI", KindID: "10002",
			Title:       "Stale Service Account: svc-deploy-prod",
			Description: "Last used 400 days ago.",
			Labels:      []string{"identityhub"},
		}))
	if err != nil {
		t.Fatalf("file ticket: %v", err)
	}

	issue := decode[issuetracker.TicketRef](t, resp)
	if issue.Key != "NHI-1" {
		t.Errorf("issue key = %q, want NHI-1", issue.Key)
	}
	if !strings.HasSuffix(issue.URL, "/browse/NHI-1") {
		t.Errorf("issue URL = %q, want a /browse/ link", issue.URL)
	}

	fields := got["fields"].(map[string]any)
	desc, ok := fields["description"].(map[string]any)
	if !ok {
		t.Fatalf("description was not an object: %#v", fields["description"])
	}
	if desc["type"] != "doc" {
		t.Errorf("description type = %v, want an ADF doc", desc["type"])
	}
}

// A caller that does not pin an issue type must get one resolved from the
// project, not a hardcoded "Task".
func TestCreateIssueResolvesIssueTypeFromProject(t *testing.T) {
	t.Parallel()

	var createBody map[string]any
	c, cfg := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "createmeta") {
			if !strings.Contains(r.URL.Path, "/issuetypes") {
				t.Errorf("used the deprecated createmeta form: %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"issueTypes":[
				{"id":"1","name":"Bug","subtask":false},
				{"id":"2","name":"Task","subtask":false},
				{"id":"3","name":"Sub-task","subtask":true}]}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&createBody)
		_, _ = w.Write([]byte(`{"id":"10001","key":"NHI-7"}`))
	})

	if _, err := c.Execute(context.Background(), cfg, request(t, jira.ActionFileTicket.Generic(),
		issuetracker.NewFinding{TargetKey: "NHI", Title: "s", Description: "d"})); err != nil {
		t.Fatalf("file ticket: %v", err)
	}

	issuetype := createBody["fields"].(map[string]any)["issuetype"].(map[string]any)
	if issuetype["id"] != "2" {
		t.Errorf("resolved issue type = %v, want Task (id 2)", issuetype["id"])
	}
}

func TestListIssueTypesExcludesSubtasks(t *testing.T) {
	t.Parallel()

	c, cfg := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issueTypes":[
			{"id":"1","name":"Task","subtask":false},
			{"id":"3","name":"Sub-task","subtask":true}]}`))
	})

	resp, err := c.Execute(context.Background(), cfg, request(t, jira.ActionListKinds.Generic(),
		map[string]string{"targetKey": "NHI"}))
	if err != nil {
		t.Fatalf("list kinds: %v", err)
	}

	types := decode[[]issuetracker.Kind](t, resp)
	if len(types) != 1 || types[0].Name != "Task" {
		t.Fatalf("got %+v, want only the creatable Task type", types)
	}
}

// Upstream statuses must be classified so the HTTP layer can pick a status code
// and Temporal can decide whether to retry. The library collapses 403, 429 and
// 503 into one sentinel, so this asserts the response code is what is used.
func TestErrorClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		status    int
		body      string
		want      error
		retryable bool
	}{
		{"rejected credential", 401, `{}`, connector.ErrUnauthorized, false},
		{"missing permission", 403, `{}`, connector.ErrForbidden, false},
		{"unknown project", 404, `{}`, connector.ErrNotFound, false},
		{"rate limited", 429, `{}`, connector.ErrRateLimited, true},
		{"server error", 503, `{}`, connector.ErrUnavailable, true},
		{"bad request", 400, `{"errors":{"summary":"Summary is required."}}`, connector.ErrInvalidRequest, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, cfg := stub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})

			_, err := c.Execute(context.Background(), cfg, request(t, jira.ActionFileTicket.Generic(),
				issuetracker.NewFinding{TargetKey: "NHI", KindID: "1", Title: "s", Description: "d"}))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}

			var ce *connector.Error
			if !errors.As(err, &ce) {
				t.Fatalf("error was not classified: %v", err)
			}
			if ce.Message == "" {
				t.Error("classified error carried no user-facing message")
			}
			if ce.Retryable() != tc.retryable {
				t.Errorf("Retryable() = %v, want %v", ce.Retryable(), tc.retryable)
			}
		})
	}
}

func TestBadRequestPreservesFieldErrors(t *testing.T) {
	t.Parallel()

	c, cfg := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"errors":{"summary":"Summary is required."}}`))
	})

	_, err := c.Execute(context.Background(), cfg, request(t, jira.ActionFileTicket.Generic(),
		issuetracker.NewFinding{TargetKey: "NHI", KindID: "1", Title: "x", Description: "d"}))

	var ce *connector.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error was not classified: %v", err)
	}
	if ce.Fields["summary"] != "Summary is required." {
		t.Errorf("field errors = %v, want the summary reason preserved", ce.Fields)
	}
}

// Issue keys reach a query, so anything not a well-formed key must be dropped.
func TestGetIssuesRejectsMalformedKeys(t *testing.T) {
	t.Parallel()

	var body map[string]any
	c, cfg := stub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[]}`))
	})

	if _, err := c.Execute(context.Background(), cfg, request(t, jira.ActionFetchTickets.Generic(),
		map[string][]string{"keys": {
			"NHI-1",
			`NHI-1") or (project = "SECRET`,
			"not a key",
		}})); err != nil {
		t.Fatalf("fetch tickets: %v", err)
	}

	keys, _ := body["issueIdsOrKeys"].([]any)
	if len(keys) != 1 || keys[0] != "NHI-1" {
		t.Fatalf("requested keys = %v, want only the well-formed key", keys)
	}
}

// A configuration belonging to another connector must be refused before any
// request.
//
// This can only be tested through Execute. The typed methods take *jira.Config,
// so passing another connector's configuration to one of those is a compile
// error rather than something to check at runtime — which is the point of
// having the typed door at all.
func TestExecuteRejectsForeignConfiguration(t *testing.T) {
	t.Parallel()

	reached := false
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) { reached = true })

	_, err := c.Execute(context.Background(), foreignConfig{},
		connector.Request{Action: jira.ActionVerifyCredentials.Generic()})
	if !errors.Is(err, connector.ErrConfigInvalid) {
		t.Fatalf("got %v, want ErrConfigInvalid", err)
	}
	if reached {
		t.Error("a foreign configuration still reached the network")
	}
}

// Supports lets a caller ask in advance, without dispatching.
func TestSupportsReportsDeclaredActions(t *testing.T) {
	t.Parallel()

	c := jira.New(nil)
	if !connector.Supports(c, jira.ActionFileTicket.Generic()) {
		t.Error("file_ticket should be supported")
	}
	if connector.Supports(c, "delete_everything") {
		t.Error("delete_everything should not be supported")
	}
}
