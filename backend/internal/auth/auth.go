// Package auth answers one question: who is making this request, and what may
// they do?
//
// It does not authenticate anyone. There is no password here, no session, no
// credential of any kind for people — an identity provider does that, and this
// application reads the access token it issues.
//
//	Authorization: Bearer <token>  ──→  TokenResolver  ──→  Principal
//
// A TokenResolver verifies a token and maps its claims onto a Principal: the
// subject, the organization and account they act in, and the roles the provider
// asserts. Handlers depend on the Principal and never on how it arrived.
//
// The one credential this application does issue is an API key, for machine
// callers of the public REST endpoint. A scanner cannot perform an interactive
// authorization flow, so that case is handled here rather than by the provider.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dbiderman/identityhub/backend/internal/store"
)

// Role is a permission level asserted by the identity provider.
//
// The provider owns role assignment; this application only reads the claim and
// enforces it. Nothing here grants, revokes or stores a role.
type Role string

const (
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
)

// ActorType is store.ActorType, aliased so that this package can name it
// without defining a second vocabulary for one column: what kind of caller a
// request came from, which is recorded on every audit event.
type ActorType = store.ActorType

const (
	ActorUser   = store.ActorUser
	ActorAPIKey = store.ActorAPIKey
)

// ScopeFindingsWrite permits creating findings through the public API.
const ScopeFindingsWrite = "findings:write"

// Claims are what an access token asserts.
//
// The tenancy is *in the token*, put there by the provider when it was issued.
// It is never read from the request body, a query parameter or a header, so a
// caller cannot move itself between organizations or accounts: doing so would
// mean forging a token.
type Claims struct {
	Scope   store.Scope
	Subject string // the provider's stable identifier for the caller

	// TokenID is the token's `jti`, and ExpiresAt is its `exp`.
	//
	// They are the two values signing out needs: something to name the token
	// by, and the moment after which naming it stops mattering. Both are empty
	// for an API key, which is revoked by its own row instead.
	TokenID   string
	ExpiresAt time.Time

	Email       string
	DisplayName string
	Roles       []Role
	Scopes      []string // OAuth scopes, for machine callers
	ActorType   ActorType

	// ActorID is this application's own row for the caller: the users row for
	// a person, the api_keys row for a machine. It is what a ticket and an
	// audit event point at, so that "which credential filed this?" has an
	// answer -- the question this product exists to ask.
	//
	// For a person it is filled in by the middleware, which upserts the row
	// from the token. For an API key the lookup already found it.
	ActorID uuid.UUID
}

// Validate rejects claims that do not identify a complete tenancy.
func (c Claims) Validate() error {
	if err := c.Scope.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.Subject) == "" {
		return fmt.Errorf("the token asserts no subject")
	}
	return nil
}

// Principal is the authenticated caller as handlers see it.
//
// ActorID is this application's own row for the caller -- a users row for a
// person, an api_keys row for a machine -- present only so that a ticket or an
// audit event can point at something. Authorization never depends on it: every
// decision is made from the claims.
type Principal struct {
	Scope     store.Scope
	ActorType ActorType
	Subject   string
	ActorID   uuid.UUID
	Email     string
	Name      string
	Roles     []Role
	Scopes    []string

	// TokenID and ExpiresAt name the credential itself rather than the caller,
	// and only signing out uses them. They are empty for an API key.
	TokenID   string
	ExpiresAt time.Time
}

// HasRole reports whether the provider asserted a role.
func (p Principal) HasRole(role Role) bool {
	return slices.Contains(p.Roles, role)
}

// IsAdmin reports whether the caller may manage account-wide settings.
func (p Principal) IsAdmin() bool { return p.HasRole(RoleAdmin) }

// HasScope reports whether the caller carries an OAuth scope.
//
// Scopes exist to limit machine credentials, so a person is never constrained
// by one: a role decides what a person may do, and this application enforces
// roles rather than granting them. A key gets exactly the scopes it was issued
// with, which is one today -- and asking for only what is needed is the point,
// the same argument that applies to the Jira token this product asks a customer
// for. TestAuthorizationIsDecidedFromTheClaims covers both paths.
func (p Principal) HasScope(scope string) bool {
	if p.ActorType == ActorUser {
		return true
	}
	return slices.Contains(p.Scopes, scope)
}

// TokenResolver verifies the credential on a request and returns its claims.
//
// This is the only extension point for authentication. A real deployment
// replaces the development resolver with one that verifies signatures against
// the provider's keys; nothing else in the application changes, because nothing
// else sees the request.
type TokenResolver interface {
	// Name identifies the mechanism, for audit events and diagnostics.
	Name() string

	// Resolve returns ErrNoCredential when the request carries no credential
	// of the expected kind, which the middleware distinguishes from one that
	// was presented and rejected.
	Resolve(ctx context.Context, r *http.Request) (Claims, error)
}

var (
	// ErrNoCredential means the request presented no token.
	ErrNoCredential = errors.New("no credential was presented")

	// ErrInvalidCredentials means one was presented and rejected. Every
	// rejection returns this, whatever the cause: an expired token, a bad
	// signature and an unknown key must not be distinguishable by a caller.
	ErrInvalidCredentials = errors.New("the credential was not accepted")
)

// BearerToken extracts a token from an Authorization header.
func BearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// contextKey is unexported, so nothing outside this package can put a Principal
// into a request context. A handler can only receive one that middleware put
// there, which is what makes the tenancy on it trustworthy.
type contextKey struct{}

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

// PrincipalFromContext returns the authenticated caller. The boolean is false
// when no middleware authenticated the request, which is a wiring mistake
// rather than an anonymous caller.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}
