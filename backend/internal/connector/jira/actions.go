package jira

import (
	"context"
	"strings"

	jirav3 "github.com/ctreminiom/go-atlassian/v2/jira/v3"
	"github.com/ctreminiom/go-atlassian/v2/pkg/infra/models"

	"github.com/dbiderman/identityhub/backend/internal/connector"
)

// Action is the set of operations the Jira connector implements.
//
// It is this package's own type, not a shared enumeration. A connector's
// operations belong with the connector: a central list would grow to the union
// of every connector's actions while no single connector implements all of
// them, and every new integration would edit a shared file.
type Action connector.Action

// The five actions are the issue-tracker contract, which is written out in
// internal/capabilities/issuetracker. A connector is not obliged to implement it
// -- an identity provider being scanned would offer entirely different actions
// -- but a connector that wants findings filed into it implements these.
const (
	ActionVerifyCredentials Action = "verify_credentials"
	ActionListTargets       Action = "list_targets"
	ActionListKinds         Action = "list_kinds"
	ActionFileTicket        Action = "file_ticket"
	ActionFetchTickets      Action = "fetch_tickets"
)

// allActions is the single source of truth for what this connector supports.
// Actions and parseAction both derive from it, so the two cannot disagree, and
// the methods below are what each one runs.
var allActions = []Action{
	ActionVerifyCredentials,
	ActionListTargets,
	ActionListKinds,
	ActionFileTicket,
	ActionFetchTickets,
}

// Generic returns the action in its wire form.
func (a Action) Generic() connector.Action { return connector.Action(a) }

// Actions implements connector.Connector.
func (c *Connector) Actions() []connector.Action {
	out := make([]connector.Action, 0, len(allActions))
	for _, a := range allActions {
		out = append(out, a.Generic())
	}
	return out
}

// parseAction converts an incoming action into this connector's own set.
//
// The conversion from connector.Action is a type change that always compiles,
// so it proves nothing on its own; the membership check is what makes it a
// real cast. An action outside the set is rejected here, before any credential
// is used or any request is built.
func parseAction(a connector.Action) (Action, error) {
	candidate := Action(a)
	for _, known := range allActions {
		if candidate == known {
			return candidate, nil
		}
	}
	return "", connector.UnsupportedActionError(ConnectorType, a)
}

const (
	// defaultProjectLimit bounds the project picker.
	defaultProjectLimit = 50
	// maxIssueTypes bounds the issue types read for one project.
	maxIssueTypes = 100
)

// VerifyCredentials checks the credential works and reports whose it is.
func (c *Connector) VerifyCredentials(ctx context.Context, cfg *Config) (Account, error) {
	client, err := c.client(cfg)
	if err != nil {
		return Account{}, err
	}

	user, res, err := client.MySelf.Details(ctx, nil)
	if err != nil {
		return Account{}, classify(res, err)
	}
	return Account{
		AccountID:   user.AccountID,
		Email:       user.EmailAddress,
		DisplayName: user.DisplayName,
	}, nil
}

// ListProjects returns projects the credential can file into.
func (c *Connector) ListProjects(ctx context.Context, cfg *Config, params ListProjectsParams) ([]Project, error) {
	client, err := c.client(cfg)
	if err != nil {
		return nil, err
	}

	limit := params.Limit
	if limit <= 0 || limit > defaultProjectLimit {
		limit = defaultProjectLimit
	}

	// Most recently active first, so the picker opens on the projects a user is
	// most likely to want.
	page, res, err := client.Project.Search(ctx, &models.ProjectSearchOptionsScheme{
		OrderBy: "-lastIssueUpdatedTime",
		Query:   strings.TrimSpace(params.Query),
	}, 0, limit)
	if err != nil {
		return nil, classify(res, err)
	}

	projects := make([]Project, 0, len(page.Values))
	for _, p := range page.Values {
		projects = append(projects, Project{ID: p.ID, Key: p.Key, Name: p.Name})
	}
	return projects, nil
}

// ListIssueTypes returns the issue types a project accepts for creation.
func (c *Connector) ListIssueTypes(ctx context.Context, cfg *Config, params ListIssueTypesParams) ([]IssueType, error) {
	if strings.TrimSpace(params.ProjectKey) == "" {
		return nil, connector.Errorf(connector.ErrInvalidRequest, "A project key is required.")
	}

	client, err := c.client(cfg)
	if err != nil {
		return nil, err
	}

	out, err := c.creatableIssueTypes(ctx, client, params.ProjectKey)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, connector.Errorf(connector.ErrForbidden,
			"Project %s has no issue types the connected account can create.", params.ProjectKey)
	}
	return out, nil
}

// CreateIssue files a finding.
func (c *Connector) CreateIssue(ctx context.Context, cfg *Config, params CreateIssueParams) (Issue, error) {
	if strings.TrimSpace(params.ProjectKey) == "" || strings.TrimSpace(params.Summary) == "" {
		return Issue{}, connector.Errorf(connector.ErrInvalidRequest,
			"A project key and a summary are required.")
	}

	client, err := c.client(cfg)
	if err != nil {
		return Issue{}, err
	}

	issueTypeID := params.IssueTypeID
	if issueTypeID == "" {
		// Resolve a type rather than let Jira reject the create with an
		// unhelpful field error. Issue types are per project, so a hardcoded
		// "Task" breaks on team-managed projects and on any renamed type.
		if issueTypeID, err = c.defaultIssueType(ctx, client, params.ProjectKey); err != nil {
			return Issue{}, err
		}
	}

	// Jira Cloud represents a description as an Atlassian Document, not a
	// string; a plain string is rejected with a 400. Blank lines become
	// paragraphs, which is the whole of what a finding needs.
	description := &models.CommentNodeScheme{Type: "doc", Version: 1}
	for _, paragraph := range strings.Split(params.Description, "\n\n") {
		text := &models.CommentNodeScheme{Type: "paragraph"}
		text.AppendNode(&models.CommentNodeScheme{Type: "text", Text: paragraph})
		description.AppendNode(text)
	}

	payload := &models.IssueScheme{
		Fields: &models.IssueFieldsScheme{
			Project:     &models.ProjectScheme{Key: params.ProjectKey},
			IssueType:   &models.IssueTypeScheme{ID: issueTypeID},
			Summary:     params.Summary,
			Description: description,
			Labels:      params.Labels,
		},
	}

	created, res, err := client.Issue.Create(ctx, payload, nil)
	if err != nil {
		return Issue{}, classify(res, err)
	}

	return Issue{
		ID:      created.ID,
		Key:     created.Key,
		Summary: params.Summary,
		URL:     cfg.browseURL(created.Key),
	}, nil
}

// defaultIssueType picks the type to file a finding as, preferring a Task and
// otherwise taking the first creatable type.
func (c *Connector) defaultIssueType(ctx context.Context, client *jirav3.Client, projectKey string) (string, error) {
	types, err := c.creatableIssueTypes(ctx, client, projectKey)
	if err != nil {
		return "", err
	}
	for _, t := range types {
		if strings.EqualFold(t.Name, "Task") {
			return t.ID, nil
		}
	}
	if len(types) == 0 {
		return "", connector.Errorf(connector.ErrForbidden,
			"Project %s has no issue types the connected account can create.", projectKey)
	}
	return types[0].ID, nil
}

// creatableIssueTypes reads the issue types a project accepts for creation.
//
// This is the createmeta/{projectKey}/issuetypes endpoint, which the library
// surfaces as gjson rather than a typed struct. Subtasks are excluded: they
// cannot be created standalone, so offering them would produce a confusing
// failure at submit time.
func (c *Connector) creatableIssueTypes(ctx context.Context, client *jirav3.Client, projectKey string) ([]IssueType, error) {
	result, res, err := client.Issue.Metadata.FetchIssueMappings(ctx, projectKey, 0, maxIssueTypes)
	if err != nil {
		return nil, classify(res, err)
	}

	raw := result.Get("issueTypes").Array()
	types := make([]IssueType, 0, len(raw))
	for _, t := range raw {
		if t.Get("subtask").Bool() {
			continue
		}
		id, name := t.Get("id").String(), t.Get("name").String()
		if id == "" {
			continue
		}
		types = append(types, IssueType{ID: id, Name: name})
	}
	return types, nil
}

// GetIssues resolves issues by key. Keys that no longer exist are omitted
// rather than reported as an error: an issue deleted in Jira is a normal state
// for the recent tickets view, not a failure.
func (c *Connector) GetIssues(ctx context.Context, cfg *Config, params GetIssuesParams) ([]Issue, error) {
	keys := make([]string, 0, len(params.Keys))
	for _, k := range params.Keys {
		// Keys are shape-checked before they are sent. BulkFetch takes keys
		// rather than a query, so this is not an injection guard -- it just
		// keeps a malformed key from wasting a slot in the request.
		if isIssueKey(k) {
			keys = append(keys, k)
		}
	}
	// Nothing well formed to ask about, so nothing is asked.
	if len(keys) == 0 {
		return []Issue{}, nil
	}

	client, err := c.client(cfg)
	if err != nil {
		return nil, err
	}

	// BulkFetch resolves issues by key rather than by query. It replaced a JQL
	// search: cheaper, and with no string to interpolate into there is no
	// injection surface to reason about.
	page, res, err := client.Issue.Search.BulkFetch(ctx, keys, []string{"summary", "status", "created"})
	if err != nil {
		return nil, classify(res, err)
	}

	issues := make([]Issue, 0, len(page.Issues))
	for _, i := range page.Issues {
		issue := Issue{ID: i.ID, Key: i.Key, URL: cfg.browseURL(i.Key)}
		if i.Fields != nil {
			issue.Summary = i.Fields.Summary
			if i.Fields.Status != nil {
				issue.Status = i.Fields.Status.Name
			}
			issue.CreatedAt = jiraTimestamp(i.Fields.Created)
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// isIssueKey reports whether s looks like PROJ-123.
func isIssueKey(s string) bool {
	dash := strings.IndexByte(s, '-')
	if dash <= 0 || dash == len(s)-1 || len(s) > 64 {
		return false
	}
	for i, r := range s {
		switch {
		case i < dash && (r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'):
		case i == dash:
		case i > dash && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
