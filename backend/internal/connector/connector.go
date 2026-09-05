// Package connector defines the port through which IdentityHub talks to an
// external service provider.
//
// This package is infrastructure, not domain. It knows how a connector is
// configured, dispatched, and how its failures are classified. It knows nothing
// about what any connector actually does.
//
// That boundary is deliberate. Jira has issues and projects, GitHub has
// repositories and pull requests, Okta has applications and grants. Those are
// three different domains, and a shared type that tried to cover them would
// either be a union nobody fully implements or a Jira model wearing a generic
// name. Each connector therefore owns its own vocabulary -- its actions, its
// parameter types, its results -- and this package moves opaque payloads
// between a caller and an implementation.
//
// What is genuinely common, and all that lives here:
//
//   - how a connector is identified and looked up (Type, Registry),
//   - what it needs in order to be configured at all (Field),
//   - how its stored configuration is decoded and validated (Config),
//   - how an operation is named and dispatched (Action, Request, Response),
//   - how a failure is classified so the HTTP layer can pick a status code and
//     Temporal can decide whether to retry (Error).
//
// What a caller wants from a connector is declared above this package, in
// internal/capabilities -- issuetracker is the one this product needs. The
// dependency runs one way: a capability may name connector, and nothing here
// may name a capability.
package connector

import (
	"context"
	"encoding/json"
)

// Type names a connector implementation. It is stored in
// connections.connector_type and is the key the registry resolves.
type Type string

// Action names an operation, as it travels between a caller and a connector.
//
// This package deliberately declares no action constants. Each connector
// defines its own Action type and its own set, and converts an incoming Action
// into that set at the start of Execute. A central list would grow to the union
// of every connector's operations while no single connector implements all of
// them, and every new integration would edit a shared file.
type Action string

// Requirements is what a connector needs to be configured, and how to get it.
type Requirements struct {
	// Fields are the values to ask for.
	Fields []Field `json:"fields"`

	// Setup is how to obtain them, in order.
	//
	// It lives here rather than in the interface because the interface cannot
	// know it. Finding the page that mints an Atlassian API token takes three
	// clicks through a menu nobody would guess, and the scopes are chosen on
	// that same screen -- a form that says "API token" and stops is a form
	// people abandon. Another connector's instructions are different and
	// equally unguessable, so each one carries its own.
	Setup []Step `json:"setup,omitempty"`
}

// Step is one instruction in obtaining a credential.
type Step struct {
	// Text is the instruction. It may name where to click.
	Text string `json:"text"`

	// URL, when the step is somewhere to go. Rendered as a link.
	URL string `json:"url,omitempty"`

	// LinkText labels the URL. Defaults to the URL when empty.
	LinkText string `json:"linkText,omitempty"`
}

// Field is one value a connector needs in its configuration.
//
// It describes the value well enough for an interface to ask a person for it,
// and no better: a name, what to call it, whether it is required, and whether
// it is a secret. It is deliberately not a schema language. A connector with
// requirements this cannot express is a reason to extend this, once, with the
// thing that connector actually needs.
type Field struct {
	// Name is the key in the configuration object, matching the connector's
	// own JSON tag for it.
	Name string `json:"name"`

	// Label is what to call the field in a form.
	Label string `json:"label"`

	// Kind tells an interface how to render and validate the input.
	Kind FieldKind `json:"kind"`

	// Required reports whether a configuration without it is rejected.
	Required bool `json:"required"`

	// Help explains where the value comes from. For a credential this is the
	// difference between a form someone can complete and one they abandon.
	Help string `json:"help,omitempty"`

	// Example is a shape, never a real value.
	Example string `json:"example,omitempty"`
}

// FieldKind is how a configuration value should be treated.
type FieldKind string

const (
	FieldText  FieldKind = "text"
	FieldURL   FieldKind = "url"
	FieldEmail FieldKind = "email"
	// FieldSecret must never be echoed back: not in a response, not in a log,
	// and not in the form after it is submitted.
	FieldSecret FieldKind = "secret"
)

// Request is one invocation of an action, for the uniform path.
//
// Params is JSON because this package cannot know the shape: parameters belong
// to the connector. Encoding and decoding them happens inside the connector,
// which is the only thing that knows what they are.
//
// Callers that know the connector do not use this. A Temporal activity, which
// must serialise its arguments anyway, does.
type Request struct {
	Action Action          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the result of an action, in the same opaque form.
type Response struct {
	Data json.RawMessage `json:"data,omitempty"`
}

// Connector is an integration with an external service provider.
//
// Implementations are stateless and shared: a configuration is passed to each
// call rather than held, so one registered instance serves every account and a
// credential lives no longer than the call that needs it.
type Connector interface {
	// Type reports which connector this is.
	Type() Type

	// DecodeConfig parses a decrypted configuration into the connector's own
	// type and validates it. Each connector owns this: only it knows what
	// fields it needs and what makes them valid.
	DecodeConfig(plaintext []byte) (Config, error)

	// Actions lists what this connector supports, for callers that need to
	// know in advance. Dispatch does not depend on it -- Execute rejects an
	// unknown action itself.
	Actions() []Action

	// Requirements describes what this connector needs to be configured, and
	// how to get it, so that an interface can ask without knowing which
	// connector it is asking about.
	//
	// Jira needs a site, an account email and an API token; GitHub would need
	// only a token; another provider needs something else again. Hard-coding
	// one connector's fields into a form -- or its setup instructions into a
	// help modal -- is how the second connector becomes a frontend change as
	// well as a backend one.
	//
	// This is metadata about the connector, like Type and Actions, and is
	// therefore on the interface rather than behind Execute: it has to be
	// answerable before any configuration exists.
	Requirements() Requirements

	// Execute performs an action using cfg.
	//
	// This is the uniform door: an action named by string, parameters and
	// results as JSON. It exists for callers that cannot know the connector at
	// compile time, chiefly Temporal activities.
	//
	// A connector also exposes typed methods for the same operations, and
	// Execute dispatches to them. There is one implementation behind both, so
	// they cannot drift, and a caller that knows the connector pays no
	// serialisation to reach it.
	Execute(ctx context.Context, cfg Config, req Request) (Response, error)
}

// Config is a validated connector configuration. Implementations are the
// credential-bearing structs each connector defines for itself.
type Config interface {
	// ConnectorType is the type this configuration belongs to. It is checked
	// against the row the configuration came from, so a blob written for one
	// connector cannot be decoded as another.
	ConnectorType() Type

	// Validate reports whether the configuration is usable. It runs both
	// before encryption and after decryption.
	Validate() error

	// Metadata returns the non-secret fields that are safe to store
	// unencrypted and show in a UI. It must never include a credential.
	Metadata() map[string]string
}

// Supports reports whether c declares an action.
//
// A connector rejects an unknown action itself, at the start of Execute. This
// is for callers that need to know in advance -- an interface deciding whether
// to offer an operation -- rather than for dispatch.
func Supports(c Connector, action Action) bool {
	if c == nil {
		return false
	}
	for _, a := range c.Actions() {
		if a == action {
			return true
		}
	}
	return false
}
