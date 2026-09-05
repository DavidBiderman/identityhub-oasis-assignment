// The shapes this API accepts and returns.
//
// Only shapes. What checks them is in validate.go and what reports a failure is
// in errors.go, because a type that describes a request and a function that
// judges one are different jobs and change for different reasons.
package httpapi

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/dbiderman/identityhub/backend/internal/capabilities/issuetracker"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

// Response is the body of every API response.
//
// Exactly one of the two fields is set. A caller reads the HTTP status to know
// which, or simply checks whether Error is present -- one shape to parse,
// whether the caller is a browser or a CI pipeline.
type Response struct {
	Data  any      `json:"data,omitempty"`
	Error *Failure `json:"error,omitempty"`
}

// Failure describes what went wrong.
//
// Code is stable and machine-readable; Message is for a person, and is the
// message the failing error carried. Fields is set when the failure is about
// specific inputs, keyed by field name so a form can attach each reason to the
// input it names.
type Failure struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// --- Requests -----------------------------------------------------------
//
// These are the *external* request shapes: what a client may say.
//
// None has an organization or account field, and that is the point. Tenancy is
// not validated away from the request -- it is absent from the vocabulary a
// client can use. A body containing "accountId" is inert: there is nothing to
// decode it into, so it cannot be honoured whether or not anything checks for
// it. The property is the missing field, not a check that could be forgotten.

// ConnectRequest establishes an integration.
//
// Config is the connector's own configuration, opaque here: this layer does not
// know what a Jira credential looks like, and passes the object through for the
// connector to decode and validate.
type ConnectRequest struct {
	ConnectorType string          `json:"connectorType"`
	Config        json.RawMessage `json:"config"`
}

// CreateTicketRequest files a finding from the interface.
type CreateTicketRequest struct {
	ProjectKey  string `json:"projectKey"`
	IssueTypeID string `json:"issueTypeId,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// CreateFindingRequest files a finding through the public API.
//
// It is a separate type from CreateTicketRequest even though the fields
// overlap: the public API is a contract with external systems and should not
// change because the interface changed.
type CreateFindingRequest struct {
	ProjectKey  string   `json:"projectKey"`
	IssueTypeID string   `json:"issueTypeId,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels,omitempty"`
}

// CreateAPIKeyRequest mints a machine credential.
type CreateAPIKeyRequest struct {
	Name string `json:"name"`
}

// --- Responses ----------------------------------------------------------

// MeResponse is what the caller's token says about them.
type MeResponse struct {
	Subject   string   `json:"subject"`
	Email     string   `json:"email"`
	Name      string   `json:"name"`
	Roles     []string `json:"roles"`
	OrgID     string   `json:"orgId"`
	AccountID string   `json:"accountId"`
}

// FiledTicketResponse is a ticket as the filing endpoints answer with it.
//
// It carries one field the recent-tickets list does not: whether this request
// is what created the ticket. Replaying an Idempotency-Key returns the original
// ticket, which is a success -- the caller asked for the finding to exist and it
// does -- and the status code says so with 200 rather than 201. This says the
// same thing in the body, so a client does not have to read the status to find
// out, and an interface can tell somebody "already filed" rather than showing
// them a ticket they think they just made.
type FiledTicketResponse struct {
	TicketResponse
	Created bool `json:"created"`
}

// ConnectionListResponse is an account's integrations, and what could be added.
type ConnectionListResponse struct {
	Connections    []sqlcgen.ListConnectionsRow `json:"connections"`
	AvailableTypes []ConnectorTypeResponse      `json:"availableTypes"`
}

// ConnectorTypeResponse is a connector that could be added, and what it needs.
//
// Fields comes from the connector itself, so an interface can build the form
// without knowing which connector it is building it for: Jira needs a site, an
// email and a token; another provider needs a token and nothing else. Hardcoding
// one connector's three inputs into a form is how adding the second connector
// becomes a frontend change as well as a backend one.
type ConnectorTypeResponse struct {
	Type   connector.Type    `json:"type"`
	Fields []connector.Field `json:"fields"`

	// Setup is how to obtain the credential, in order. Finding the page that
	// mints an Atlassian API token is three clicks through a menu nobody would
	// guess, so the connector says where it is rather than leaving a form that
	// asks for a token and offers no way to get one.
	Setup []connector.Step `json:"setup,omitempty"`
}

// ConnectedResponse confirms a stored integration and whose credential it uses.
type ConnectedResponse struct {
	ID            uuid.UUID          `json:"id"`
	ConnectorType connector.Type     `json:"connectorType"`
	Owner         issuetracker.Owner `json:"owner"`
}

// ProjectListResponse is where findings can be filed.
type ProjectListResponse struct {
	Projects []issuetracker.Target `json:"projects"`
}

// IssueTypeListResponse is the kinds of ticket a project accepts.
type IssueTypeListResponse struct {
	IssueTypes []issuetracker.Kind `json:"issueTypes"`
}

// TicketResponse is a recorded ticket as the interface sees it.
type TicketResponse struct {
	ID         string    `json:"id"`
	ProjectKey string    `json:"projectKey"`
	IssueKey   string    `json:"issueKey"`
	IssueURL   string    `json:"issueUrl"`
	Title      string    `json:"title"`
	Source     string    `json:"source"`
	Status     string    `json:"status,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// TicketListResponse is the recent tickets list.
type TicketListResponse struct {
	Tickets []TicketResponse `json:"tickets"`
}

// PendingTicketResponse is returned when a ticket is still being filed.
//
// Filing is durable, so a slow provider does not fail the request: the workflow
// keeps retrying and the ticket appears in the list when it lands.
type PendingTicketResponse struct {
	Status     string `json:"status"`
	WorkflowID string `json:"workflowId"`
	Detail     string `json:"detail"`
}

// DigestResponse is the state of an account's recurring blog digest.
//
// The digest is external to the interface by design. It is readable here
// because a scheduled feature that only proves itself a day later is one nobody
// checks -- and "does it work?" is a fair question to be able to answer without
// opening Temporal.
type DigestResponse struct {
	Scheduled  bool       `json:"scheduled"`
	Paused     bool       `json:"paused"`
	ProjectKey string     `json:"projectKey,omitempty"`
	NextRunAt  *time.Time `json:"nextRunAt"`
	LastRunAt  *time.Time `json:"lastRunAt"`
	EveryHours int        `json:"everyHours"`
}

// APIKeyListResponse is an account's machine credentials, without any secret.
type APIKeyListResponse struct {
	APIKeys []sqlcgen.ListAPIKeysRow `json:"apiKeys"`
}

// CreatedAPIKeyResponse carries the only plaintext copy of a new key.
type CreatedAPIKeyResponse struct {
	APIKey sqlcgen.CreateAPIKeyRow `json:"apiKey"`
	Secret string                  `json:"secret"`
	Notice string                  `json:"notice"`
}

// AuditListResponse is the account's recent security-relevant actions.
type AuditListResponse struct {
	Events []sqlcgen.RecentAuditEventsRow `json:"events"`
}

// SignInRequest is an email address and a password.
type SignInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// SignInResponse is the access token, in the shape an OAuth token endpoint
// answers with -- so that swapping this endpoint for a real provider's is a
// change of address rather than a change of client.
type SignInResponse struct {
	AccessToken string `json:"accessToken"`
	TokenType   string `json:"tokenType"`
	ExpiresIn   int    `json:"expiresIn"`
}
