// Package issuetracker is the contract a connector implements in order to have
// findings filed into it.
//
// It sits above connector, not inside it. connector is the port: it moves
// opaque JSON between a caller and an implementation and knows nothing about
// what any connector does. This package is what the product asks for over that
// port -- the five operations IdentityHub needs from an issue tracker, and the
// shapes they exchange. A connector for something that is not an issue tracker
// implements none of it and is unaffected by it, which is why these five
// actions are not five more constants in connector.
//
// internal/capabilities is where it belongs: filing findings into an issue
// tracker is one capability a connector may have, and each capability names
// what the product needs of it. The dependency runs one way -- a capability may
// name connector, and nothing under connector may name a capability.
//
// The contract is action names and JSON shapes rather than a Go interface,
// because Execute is the boundary and Execute carries opaque JSON. Both sides
// declare their own structs and agree on the tags. A provider's model stays the
// provider's: Jira has projects and issue types, GitHub has repositories and
// labels, and neither is forced through the other's names. What crosses the
// boundary is the product's vocabulary, declared below.
//
//	verify_credentials  {}                                  -> Owner
//	list_targets        {query, limit}                      -> []Target
//	list_kinds          {targetKey}                         -> []Kind
//	file_ticket         {targetKey, kindId, title,          -> TicketRef
//	                     description, labels}
//	fetch_tickets       {keys}                              -> []TicketRef
//
// jira/issuetracker.go is the worked example, and jira/issuetracker_test.go
// pins the agreement by round-tripping the structs declared here.
package issuetracker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/dbiderman/identityhub/backend/internal/connector"
)

const (
	ActionIdentify     connector.Action = "verify_credentials"
	ActionListTargets  connector.Action = "list_targets"
	ActionListKinds    connector.Action = "list_kinds"
	ActionFileTicket   connector.Action = "file_ticket"
	ActionFetchTickets connector.Action = "fetch_tickets"
)

// Contract is the set a connector must implement in full. Partial support is
// not a usable issue tracker: an integration that can file a ticket but cannot
// list the projects to file it into cannot be driven by this product.
var Contract = []connector.Action{
	ActionIdentify, ActionListTargets, ActionListKinds, ActionFileTicket, ActionFetchTickets,
}

// Implements reports whether a connector satisfies the contract.
//
// Callers check this once, when a connection is configured, so that a connector
// which is not an issue tracker says so in those words rather than failing
// later with an unsupported action from inside a workflow.
func Implements(c connector.Connector) error {
	for _, action := range Contract {
		if !connector.Supports(c, action) {
			return connector.Errorf(connector.ErrUnsupportedAction,
				"A %s connection cannot file findings: it does not implement %s.",
				c.Type(), action)
		}
	}
	return nil
}

// TicketRef is what the product needs back after filing a finding: enough to
// record it, link to it, and show it in the recent tickets list.
type TicketRef struct {
	ExternalID  string    `json:"externalId"`
	Key         string    `json:"key"`
	URL         string    `json:"url"`
	Summary     string    `json:"summary"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	ContainerID string    `json:"containerId"` // the project, repository or equivalent
}

// Target is somewhere a finding can be filed: a Jira project, a GitHub
// repository, whatever the provider calls its container.
type Target struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Kind is a category of ticket a Target accepts -- a Jira issue type, a GitHub
// label. Providers that have no such notion return none.
type Kind struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Owner is who a credential belongs to, as the provider reports it. It is shown
// so a person can confirm they connected the account they meant to.
type Owner struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

// NewFinding is a finding on its way to becoming a ticket.
type NewFinding struct {
	TargetKey   string   `json:"targetKey"`
	KindID      string   `json:"kindId,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels,omitempty"`
}

type listTargetsParams struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type listKindsParams struct {
	TargetKey string `json:"targetKey"`
}

type fetchTicketsParams struct {
	Keys []string `json:"keys"`
}

// Client is our end of a connection to an issue tracker: a registered connector
// paired with the configuration it should act under.
//
// It is named for what it is rather than for what it talks to. The far end is
// the issue tracker; this is the handle, and it lives no longer than the
// request that decrypted the credential. The pairing exists because connectors
// are stateless and shared -- one registered instance serves every account --
// so a credential travels alongside a connector rather than inside it.
//
// It is a value, not an interface. There is nothing to implement and no second
// path to a connector: one is defined by connector.Connector, found in the
// Registry, and driven through Execute.
//
// Both fields stay exported because connections constructs them, but neither
// can leave this process by the two routes that would otherwise carry them: the
// tags close encoding/json, and LogValue below closes log/slog.
type Client struct {
	Connector connector.Connector `json:"-"`
	Config    connector.Config    `json:"-"`
}

// LogValue reports the only thing about a Client that is safe to say, which is
// which integration it is for.
//
// Config holds a decrypted credential, and Client is embedded in
// connections.Connection -- a value handlers hold and pass around. Logging one
// would otherwise reflect over it and print the credential with everything
// else. Implementing slog.LogValuer makes that structurally impossible rather
// than a rule somebody has to remember, and the method is promoted, so a logged
// Connection is covered by it too.
func (c Client) LogValue() slog.Value {
	if c.Connector == nil {
		return slog.StringValue("no connector")
	}
	return slog.StringValue(string(c.Connector.Type()))
}

// Identify confirms the credential works and reports who it belongs to.
//
// It is called once, when a connection is created, and may complete the
// configuration -- normalising what was typed, and discovering values only the
// provider can supply -- so the caller stores the configuration afterwards, not
// the one it passed in.
func (c Client) Identify(ctx context.Context) (Owner, error) {
	var owner Owner
	err := c.call(ctx, ActionIdentify, nil, &owner)
	return owner, err
}

// ListTargets returns the places a finding can be filed.
func (c Client) ListTargets(ctx context.Context, query string, limit int) ([]Target, error) {
	var targets []Target
	err := c.call(ctx, ActionListTargets, listTargetsParams{Query: query, Limit: limit}, &targets)
	return targets, err
}

// ListKinds returns the categories of ticket a target accepts.
func (c Client) ListKinds(ctx context.Context, targetKey string) ([]Kind, error) {
	var kinds []Kind
	err := c.call(ctx, ActionListKinds, listKindsParams{TargetKey: targetKey}, &kinds)
	return kinds, err
}

// FileTicket creates the ticket for a finding.
func (c Client) FileTicket(ctx context.Context, f NewFinding) (TicketRef, error) {
	var ref TicketRef
	err := c.call(ctx, ActionFileTicket, f, &ref)
	return ref, err
}

// FetchTickets reads back tickets this application filed, for live status.
func (c Client) FetchTickets(ctx context.Context, keys []string) ([]TicketRef, error) {
	var refs []TicketRef
	err := c.call(ctx, ActionFetchTickets, fetchTicketsParams{Keys: keys}, &refs)
	return refs, err
}

// call is the only place this application invokes a connector.
//
// Encode the product's request, hand it to Execute, decode the product's answer
// into out. Nothing here branches on which connector it is talking to, and
// nothing above it -- no handler, no Temporal activity -- knows either.
//
// out is a pointer, the same arrangement json.Unmarshal uses, so each operation
// above reads as three lines rather than repeating this one.
func (c Client) call(ctx context.Context, action connector.Action, params, out any) error {
	var encoded json.RawMessage
	if params != nil {
		var err error
		if encoded, err = json.Marshal(params); err != nil {
			return fmt.Errorf("encode the parameters for %s: %w", action, err)
		}
	}

	res, err := c.Connector.Execute(ctx, c.Config, connector.Request{Action: action, Params: encoded})
	if err != nil {
		return err
	}
	if len(res.Data) == 0 {
		return nil
	}

	if err := json.Unmarshal(res.Data, out); err != nil {
		// The connector answered in a shape this contract does not describe.
		// That is a defect in the connector, not something a caller can fix, so
		// it is reported as an unusable integration rather than as a provider
		// error.
		return connector.Errorf(connector.ErrConfigInvalid,
			"The %s integration answered %s in a shape this application does not understand.",
			c.Connector.Type(), action)
	}
	return nil
}
