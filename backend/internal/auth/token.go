package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/dbiderman/identityhub/backend/internal/store"
)

// leeway tolerates a small clock difference between whoever issued a token and
// whoever reads it.
//
// Zero would be stricter and wrong: two machines are never exactly in step, and
// a token rejected because the issuer's clock is a second ahead is a sign-in
// that fails for no reason a person can act on. Thirty seconds is far below the
// token's own lifetime, so it widens nothing that matters.
const leeway = 30 * time.Second

// Tokens issues and verifies the access tokens people carry.
//
// One type for both directions on purpose: they share a signing key, and a
// design where minting and checking can disagree about the key, the algorithm
// or the claim names is a design with a hole in it. Here they cannot.
//
// The JWT work is golang-jwt's, not ours. Hand-rolling it is a page of
// base64, HMAC and expiry arithmetic that a maintained library already has --
// and the places it is easy to get subtly wrong are the places an attacker
// looks. What is ours is the configuration, which is where the security
// decisions actually live: see verify.
//
// A real deployment federates -- Auth0, Clerk, Okta -- and then the asymmetry
// matters: the provider signs with a private key and this application verifies
// with a public one from the provider's JWKS. That is a different key function
// passed to the same parser, which is the point of using one.
type Tokens struct {
	key      []byte
	issuer   string
	audience string
	ttl      time.Duration
	parser   *jwt.Parser
}

// NewTokens returns a token service signing with key.
func NewTokens(key []byte, issuer, audience string, ttl time.Duration) *Tokens {
	return &Tokens{
		key: key, issuer: issuer, audience: audience, ttl: ttl,
		parser: jwt.NewParser(parserOptions(issuer, audience)...),
	}
}

// parserOptions are every check this application makes on a token beyond its
// signature. They are options rather than code because the library already
// implements each one correctly, and because listing them here makes the policy
// readable in one place.
func parserOptions(issuer, audience string) []jwt.ParserOption {
	options := []jwt.ParserOption{
		// THE important one. Without it the parser believes the token's own
		// header about how it was signed, which is a verifier a caller can talk
		// out of verifying: the "alg": "none" forgery, and the RS256-to-HS256
		// confusion where an attacker signs with the public key. Pinning the
		// method means the header is not something a caller gets to negotiate.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),

		// Expiry is the revocation that needs no state, so a token without one
		// would be a credential that never stops working. The library treats
		// exp as optional; this application does not.
		jwt.WithExpirationRequired(),

		jwt.WithLeeway(leeway),
	}

	// Only when configured. An empty setting means "not configured", not
	// "accept anything" -- but a configured one is enforced, because a valid
	// signature on a token minted for another application is still one to
	// refuse.
	if issuer != "" {
		options = append(options, jwt.WithIssuer(issuer))
	}
	if audience != "" {
		options = append(options, jwt.WithAudience(audience))
	}
	return options
}

// Name implements TokenResolver.
func (t *Tokens) Name() string { return "access_token" }

// Identity is a person to issue a token for.
type Identity struct {
	Subject     string
	Email       string
	DisplayName string
	OrgID       string
	AccountID   string
	Roles       []Role
}

// tokenClaims is the payload of an access token.
//
// RegisteredClaims carries the standard set -- sub, iss, aud, exp, nbf, iat,
// jti -- in the library's own types, which is what makes `aud` work whether a
// provider emits a string or an array. RFC 7519 permits both; reading it as a
// plain string, as an earlier version did, would have rejected valid tokens from
// a real provider.
//
// The three above it are the custom claims a deployment configures its provider
// to include. Reading them is what this application does either way; only who
// signed the token changes.
type tokenClaims struct {
	OrgID     string   `json:"org_id"`
	AccountID string   `json:"account_id"`
	Roles     []string `json:"roles,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	Email     string   `json:"email,omitempty"`
	Name      string   `json:"name,omitempty"`

	jwt.RegisteredClaims
}

// Issue returns a signed access token asserting id, and the jti it carries.
//
// The jti comes back so the caller can record which token it handed out. A
// bearer token cannot be asked later who is holding it, so that connection has
// to be made at the moment of issue or not at all.
func (t *Tokens) Issue(id Identity) (token, tokenID string, err error) {
	roles := make([]string, 0, len(id.Roles))
	for _, r := range id.Roles {
		roles = append(roles, string(r))
	}

	tokenID = uuid.NewString()
	now := time.Now()

	claims := tokenClaims{
		OrgID:     id.OrgID,
		AccountID: id.AccountID,
		Roles:     roles,
		Email:     id.Email,
		Name:      id.DisplayName,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        tokenID,
			Subject:   id.Subject,
			Issuer:    t.issuer,
			Audience:  jwt.ClaimStrings{t.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.key)
	if err != nil {
		return "", "", fmt.Errorf("sign an access token: %w", err)
	}
	return signed, tokenID, nil
}

// Resolve implements TokenResolver.
func (t *Tokens) Resolve(_ context.Context, r *http.Request) (Claims, error) {
	raw, ok := BearerToken(r)
	if !ok {
		return Claims{}, ErrNoCredential
	}

	payload, err := t.verify(raw)
	if err != nil {
		return Claims{}, ErrInvalidCredentials
	}

	claims, err := claimsFrom(payload)
	if err != nil {
		return Claims{}, ErrInvalidCredentials
	}

	// A token whose claims do not identify a complete tenancy is a bad token,
	// not a bug in this resolver. Checking it here means it is rejected as a
	// credential -- a 401 -- rather than escaping to the middleware, which
	// treats incomplete claims as a programming error and returns a 500.
	if err := claims.Validate(); err != nil {
		return Claims{}, ErrInvalidCredentials
	}
	return claims, nil
}

// verify checks a token's signature and everything a valid signature does not
// tell you.
//
// The parser does both, in that order, with the policy from parserOptions.
// Nothing in the payload is trusted until the signature verifies, because until
// then the payload is a string a caller sent -- and the key function below
// cannot be talked into a different algorithm, because WithValidMethods has
// already rejected anything but HS256 by the time it is called.
func (t *Tokens) verify(raw string) (tokenClaims, error) {
	var claims tokenClaims
	_, err := t.parser.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) {
		return t.key, nil
	})
	if err != nil {
		return tokenClaims{}, fmt.Errorf("the token was not accepted: %w", err)
	}
	return claims, nil
}

// claimsFrom maps token claims onto a tenancy.
//
// This is the whole of what the application takes from a token, and it is
// deliberately narrow: two identifiers, a subject, some roles. Anything else in
// the token is ignored.
func claimsFrom(t tokenClaims) (Claims, error) {
	orgID, err := uuid.Parse(t.OrgID)
	if err != nil {
		return Claims{}, fmt.Errorf("the org_id claim %q is not a UUID: %w", t.OrgID, err)
	}
	accountID, err := uuid.Parse(t.AccountID)
	if err != nil {
		return Claims{}, fmt.Errorf("the account_id claim %q is not a UUID: %w", t.AccountID, err)
	}

	roles := make([]Role, 0, len(t.Roles))
	for _, r := range t.Roles {
		// Unrecognised roles are dropped rather than rejected: a provider may
		// legitimately assert roles that mean nothing to this application, and
		// refusing the token would be the wrong response to that.
		switch Role(strings.ToLower(strings.TrimSpace(r))) {
		case RoleAdmin:
			roles = append(roles, RoleAdmin)
		case RoleMember:
			roles = append(roles, RoleMember)
		}
	}

	var expiresAt time.Time
	if t.ExpiresAt != nil {
		expiresAt = t.ExpiresAt.Time
	}

	return Claims{
		Scope:       store.Scope{OrgID: orgID, AccountID: accountID},
		TokenID:     t.ID,
		ExpiresAt:   expiresAt,
		Subject:     t.Subject,
		Email:       t.Email,
		DisplayName: t.Name,
		Roles:       roles,
		Scopes:      strings.Fields(t.Scope),
		ActorType:   ActorUser,
	}, nil
}
