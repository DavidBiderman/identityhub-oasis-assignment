package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dbiderman/identityhub/backend/internal/config"
)

// defaultAppPassword is the value the repository ships for local development.
// It is refused in production, because it is published here.
const defaultAppPassword = "identityhub_app"

// migrateConfig is everything the migrate step reads.
//
// It needs a database and the password to give the application's role, and
// nothing else. In particular it does not read the key service configuration:
// migrations change the schema, never the ciphertext in it, so a broken key
// setting must not be able to block a schema change.
type migrateConfig struct {
	config.Core

	// AppDBPassword is the password given to the identityhub_app role, which
	// is what the API and worker connect as. The migration reads it from the
	// environment rather than carrying a literal.
	AppDBPassword string `env:"APP_DB_PASSWORD"`
}

// Validate reports whether the configuration is usable.
func (c migrateConfig) Validate() error {
	errs := []error{c.Core.Validate()}

	password := strings.TrimSpace(c.AppDBPassword)
	switch {
	case password == "":
		errs = append(errs, fmt.Errorf(
			"APP_DB_PASSWORD is required: it is the password given to the identityhub_app "+
				"role that the API and worker connect as"))
	case c.Env.IsProduction() && password == defaultAppPassword:
		errs = append(errs, fmt.Errorf(
			"APP_DB_PASSWORD is the value published in this repository; set a real one "+
				"when APP_ENV=production"))
	case strings.ContainsAny(password, "'\\"):
		// The value is substituted into a SQL string literal by goose, which
		// does not quote it. Rejecting the two characters that could end the
		// literal early is the guard; there is no bound parameter available in
		// a CREATE ROLE.
		errs = append(errs, fmt.Errorf(
			"APP_DB_PASSWORD must not contain a quote or a backslash"))
	}
	return errors.Join(errs...)
}
