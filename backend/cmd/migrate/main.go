// Command migrate applies the database schema.
//
// It runs as its own Compose step before the API starts, so the API never
// races a partially migrated database.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"

	"github.com/dbiderman/identityhub/backend/db"
	"github.com/dbiderman/identityhub/backend/internal/config"
)

// applyTimeout bounds the whole run. Waiting for the database is not part of
// it: Compose holds this step until postgres reports healthy, so a database
// that is not reachable here is a failure rather than something to sit through.
const applyTimeout = 60 * time.Second

func main() {
	command := flag.String("command", "up", "goose command: up, down, status, version")
	flag.Parse()

	var cfg migrateConfig
	config.MustLoad(context.Background(), "migrate", &cfg)

	// The redacting handler, installed as the process default before anything
	// can fail: these binaries report their failures through slog directly, and
	// a database URL inside a connection error is exactly what must not reach
	// stderr in the clear.
	config.NewLogger(cfg.Env)

	if err := run(cfg, *command); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}
}

func run(cfg migrateConfig, command string) error {
	sqlDB, err := goose.OpenDBWithDriver("pgx", cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("database not reachable: %w", err)
	}

	if err := ensureAppRole(ctx, sqlDB, cfg.AppDBPassword); err != nil {
		return err
	}

	goose.SetBaseFS(db.Migrations)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	if err := goose.RunContext(ctx, command, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("goose %s: %w", command, err)
	}

	version, err := goose.GetDBVersionContext(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	slog.Info("migrations applied", "command", command, "schema_version", version)
	return nil
}

// appRole is the role the API and worker connect as. It can read and write
// rows and nothing else: no DDL, no ownership.
const appRole = "identityhub_app"

// ensureAppRole creates or updates the application's database role.
//
// It runs here rather than in a migration because creating a login role is
// provisioning rather than a schema change, and because the password has to
// come from the environment: a literal in a migration file is a credential
// published in the repository and applied to every environment it is ever run
// against. The grants that go with the role are still in the migration, where
// they belong, and they run immediately after this.
//
// The password is quoted by Postgres rather than by string formatting here.
// CREATE ROLE takes no bound parameter, so quote_literal is what makes the
// value data instead of syntax.
func ensureAppRole(ctx context.Context, sqlDB *sql.DB, password string) error {
	var quoted string
	if err := sqlDB.QueryRowContext(ctx, `select quote_literal($1::text)`, password).Scan(&quoted); err != nil {
		return fmt.Errorf("quote the %s password: %w", appRole, err)
	}

	var exists bool
	if err := sqlDB.QueryRowContext(ctx,
		`select exists(select 1 from pg_roles where rolname = $1)`, appRole).Scan(&exists); err != nil {
		return fmt.Errorf("look for the %s role: %w", appRole, err)
	}

	statement := "create role " + appRole + " login password " + quoted
	if exists {
		statement = "alter role " + appRole + " password " + quoted
	}
	if _, err := sqlDB.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("create or update the %s role: %w", appRole, err)
	}
	return nil
}
