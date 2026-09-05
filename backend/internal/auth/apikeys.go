package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

// apiKeyPrefix marks IdentityHub API keys so that one is recognisable on sight
// — in a log, a CI configuration, or a secret scanner's rules.
const apiKeyPrefix = "ih"

// APIKeys issues and validates credentials for machine callers.
//
// This is the one credential this application issues. A scanner or a CI job
// cannot perform an interactive authorization flow, so the public REST endpoint
// takes a key rather than a provider-issued token. People never see one.
type APIKeys struct {
	pool *pgxpool.Pool
}

// NewAPIKeys returns an API key service.
func NewAPIKeys(pool *pgxpool.Pool) *APIKeys { return &APIKeys{pool: pool} }

// Issue mints a key and returns its single plaintext copy, which the caller
// must show once and then discard. Only a hash is stored.
func (a *APIKeys) Issue(ctx context.Context, p Principal, name string) (string, sqlcgen.CreateAPIKeyRow, error) {
	var none sqlcgen.CreateAPIKeyRow

	if name = strings.TrimSpace(name); name == "" {
		return "", none, fmt.Errorf("an API key needs a name")
	}

	keyID, secret, err := newKey()
	if err != nil {
		return "", none, err
	}

	var created sqlcgen.CreateAPIKeyRow
	err = store.InTx(ctx, a.pool, func(q *sqlcgen.Queries) error {
		var err error
		created, err = q.CreateAPIKey(ctx, sqlcgen.CreateAPIKeyParams{
			OrgID:      p.Scope.OrgID,
			AccountID:  p.Scope.AccountID,
			KeyID:      keyID,
			SecretHash: hashSecret(secret),
			Name:       name,
			Scopes:     []string{ScopeFindingsWrite},
			CreatedBy:  &p.ActorID,
		})
		return err
	})
	if err != nil {
		return "", none, fmt.Errorf("store the new api key %q: %w", name, err)
	}
	return apiKeyPrefix + "_" + keyID + "_" + secret, created, nil
}

// Authenticate resolves a presented API key to claims.
//
// The lookup is deliberately not scoped: the tenancy is what it discovers.
//
// A revoked key, an unknown key ID and a wrong secret all fail identically, so
// a response cannot be used to confirm that a key exists.
func (a *APIKeys) Authenticate(ctx context.Context, presented string) (Claims, error) {
	keyID, secret, ok := splitAPIKey(presented)
	if !ok {
		return Claims{}, ErrInvalidCredentials
	}

	record, err := store.Read(a.pool).FindAPIKey(ctx, keyID)
	if err != nil {
		return Claims{}, ErrInvalidCredentials
	}
	if subtle.ConstantTimeCompare(hashSecret(secret), record.SecretHash) != 1 {
		return Claims{}, ErrInvalidCredentials
	}
	if record.Revoked {
		return Claims{}, ErrInvalidCredentials
	}

	// This lookup is the one place a tenancy is discovered rather than supplied,
	// so the pair is assembled here and travels as a Scope from now on.
	scope := store.Scope{OrgID: record.OrgID, AccountID: record.AccountID}

	// Best effort: a key that authenticated is still valid even if we cannot
	// record that it was used. Knowing when a machine credential was last
	// exercised is how an unused one gets noticed and retired.
	_ = store.InTx(ctx, a.pool, func(q *sqlcgen.Queries) error {
		return q.TouchAPIKey(ctx, sqlcgen.TouchAPIKeyParams{
			ID: record.ID, OrgID: scope.OrgID, AccountID: scope.AccountID,
		})
	})

	claims := Claims{
		Scope:     scope,
		Subject:   apiKeyPrefix + "_" + keyID,
		Scopes:    record.Scopes,
		ActorType: ActorAPIKey,
		// The key's own row. Without it a ticket filed by a machine records no
		// actor at all, and an account with ten CI keys cannot say which one
		// filed what.
		ActorID: record.ID,
	}
	if err := claims.Validate(); err != nil {
		return Claims{}, ErrInvalidCredentials
	}
	return claims, nil
}

// APIKeyResolver authenticates machine callers of the public REST endpoint.
type APIKeyResolver struct{ keys *APIKeys }

// NewAPIKeyResolver returns a resolver backed by keys.
func NewAPIKeyResolver(keys *APIKeys) *APIKeyResolver { return &APIKeyResolver{keys: keys} }

// Name implements TokenResolver.
func (r *APIKeyResolver) Name() string { return "api_key" }

// Resolve implements TokenResolver.
func (r *APIKeyResolver) Resolve(ctx context.Context, req *http.Request) (Claims, error) {
	presented, ok := BearerToken(req)
	if !ok {
		return Claims{}, ErrNoCredential
	}
	return r.keys.Authenticate(ctx, presented)
}

// newKey returns the public identifier and the secret half of an API key.
//
// The identifier is hex so that it cannot contain the separator the key format
// uses; the secret is 256 bits from crypto/rand.
func newKey() (keyID, secret string, err error) {
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return "", "", fmt.Errorf("generate a key id: %w", err)
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", "", fmt.Errorf("generate a key secret: %w", err)
	}
	return hex.EncodeToString(idBytes), base64.RawURLEncoding.EncodeToString(secretBytes), nil
}

// hashSecret returns the value stored in place of a secret.
//
// SHA-256 rather than a password hash is deliberate: this value is 256 bits
// from a CSPRNG, so there is no dictionary to attack and no work factor worth
// paying on every request.
func hashSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// splitAPIKey parses "ih_<keyid>_<secret>".
//
// The split takes three parts and stops, so the secret may contain underscores
// — which matters, because base64url includes one. The key ID is hex and never
// does.
func splitAPIKey(s string) (keyID, secret string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(s), "_", 3)
	if len(parts) != 3 || parts[0] != apiKeyPrefix || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// Compile-time proof that both mechanisms satisfy the one seam.
var (
	_ TokenResolver = (*APIKeyResolver)(nil)
	_ TokenResolver = (*Tokens)(nil)
)
