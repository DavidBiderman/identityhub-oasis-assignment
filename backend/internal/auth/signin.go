package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

// SignIn turns an email address and a password into an access token.
//
// This is the part a real deployment does not run. An organization that buys an
// NHI platform already has an identity provider, and asking it to keep a second
// password here would be asking it to take on a liability in the name of
// convenience. So in production this is Auth0, Clerk or Okta: they authenticate
// the person, they issue the token, and this application does what it does
// either way -- read it.
//
// It is implemented rather than stubbed because the exercise asks for login,
// and because a stub would demonstrate nothing. What it demonstrates is the
// shape: the token this mints is the same token the rest of the application
// reads, so replacing this with a provider changes this file and no other.
type SignIn struct {
	pool   *pgxpool.Pool
	tokens *Tokens
}

// NewSignIn returns a sign-in service.
func NewSignIn(pool *pgxpool.Pool, tokens *Tokens) *SignIn {
	return &SignIn{pool: pool, tokens: tokens}
}

// Session is a signed-in person: the token, and enough about who it was issued
// to for the audit trail to record the fact.
//
// A bearer token cannot tell us later who is holding it, so the moment it is
// handed out is the only moment that connection can be recorded.
type Session struct {
	Token   string
	TokenID string
	Subject string
	UserID  uuid.UUID
	Scope   store.Scope
}

// Authenticate verifies a password and returns a signed access token.
//
// Every failure returns ErrBadCredentials: an unknown address, a person with no
// password set, and a wrong password are one answer. Distinguishing them would
// turn the form into a way to ask who has an account here.
func (s *SignIn) Authenticate(ctx context.Context, email, password string) (Session, error) {
	email = strings.TrimSpace(email)
	if email == "" || password == "" {
		return Session{}, ErrBadCredentials
	}

	person, err := store.Read(s.pool).UserForSignIn(ctx, email)
	if err != nil {
		if errors.Is(store.MapError(err), store.ErrNotFound) {
			// Spend the time anyway. Returning early for an unknown address
			// would make it measurably faster than a wrong password, and that
			// difference is enough to enumerate who has an account. bcrypt
			// against a throwaway hash costs what a real comparison costs.
			_ = VerifyPassword(decoyHash, password)
			return Session{}, ErrBadCredentials
		}
		return Session{}, fmt.Errorf("look up %q to sign in: %w", email, err)
	}

	if len(person.PasswordHash) == 0 {
		_ = VerifyPassword(decoyHash, password)
		return Session{}, ErrBadCredentials
	}
	if err := VerifyPassword(person.PasswordHash, password); err != nil {
		return Session{}, ErrBadCredentials
	}

	roles := make([]Role, 0, len(person.Roles))
	for _, r := range person.Roles {
		switch Role(strings.ToLower(strings.TrimSpace(r))) {
		case RoleAdmin:
			roles = append(roles, RoleAdmin)
		case RoleMember:
			roles = append(roles, RoleMember)
		}
	}

	token, tokenID, err := s.tokens.Issue(Identity{
		Subject:     person.Subject,
		Email:       person.Email,
		DisplayName: person.DisplayName,
		OrgID:       person.OrgID.String(),
		AccountID:   person.AccountID.String(),
		Roles:       roles,
	})
	if err != nil {
		return Session{}, err
	}

	// Best effort, like the API key's. Somebody who signed in is signed in
	// whether or not we managed to record when.
	_ = store.InTx(ctx, s.pool, func(q *sqlcgen.Queries) error {
		return q.TouchUser(ctx, person.ID)
	})

	return Session{
		Token:   token,
		TokenID: tokenID,
		Subject: person.Subject,
		UserID:  person.ID,
		Scope:   store.Scope{OrgID: person.OrgID, AccountID: person.AccountID},
	}, nil
}

// decoyHash is a real bcrypt hash of a value nothing will ever present.
//
// It exists so that an unknown email address costs the same as a known one.
// Without it, "no such person" returns in microseconds and "wrong password"
// returns in the tens of milliseconds bcrypt deliberately takes -- a difference
// large enough to read over a network, and therefore a way to ask whether an
// address has an account here.
var decoyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// TokenTTL reports how long an issued token lasts, for callers that want to
// tell somebody when they will have to sign in again.
func (s *SignIn) TokenTTL() time.Duration { return s.tokens.ttl }
