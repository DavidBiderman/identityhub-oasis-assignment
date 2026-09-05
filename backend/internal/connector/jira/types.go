package jira

import "time"

// The types below are the Jira connector's own vocabulary. They live here, not
// in the connector package, because they are Jira's model: another provider has
// repositories, or applications, or grants, and would describe them with its
// own types rather than being forced through these.

// Account identifies whose credential the connector is using. Surfacing it
// matters: a shared or departed employee's token is exactly the kind of
// non-human identity this product exists to find.
type Account struct {
	AccountID   string `json:"accountId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

// Project is a Jira project.
type Project struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// IssueType is a kind of issue a project accepts. Issue types are configured
// per project, so they are discovered rather than assumed.
type IssueType struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Issue is a Jira issue.
type Issue struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Summary   string    `json:"summary"`
	Status    string    `json:"status"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListProjectsParams filters the project list.
type ListProjectsParams struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// ListIssueTypesParams selects the project whose issue types are wanted.
type ListIssueTypesParams struct {
	ProjectKey string `json:"projectKey"`
}

// CreateIssueParams describes an issue to file.
type CreateIssueParams struct {
	ProjectKey  string   `json:"projectKey"`
	IssueTypeID string   `json:"issueTypeId,omitempty"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Labels      []string `json:"labels,omitempty"`
}

// GetIssuesParams selects issues by key.
type GetIssuesParams struct {
	Keys []string `json:"keys"`
}
