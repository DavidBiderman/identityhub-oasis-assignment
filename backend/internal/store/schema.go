package store

// The values this application stores in constrained columns.
//
// They live next to the schema because the schema is what constrains them: a
// check constraint on audit_events.actor_type. Naming them once is what stops
// "api_key" and "apikey" both appearing in a column a query filters on.
//
// Connection statuses are deliberately absent. They appear only inside the SQL
// -- status = 'active', status in ('active','invalid') -- where a Go constant
// cannot be substituted anyway, so a second copy here would name values nothing
// reads. The check constraint on the column is the guard.

// How a ticket came to be created. Recording this lets the interface
// distinguish a ticket a person filed from one a scanner or the digest filed.
const (
	SourceUI         = "ui"
	SourceAPI        = "api"
	SourceAutomation = "automation"
)

// ActorType is what kind of caller did something: a person, a machine
// credential, or this application acting on its own schedule.
type ActorType string

// Actor kinds, matching the check constraint on audit_events.
const (
	ActorUser   ActorType = "user"
	ActorAPIKey ActorType = "api_key"
	ActorSystem ActorType = "system"
)

// Audit actions. The set is small and closed on purpose: an audit trail is only
// useful if the vocabulary is stable enough to query.
const (
	ActionJiraConnected     = "jira.connected"
	ActionJiraDisconnected  = "jira.disconnected"
	ActionJiraCredentialBad = "jira.credential_rejected"
	ActionUserSignedIn      = "user.signed_in"
	ActionUserSignedOut     = "user.signed_out"
	ActionAPIKeyCreated     = "api_key.created"
	ActionAPIKeyRevoked     = "api_key.revoked"
	ActionTicketCreated     = "ticket.created"
	ActionTicketFailed      = "ticket.failed"
	ActionDigestRan         = "digest.ran"
	ActionDigestFailed      = "digest.failed"
)
