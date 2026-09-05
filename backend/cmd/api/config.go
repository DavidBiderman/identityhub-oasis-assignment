package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dbiderman/identityhub/backend/internal/config"
)

// config is everything the API process reads from the environment.
type apiConfig struct {
	config.Core
	config.Secrets
	config.Temporal

	// RedisURL is where signed-out tokens are remembered until they expire.
	// Only the API needs it: the worker authenticates no requests.
	RedisURL string `env:"REDIS_URL,default=redis://localhost:6379/0"`

	Addr      string `env:"APP_ADDR,default=:8080"`
	PublicURL string `env:"APP_PUBLIC_URL,default=http://localhost:3000"`

	// Who issues access tokens, and who they are for. Both are checked on every
	// token: a valid signature on a token minted for a different application is
	// still one this application must reject.
	TokenIssuer   string `env:"TOKEN_ISSUER,default=https://identityhub.local"`
	TokenAudience string `env:"TOKEN_AUDIENCE,default=identityhub"`

	// TokenSigningKey signs and verifies access tokens, HMAC-SHA256.
	//
	// One key, because this application both issues and verifies. Federate
	// instead and this goes away: the provider signs with a private key and
	// this application verifies with a public one fetched from its JWKS.
	//
	// The default is published in this repository, which is why configuration
	// refuses it when APP_ENV=production -- the same rule as the database
	// password. Anyone holding this key can mint a token for any tenant.
	TokenSigningKey string `env:"TOKEN_SIGNING_KEY,default=dev-signing-key-not-for-production"`

	// TokenTTL is how long a token lasts. There is no refresh flow: renewing a
	// credential is the identity provider's side of the exchange.
	TokenTTL time.Duration `env:"TOKEN_TTL,default=1h"`

	// DigestProjectKey is where the scheduled blog digest files findings. It is
	// configuration because the digest runs unattended, with no user to ask.
	DigestProjectKey string `env:"DIGEST_PROJECT_KEY"`
}

// publishedSigningKey is the default in this repository, and therefore known to
// anyone who can read it.
const publishedSigningKey = "dev-signing-key-not-for-production"

// projectKeyPattern is the shape trackers use for a project key.
var projectKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// Validate reports every problem at once, so a misconfigured deployment is one
// fix rather than a sequence of restarts.
func (c apiConfig) Validate() error {
	errs := []error{c.Core.Validate(), c.Secrets.Validate(c.Env), c.Temporal.Validate()}

	if strings.TrimSpace(c.Addr) == "" {
		errs = append(errs, fmt.Errorf("APP_ADDR is required"))
	}

	if strings.TrimSpace(c.RedisURL) == "" {
		errs = append(errs, fmt.Errorf("REDIS_URL is required; it is where signing out is recorded"))
	}
	if strings.TrimSpace(c.TokenIssuer) == "" {
		errs = append(errs, fmt.Errorf("OIDC_ISSUER is required"))
	}
	if strings.TrimSpace(c.TokenAudience) == "" {
		errs = append(errs, fmt.Errorf("OIDC_AUDIENCE is required"))
	}

	switch {
	case strings.TrimSpace(c.TokenSigningKey) == "":
		errs = append(errs, fmt.Errorf("TOKEN_SIGNING_KEY is required; it signs every access token"))
	case c.TokenSigningKey == publishedSigningKey && c.Env.IsProduction():
		errs = append(errs, fmt.Errorf(
			"TOKEN_SIGNING_KEY is the default published in this repository and is not "+
				"permitted when APP_ENV=production; anyone holding it can mint a token "+
				"for any tenant"))
	case len(c.TokenSigningKey) < 32 && c.Env.IsProduction():
		errs = append(errs, fmt.Errorf(
			"TOKEN_SIGNING_KEY must be at least 32 characters when APP_ENV=production"))
	}

	if c.TokenTTL <= 0 {
		errs = append(errs, fmt.Errorf("TOKEN_TTL must be positive, got %s", c.TokenTTL))
	}

	// Set or unset are both fine -- unset turns the digest off -- but a value
	// no tracker would accept is a typo worth catching at startup rather than
	// at 3am in a scheduled run.
	if key := strings.TrimSpace(c.DigestProjectKey); key != "" && !projectKeyPattern.MatchString(key) {
		errs = append(errs, fmt.Errorf(
			"DIGEST_PROJECT_KEY must be uppercase letters, digits or underscores, "+
				"starting with a letter, for example NHI; got %q", key))
	}

	if c.Env.IsProduction() && !strings.HasPrefix(c.PublicURL, "https://") {
		errs = append(errs, fmt.Errorf("APP_PUBLIC_URL must be https when APP_ENV=production"))
	}

	return errors.Join(errs...)
}
