// Package config reads and validates what a process needs to start.
//
// Every binary declares its own configuration struct and calls MustLoad; the
// values more than one of them needs -- the environment, the database, the key
// service, Temporal -- are the shared pieces here, so that a setting means the
// same thing in the API as it does in the worker.
//
// Nothing calls os.Getenv outside this package. A binary that needs a value
// declares a field for it, which is what makes a misconfigured deployment a
// refusal at startup with every problem listed at once, rather than a nil
// dereference on the first request that happens to need it.
//
// The logger lives here too, because its level and its format are decided by
// the environment this reads. Domain packages must not depend on any of it.
package config

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/sethvargo/go-envconfig"
)

// Environment names the deployment a process is running in.
type Environment string

const (
	Development Environment = "development"
	Production  Environment = "production"
)

// IsProduction reports whether stricter rules apply.
func (e Environment) IsProduction() bool { return e == Production }

// Valid reports whether the environment is one this application knows.
func (e Environment) Valid() bool { return e == Development || e == Production }

// Core is the configuration every binary needs.
//
// It is embedded rather than inherited, so each binary declares exactly the
// variables it reads: the worker never asks for a listen address, and the
// migrate step never asks for a key service. A binary that stops needing a
// value stops reading it, which is visible in one struct.
type Core struct {
	Env         Environment `env:"APP_ENV,default=development"`
	DatabaseURL string      `env:"DATABASE_URL,required"`
}

// Validate reports whether the shared values are usable.
func (c Core) Validate() error {
	var errs []error
	if !c.Env.Valid() {
		errs = append(errs, fmt.Errorf("APP_ENV must be %q or %q, got %q",
			Development, Production, c.Env))
	}
	if _, err := url.Parse(c.DatabaseURL); err != nil {
		errs = append(errs, fmt.Errorf("DATABASE_URL is not a valid connection string: %w", err))
	}
	return errors.Join(errs...)
}

// Secrets is the key service configuration, for binaries that decrypt.
type Secrets struct {
	// KeeperURL selects the key service through the Go CDK. The scheme is the
	// provider: awskms://, gcpkms://, azurekeyvault://, hashivault://, or
	// base64key:// for a local key. It may contain key material and is never
	// logged.
	KeeperURL string `env:"SECRETS_KEEPER_URL,required"`
}

// Validate reports whether the key service configuration is usable.
func (s Secrets) Validate(env Environment) error {
	parsed, err := url.Parse(s.KeeperURL)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf(
			`SECRETS_KEEPER_URL must name a provider, e.g. "awskms://alias/identityhub"`)
	}
	if env.IsProduction() && parsed.Scheme == "base64key" {
		// base64key:// carries the key in the URL, so the key protecting the
		// credentials would live in the same environment as the credentials.
		return fmt.Errorf(
			"SECRETS_KEEPER_URL must not use base64key:// when APP_ENV=production; use a managed key service")
	}
	return nil
}

// Temporal is the durable execution configuration.
type Temporal struct {
	HostPort  string `env:"TEMPORAL_HOSTPORT,default=localhost:7233"`
	Namespace string `env:"TEMPORAL_NAMESPACE,default=identityhub"`
}

// Validate reports whether the Temporal configuration is usable.
func (t Temporal) Validate() error {
	if strings.TrimSpace(t.HostPort) == "" {
		return fmt.Errorf("TEMPORAL_HOSTPORT is required")
	}
	if strings.TrimSpace(t.Namespace) == "" {
		return fmt.Errorf("TEMPORAL_NAMESPACE is required")
	}
	return nil
}

// Validator is anything a binary's configuration can be checked against.
type Validator interface {
	Validate() error
}

// MustLoad reads a configuration struct from the environment, validates it,
// and exits the process if it is unusable.
//
// A process with an unusable configuration must not start. There is no useful
// degraded mode: a missing database URL or key service is not something the
// application can work around, and discovering it at the first request rather
// than at startup turns a deployment mistake into an outage.
//
// The report names every problem at once, so a misconfigured deployment is one
// fix rather than a sequence of restarts, and it is written as plain lines
// rather than a log record: nothing is running yet, and the reader is a person
// looking at a container that would not start.
func MustLoad(ctx context.Context, name string, cfg Validator) {
	if err := Load(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "%s cannot start.\n\n%s\n", name, err)
		os.Exit(1)
	}
}

// Load reads and validates a configuration struct, returning the problems
// rather than exiting. MustLoad is what binaries call.
func Load(ctx context.Context, cfg Validator) error {
	if err := envconfig.Process(ctx, cfg); err != nil {
		return fmt.Errorf("  %s", err)
	}
	if err := cfg.Validate(); err != nil {
		lines := strings.Split(err.Error(), "\n")
		for i, line := range lines {
			lines[i] = "  " + line
		}
		return fmt.Errorf("%s", strings.Join(lines, "\n"))
	}
	return nil
}
