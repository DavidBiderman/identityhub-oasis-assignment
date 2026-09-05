package jira

import (
	"context"
	"time"
)

// How Jira satisfies the issue-tracker contract.
//
// The contract is five action names and the JSON shapes that go with them; it
// is written out in full in internal/capabilities/issuetracker. This file is the
// translation between those shapes and Jira's own model, and it is the only
// place in this package that knows the contract exists.
//
// The structs below are declared here rather than imported, because importing
// them would make this connector depend on the product it plugs into. They
// carry the contract's field tags and nothing else; the names on the Go side
// are Jira's business. tracker_test.go pins the agreement by decoding the
// product's own structs into these.

// filedTicket is the contract's TicketRef.
type filedTicket struct {
	ExternalID  string    `json:"externalId"`
	Key         string    `json:"key"`
	URL         string    `json:"url"`
	Summary     string    `json:"summary"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	ContainerID string    `json:"containerId"`
}

// target is the contract's Target. A Jira project is one.
type target struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// kind is the contract's Kind. A Jira issue type is one.
type kind struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// owner is the contract's Owner.
type owner struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

// The contract's parameter shapes.
type (
	listTargetsParams struct {
		Query string `json:"query,omitempty"`
		Limit int    `json:"limit,omitempty"`
	}
	listKindsParams struct {
		TargetKey string `json:"targetKey"`
	}
	fileTicketParams struct {
		TargetKey   string   `json:"targetKey"`
		KindID      string   `json:"kindId,omitempty"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Labels      []string `json:"labels,omitempty"`
	}
	fetchTicketsParams struct {
		Keys []string `json:"keys"`
	}
)

// resolveOwner completes the configuration and reports whose credential it is.
//
// Resolve rather than VerifyCredentials: connecting is when the API address is
// discovered, and cfg is a pointer so the caller stores the completed value.
func (c *Connector) resolveOwner(ctx context.Context, cfg *Config) (owner, error) {
	account, err := c.Resolve(ctx, cfg)
	if err != nil {
		return owner{}, err
	}
	return owner{
		ID:          account.AccountID,
		Email:       account.Email,
		DisplayName: account.DisplayName,
	}, nil
}

// listTargets reports the projects the credential can file into.
func (c *Connector) listTargets(ctx context.Context, cfg *Config, params listTargetsParams) ([]target, error) {
	projects, err := c.ListProjects(ctx, cfg, ListProjectsParams{Query: params.Query, Limit: params.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]target, 0, len(projects))
	for _, p := range projects {
		out = append(out, target{ID: p.ID, Key: p.Key, Name: p.Name})
	}
	return out, nil
}

// listKinds reports the issue types a project accepts.
func (c *Connector) listKinds(ctx context.Context, cfg *Config, params listKindsParams) ([]kind, error) {
	types, err := c.ListIssueTypes(ctx, cfg, ListIssueTypesParams{ProjectKey: params.TargetKey})
	if err != nil {
		return nil, err
	}
	out := make([]kind, 0, len(types))
	for _, t := range types {
		out = append(out, kind{ID: t.ID, Name: t.Name})
	}
	return out, nil
}

// fileTicket creates the issue for a finding.
func (c *Connector) fileTicket(ctx context.Context, cfg *Config, params fileTicketParams) (filedTicket, error) {
	issue, err := c.CreateIssue(ctx, cfg, CreateIssueParams{
		ProjectKey:  params.TargetKey,
		IssueTypeID: params.KindID,
		Summary:     params.Title,
		Description: params.Description,
		Labels:      params.Labels,
	})
	if err != nil {
		return filedTicket{}, err
	}
	return filedTicket{
		ExternalID: issue.ID,
		Key:        issue.Key,
		URL:        issue.URL,
		Summary:    issue.Summary,
		CreatedAt:  issue.CreatedAt,
		// The project the issue was filed into. Jira does not repeat it on the
		// create response, and the caller asked for it by key, so it comes
		// from the request.
		ContainerID: params.TargetKey,
	}, nil
}

// fetchTickets reads back issues by key, for live status.
func (c *Connector) fetchTickets(ctx context.Context, cfg *Config, params fetchTicketsParams) ([]filedTicket, error) {
	issues, err := c.GetIssues(ctx, cfg, GetIssuesParams{Keys: params.Keys})
	if err != nil {
		return nil, err
	}
	out := make([]filedTicket, 0, len(issues))
	for _, i := range issues {
		out = append(out, filedTicket{
			ExternalID: i.ID,
			Key:        i.Key,
			URL:        i.URL,
			Summary:    i.Summary,
			Status:     i.Status,
			CreatedAt:  i.CreatedAt,
		})
	}
	return out, nil
}
