package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client is the part of go-redis this package uses.
//
// An interface rather than *redis.Client so that the dependency points the
// right way: this package declares what it needs, and the binary satisfies it.
// It is also the whole surface a test would have to stand in for.
type Client interface {
	Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd
	Exists(ctx context.Context, keys ...string) *redis.IntCmd
	Ping(ctx context.Context) *redis.StatusCmd
}

// Revocations remembers which tokens were signed out.
//
// An access token is self-contained. Nothing about it is written when it is
// issued -- there is no session, and that is the design -- so the only thing
// that can stop one before its own expiry is a record saying it is no longer
// welcome. This is that record, and it is the whole of what "sign out" means
// on the server side.
//
// It is deliberately not in Postgres. The record has exactly one property that
// matters: it must live as long as the token would have, and then stop
// existing. After that moment the token is refused for being expired anyway,
// so the record has nothing left to say. A key with a deadline is what Redis
// is, and expressing the deadline as the storage engine's own TTL removes the
// question of who deletes it and when.
//
// The second reason is where the read happens: every authenticated request
// asks this question, so it belongs on a lookup built for that rather than on
// a connection the request may not otherwise need.
//
// The client is handed in, not built here. Connecting to Redis -- the address,
// the timeouts, how long to wait at startup -- is a deployment decision, and
// this package has no business holding one: it is the same rule that keeps the
// pool and the key service client out of the packages that use them. What this
// file knows is what a revocation *is*.
//
// What is durable lives elsewhere. Postgres records that somebody signed out,
// as an audit event, and that record is permanent. Redis holds only the
// enforcement, which is meant to expire.
type Revocations struct {
	client Client
}

// NewRevocations returns a revocation store backed by client.
func NewRevocations(client Client) *Revocations {
	return &Revocations{client: client}
}

// key namespaces a token id, so this data is recognisable in a shared Redis and
// cannot collide with anything else that ends up there.
func key(tokenID string) string { return "identityhub:revoked-token:" + tokenID }

// Revoke stops a token being accepted, until it would have expired anyway.
//
// The expiry is the token's own: the record is set to outlive it by nothing.
// A token already past its expiry is left alone, because refusing it is
// already what happens.
//
// The value stored is the subject, which is not needed to answer the question
// -- the key existing is the answer -- but makes a live Redis readable when
// somebody is working out what happened. There is no "active" counterpart,
// because nothing is written when a token is issued.
func (r *Revocations) Revoke(ctx context.Context, tokenID, subject string, expiresAt time.Time) error {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return nil
	}
	if err := r.client.Set(ctx, key(tokenID), subject, ttl).Err(); err != nil {
		return fmt.Errorf("record that the token was signed out: %w", err)
	}
	return nil
}

// IsRevoked reports whether a token was signed out.
//
// An error is returned rather than swallowed so the caller can fail closed.
// Not being able to tell whether a credential was revoked is not a reason to
// honour it.
func (r *Revocations) IsRevoked(ctx context.Context, tokenID string) (bool, error) {
	found, err := r.client.Exists(ctx, key(tokenID)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return false, fmt.Errorf("check whether the token was signed out: %w", err)
	}
	return found > 0, nil
}

// Ping reports whether the store is reachable, for the readiness endpoint.
func (r *Revocations) Ping(ctx context.Context) error {
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	return nil
}
