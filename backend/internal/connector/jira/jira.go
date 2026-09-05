// Package jira implements the connector port for Atlassian Jira Cloud.
//
// The Jira REST API is reached through github.com/ctreminiom/go-atlassian,
// which is the actively maintained Go client for Atlassian Cloud. Atlassian
// publishes no official Go SDK; hand-writing endpoint definitions and the
// Atlassian Document Format would be re-implementing a maintained library.
//
// What this package still owns is everything the library cannot decide for us:
// which operations the product exposes, how upstream failures are classified so
// the HTTP layer can pick a status code and Temporal can decide whether to
// retry, and what a Jira credential looks like.
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	jirav3 "github.com/ctreminiom/go-atlassian/v2/jira/v3"

	"github.com/dbiderman/identityhub/backend/internal/connector"
)

// requestTimeout bounds every call. Without it a hung Jira site would hold a
// request goroutine, and a Temporal activity, indefinitely.
const requestTimeout = 20 * time.Second

// Connector is the Jira implementation of connector.Connector.
//
// It is stateless: a configuration is supplied per call rather than held, so
// one instance serves every account and a credential lives no longer than the
// call that needs it.
type Connector struct {
	// httpClient is shared across accounts. It carries no credential; the
	// per-request Authorization header comes from the configuration.
	httpClient *http.Client

	// gatewayHost overrides Atlassian's API gateway, for tests.
	gatewayHost string

	// log is optional; when set, configuration resolution is recorded.
	log *slog.Logger
}

// New returns a Jira connector using httpClient for every outbound call.
//
// The client is supplied rather than built here. It is a shared resource with
// a connection pool and a timeout, and a process should own exactly one of
// those and hand it down -- not discover that a package created its own.
// Passing nil falls back to a client with a bounded timeout, so a test or a
// one-off does not have to construct one.
func New(httpClient *http.Client, opts ...Option) *Connector {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	c := &Connector{httpClient: httpClient}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Option customises a Connector.
type Option func(*Connector)

// WithGatewayHost overrides Atlassian's API gateway address, so that a test can
// serve both entry points from one server.
func WithGatewayHost(host string) Option {
	return func(c *Connector) { c.gatewayHost = host }
}

// WithLogger records which Atlassian entry point a credential resolved to.
func WithLogger(log *slog.Logger) Option {
	return func(c *Connector) { c.log = log }
}

// Register adds the Jira connector to a registry.
func Register(r *connector.Registry, httpClient *http.Client, opts ...Option) {
	r.Register(New(httpClient, opts...))
}

// Compile-time proof that the adapter satisfies the port.
var _ connector.Connector = (*Connector)(nil)

// Type implements connector.Connector.
func (c *Connector) Type() connector.Type { return ConnectorType }

// DecodeConfig implements connector.Connector.
func (c *Connector) DecodeConfig(plaintext []byte) (connector.Config, error) {
	cfg := &Config{}
	if err := connector.StrictDecode(plaintext, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Execute implements connector.Connector.
//
// This is the only door. Everything above this package -- handlers, Temporal
// activities, the digest -- reaches a connector through here, so none of them
// links a connector type and none of them branches on one.
//
// It is a translation layer and nothing else: parse the action, decode the
// parameters, call the typed method that does the work, encode the result. The
// decoding and encoding happen inside the connector because this is the only
// place that knows what an action's parameters and results are; the shared
// package carries them as opaque JSON and never looks inside.
func (c *Connector) Execute(ctx context.Context, cfg connector.Config, req connector.Request) (connector.Response, error) {
	// Resolve the action first. An unsupported one is rejected before the
	// configuration is touched, so a bad request never reaches a credential.
	action, err := parseAction(req.Action)
	if err != nil {
		return connector.Response{}, err
	}

	// The registry decoded into the type this package supplied, so this
	// assertion cannot fail in practice. It is checked rather than asserted
	// because an unchecked assertion would panic inside a request.
	jiraCfg, ok := cfg.(*Config)
	if !ok {
		return connector.Response{}, connector.Errorf(connector.ErrConfigInvalid,
			"The stored configuration is not a Jira configuration. Reconnect the integration to continue.")
	}

	switch action {
	case ActionVerifyCredentials:
		return result(c.resolveOwner(ctx, jiraCfg))

	case ActionListTargets:
		params, err := decode[listTargetsParams](req.Params)
		if err != nil {
			return connector.Response{}, err
		}
		return result(c.listTargets(ctx, jiraCfg, params))

	case ActionListKinds:
		params, err := decode[listKindsParams](req.Params)
		if err != nil {
			return connector.Response{}, err
		}
		return result(c.listKinds(ctx, jiraCfg, params))

	case ActionFileTicket:
		params, err := decode[fileTicketParams](req.Params)
		if err != nil {
			return connector.Response{}, err
		}
		return result(c.fileTicket(ctx, jiraCfg, params))

	case ActionFetchTickets:
		params, err := decode[fetchTicketsParams](req.Params)
		if err != nil {
			return connector.Response{}, err
		}
		return result(c.fetchTickets(ctx, jiraCfg, params))

	default:
		// Unreachable: parseAction accepts exactly the actions above. The case
		// exists so that adding a constant without a branch fails loudly here
		// rather than silently returning an empty response.
		return connector.Response{}, connector.UnsupportedActionError(ConnectorType, req.Action)
	}
}

// decode reads an action's parameters. Absent parameters are the zero value,
// which is what an action with no arguments sends.
func decode[T any](raw json.RawMessage) (T, error) {
	var params T
	if len(raw) == 0 {
		return params, nil
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return params, connector.Errorf(connector.ErrInvalidRequest,
			"The parameters for this action could not be read.")
	}
	return params, nil
}

// result encodes an action's outcome, passing a failure through untouched so
// that its classification survives.
func result(v any, err error) (connector.Response, error) {
	if err != nil {
		return connector.Response{}, err
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return connector.Response{}, fmt.Errorf("encode a %T result: %w", v, err)
	}
	return connector.Response{Data: encoded}, nil
}

// client builds a Jira API client for one configuration.
//
// The address comes from the configuration, resolved when the connection was
// created. Construction is a URL parse and some struct wiring -- no connection
// and no I/O -- so building one per call costs nothing and lets the connector
// stay stateless.
func (c *Connector) client(cfg *Config) (*jirav3.Client, error) {
	base, err := cfg.baseURL()
	if err != nil {
		return nil, err
	}

	client, err := jirav3.New(c.httpClient, base)
	if err != nil {
		return nil, connector.Errorf(connector.ErrInvalidRequest,
			"The configured Jira site URL is not valid.")
	}
	// Jira Cloud expects the account email and API token as HTTP Basic.
	client.Auth.SetBasicAuth(strings.TrimSpace(cfg.Email), strings.TrimSpace(cfg.APIToken))
	return client, nil
}
