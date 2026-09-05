package jira_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dbiderman/identityhub/backend/internal/capabilities/issuetracker"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/connector/jira"
)

// The issue-tracker contract is a set of JSON shapes, not a Go interface, so
// nothing makes the two sides agree at compile time. These tests are what does:
// the product's own structs go in as parameters and come back out as results,
// and a renamed field on either side fails here rather than in production.
//
// This is a test-only dependency. The jira package does not import the contract
// package, and the contract package does not import jira -- which is the point
// of a contract written in JSON tags rather than in Go types.

func TestTheProductsFindingArrivesAsAJiraIssue(t *testing.T) {
	t.Parallel()

	var sent map[string]any
	c, cfg := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/issue" {
			_ = json.NewDecoder(r.Body).Decode(&sent)
			_, _ = w.Write([]byte(`{"id":"10001","key":"NHI-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"issueTypes":[{"id":"2","name":"Task","subtask":false}]}`))
	})

	finding := issuetracker.NewFinding{
		TargetKey:   "NHI",
		KindID:      "2",
		Title:       "Stale service account",
		Description: "Last used 400 days ago.",
		Labels:      []string{"identityhub"},
	}

	resp, err := c.Execute(context.Background(), cfg,
		request(t, jira.ActionFileTicket.Generic(), finding))
	if err != nil {
		t.Fatalf("file ticket: %v", err)
	}

	// Every field the product sent has to have landed somewhere in Jira's
	// request, under Jira's names.
	fields := sent["fields"].(map[string]any)
	if got := fields["project"].(map[string]any)["key"]; got != finding.TargetKey {
		t.Errorf("project key = %v, want %s", got, finding.TargetKey)
	}
	if got := fields["summary"]; got != finding.Title {
		t.Errorf("summary = %v, want %q", got, finding.Title)
	}
	if got := fields["issuetype"].(map[string]any)["id"]; got != finding.KindID {
		t.Errorf("issue type = %v, want %s", got, finding.KindID)
	}

	// The description is the body of the finding, and it is the field most
	// easily lost: Jira wants an Atlassian Document rather than a string, so it
	// is rebuilt rather than copied. The text has to survive that.
	document, err := json.Marshal(fields["description"])
	if err != nil {
		t.Fatalf("encode the description Jira was sent: %v", err)
	}
	if !strings.Contains(string(document), finding.Description) {
		t.Errorf("the description did not reach Jira: %s", document)
	}

	// Labels are how tickets filed by this application are found again, in Jira
	// and in the digest's duplicate check. Losing them is silent.
	labels, ok := fields["labels"].([]any)
	if !ok || len(labels) != len(finding.Labels) || labels[0] != finding.Labels[0] {
		t.Errorf("labels = %v, want %v", fields["labels"], finding.Labels)
	}

	// And the answer has to decode into the product's own type, populated.
	// Jira's create response carries only an id and a key, so a summary here
	// proves the connector filled it from the request rather than dropping it.
	ref := decode[issuetracker.TicketRef](t, resp)
	switch {
	case ref.Key != "NHI-1":
		t.Errorf("key = %q, want NHI-1", ref.Key)
	case ref.ExternalID != "10001":
		t.Errorf("externalId = %q, want 10001", ref.ExternalID)
	case ref.Summary != finding.Title:
		t.Errorf("summary = %q, want %q", ref.Summary, finding.Title)
	case ref.ContainerID != "NHI":
		t.Errorf("containerId = %q, want the target it was filed into", ref.ContainerID)
	case ref.URL == "":
		t.Error("url is empty; the recent tickets list has nothing to link to")
	}
}

func TestTheContractsReadActionsDecodeIntoTheProductsTypes(t *testing.T) {
	t.Parallel()

	// The stub answers /rest/api/3/myself itself, with the service account
	// every test in this package shares.
	c, cfg := stub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/project/search":
			_, _ = w.Write([]byte(`{"values":[{"id":"1","key":"NHI","name":"NHI Findings"}]}`))
		default:
			_, _ = w.Write([]byte(`{"issueTypes":[{"id":"2","name":"Task","subtask":false}]}`))
		}
	})
	ctx := context.Background()

	resp, err := c.Execute(ctx, cfg, connector.Request{Action: jira.ActionVerifyCredentials.Generic()})
	if err != nil {
		t.Fatalf("verify credentials: %v", err)
	}
	owner := decode[issuetracker.Owner](t, resp)
	if owner.ID != "5b10a" || owner.Email != "svc@example.com" || owner.DisplayName != "Service" {
		t.Errorf("owner = %+v, want the account the credential belongs to", owner)
	}

	resp, err = c.Execute(ctx, cfg, request(t, jira.ActionListTargets.Generic(),
		map[string]any{"query": "NHI", "limit": 10}))
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	if targets := decode[[]issuetracker.Target](t, resp); len(targets) != 1 || targets[0].Key != "NHI" {
		t.Errorf("targets = %+v, want the one project", targets)
	}

	resp, err = c.Execute(ctx, cfg, request(t, jira.ActionListKinds.Generic(),
		map[string]string{"targetKey": "NHI"}))
	if err != nil {
		t.Fatalf("list kinds: %v", err)
	}
	if kinds := decode[[]issuetracker.Kind](t, resp); len(kinds) != 1 || kinds[0].Name != "Task" {
		t.Errorf("kinds = %+v, want the one creatable issue type", kinds)
	}
}
