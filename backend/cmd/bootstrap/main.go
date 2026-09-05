// Command bootstrap prepares a fresh environment: it creates the customer
// managed key that credentials are encrypted under, and the demo organization,
// accounts and users.
//
// Both steps are idempotent, so restarting the stack is harmless. It runs as a
// Compose step that must complete before the API starts.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/dbiderman/identityhub/backend/internal/auth"
	"github.com/dbiderman/identityhub/backend/internal/config"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

func main() {
	skipKMS := flag.Bool("skip-kms", false, "do not create the KMS key")
	skipSeed := flag.Bool("skip-seed", false, "do not create the demo data")
	force := flag.Bool("force", false, "recreate the demo data if it already exists")
	flag.Parse()

	var cfg bootstrapConfig
	config.MustLoad(context.Background(), "bootstrap", &cfg)

	// The redacting handler, installed as the process default before anything
	// can fail: these binaries report their failures through slog directly, and
	// a database URL inside a connection error is exactly what must not reach
	// stderr in the clear.
	config.NewLogger(cfg.Env)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if !*skipKMS {
		if err := ensureKey(ctx, cfg.KMSAlias); err != nil {
			slog.Error("could not prepare the encryption key", "error", err)
			os.Exit(1)
		}
	}
	if !*skipSeed {
		if err := seed(ctx, cfg.DatabaseURL, *force); err != nil {
			slog.Error("could not create the demo data", "error", err)
			os.Exit(1)
		}
	}
}

// ensureKey creates the customer managed key and its alias if they do not exist.
//
// The alias is what the application configures, so rotating the underlying key
// is a matter of repointing the alias rather than changing configuration.
func ensureKey(ctx context.Context, alias string) error {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}
	client := kms.NewFromConfig(cfg)

	// DescribeKey on the alias is the cheapest existence check, and it also
	// confirms the alias actually resolves to a usable key.
	if out, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(alias)}); err == nil {
		slog.Info("encryption key already present", "alias", alias, "key_id", *out.KeyMetadata.KeyId)
		return nil
	}

	created, err := client.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("IdentityHub connector credential encryption"),
		KeyUsage:    types.KeyUsageTypeEncryptDecrypt,
	})
	if err != nil {
		return fmt.Errorf("create key: %w", err)
	}
	keyID := *created.KeyMetadata.KeyId

	if _, err := client.CreateAlias(ctx, &kms.CreateAliasInput{
		AliasName:   aws.String(alias),
		TargetKeyId: aws.String(keyID),
	}); err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
		return fmt.Errorf("create alias: %w", err)
	}

	slog.Info("encryption key created", "alias", alias, "key_id", keyID)
	return nil
}

type seedUser struct {
	accountName string
	accountSlug string
	subject     string
	email       string
	displayName string
	roles       []string
}

// demoPassword is what every seeded person signs in with.
//
// One password for all three, printed in the README, because these accounts
// exist to be signed into by whoever is reviewing this. It is seeded data, not
// a credential: the tenancy it opens contains nothing but more seeded data.
//
// A deployment that federates seeds no passwords at all -- password_hash stays
// null and the identity provider authenticates people instead.
const demoPassword = "identityhub-demo"

// demoOrgSlug identifies the seeded organization, so a re-run can find it.
const demoOrgSlug = "acme"

// demoUsers is the tenancy the seed creates: one organization, two accounts,
// and two roles inside the first so that both isolation and permissions can be
// checked from the interface.
// The subject is the identifier an identity provider would assert. It is the
// key this application stores people by, because an email address can change
// and be reassigned.
// Roles are a column now, because this application signs people in and is
// therefore the thing that has to say who is an administrator. Federate instead
// and the provider asserts them in the token; this column stops being read.
var demoUsers = []seedUser{
	{"Platform Engineering", "platform", "dev|dana-admin", "admin@acme.test", "Dana Admin",
		[]string{"admin", "member"}},
	{"Platform Engineering", "platform", "dev|sam-member", "member@acme.test", "Sam Member",
		[]string{"member"}},
	{"Security Operations", "secops", "dev|rae-secops", "secops@acme.test", "Rae SecOps",
		[]string{"admin", "member"}},
}

// seed creates one organization containing two accounts.
//
// Two accounts, not one, on purpose: the isolation claim is only checkable if
// there is a sibling account to check it against.
//
// The whole seed runs in one transaction. A half-created tenancy -- an
// organization with no users, or an account whose users failed midway -- is
// worse than none at all, because the next run would find the organization
// present and skip.
func seed(ctx context.Context, dsn string, force bool) error {
	conn, err := connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := sqlcgen.New(tx)

	if force {
		if err := q.DeleteOrganizationBySlug(ctx, demoOrgSlug); err != nil {
			return fmt.Errorf("clear existing demo data: %w", err)
		}
	}

	orgID, err := q.OrganizationBySlug(ctx, demoOrgSlug)
	if err == nil {
		slog.Info("demo data already present", "org_id", orgID, "hint", "use -force to recreate")
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check for existing demo data: %w", err)
	}

	orgID, err = q.CreateOrganization(ctx, sqlcgen.CreateOrganizationParams{
		Name: "Acme Corp", Slug: demoOrgSlug,
	})
	if err != nil {
		return fmt.Errorf("create organization: %w", err)
	}

	accounts := map[string]uuid.UUID{}
	for _, u := range demoUsers {
		accountID, ok := accounts[u.accountSlug]
		if !ok {
			accountID, err = q.CreateAccount(ctx, sqlcgen.CreateAccountParams{
				OrgID: orgID, Name: u.accountName, Slug: u.accountSlug,
			})
			if err != nil {
				return fmt.Errorf("create account %s: %w", u.accountSlug, err)
			}
			accounts[u.accountSlug] = accountID
		}

		hash, err := auth.HashPassword(demoPassword)
		if err != nil {
			return fmt.Errorf("hash the demo password: %w", err)
		}

		if err := q.CreateUser(ctx, sqlcgen.CreateUserParams{
			OrgID:        orgID,
			AccountID:    accountID,
			Subject:      u.subject,
			Email:        u.email,
			DisplayName:  u.displayName,
			PasswordHash: hash,
			Roles:        u.roles,
		}); err != nil {
			return fmt.Errorf("create user %s: %w", u.email, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	slog.Info("demo data created", "organization", "Acme Corp", "org_id", orgID)
	for _, u := range demoUsers {
		slog.Info("user", "email", u.email, "account", u.accountSlug, "subject", u.subject)
	}
	// The password is the same for all three, and is printed rather than hidden:
	// these accounts exist to be signed into by whoever is reviewing this, and
	// the tenancy they open contains nothing but more seeded data.
	slog.Info("open the interface and sign in",
		"email", demoUsers[0].email, "password", demoPassword)
	return nil
}

// connect waits for the database, since Compose starts everything at once.
func connect(ctx context.Context, dsn string) (*pgx.Conn, error) {
	var lastErr error
	for {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			return conn, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("database not reachable: %w", lastErr)
		case <-time.After(time.Second):
		}
	}
}
