// Package cache is the process's connection to Redis.
//
// "Cache" describes the storage, not the reliance. Redis here is a key-value
// store whose contents carry deadlines and are meant to be lost -- which is a
// cache in the sense that matters for choosing it. It is *not* a cache in the
// sense of something a caller can fall back from: the one thing kept here is
// the set of tokens that were signed out, and there is nowhere else to look, so
// a caller that cannot reach it refuses the request rather than guessing. See
// DECISIONS.md §2a.
//
// The package holds the connection and nothing else. What a revocation is, and
// what to do when the store will not answer, belong to internal/auth -- the same
// split as store and crypto, where the package owns the resource and its
// consumer owns the meaning.
package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// Per-request budgets. The revocation check runs on the hot path of every
	// authenticated request, and a local lookup that has not answered by then
	// has failed -- so failing closed quickly beats failing closed slowly.
	//
	// These bound the client rather than each call, and that distinction was
	// learned the hard way: a 250ms context on the call still produced a
	// five-second request against an unresponsive Redis, because go-redis
	// spends its own budget getting a usable connection out of the pool before
	// the command -- and therefore the caller's deadline -- is reached at all.
	dialTimeout  = 500 * time.Millisecond
	readTimeout  = 250 * time.Millisecond
	writeTimeout = 250 * time.Millisecond
	poolTimeout  = 500 * time.Millisecond
	maxRetries   = 1

	// Startup gets its own grace, and a generous one. The budgets above are for
	// a request; this is a process that is not serving yet, connecting to a
	// dependency that may still be coming up beside it -- a managed Redis
	// accepting its first connection, a container ordering race, a cold network
	// path. Refusing to boot on a slow first connection would be a worse failure
	// than the one those budgets exist to prevent.
	startupGrace = 30 * time.Second
)

// Open connects to Redis and confirms it answers.
//
// A binary calls this once at startup and hands the client to whatever needs
// it. Nothing further down builds its own: a shared resource created in a corner
// is one nobody can size, instrument or shut down -- the same rule as the
// database pool and the key service client.
//
// Connecting eagerly rather than lazily, for the same reason configuration is
// validated at startup: a wrong address should stop the process, not surface as
// the first person who tries to sign out.
func Open(ctx context.Context, url string) (*redis.Client, error) {
	options, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("REDIS_URL is not a valid Redis address: %w", err)
	}

	options.DialTimeout = dialTimeout
	options.ReadTimeout = readTimeout
	options.WriteTimeout = writeTimeout
	options.PoolTimeout = poolTimeout
	options.MaxRetries = maxRetries

	client := redis.NewClient(options)

	startup, cancel := context.WithTimeout(ctx, startupGrace)
	defer cancel()
	if err := client.Ping(startup).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect to Redis at %s: %w", options.Addr, err)
	}
	return client, nil
}
