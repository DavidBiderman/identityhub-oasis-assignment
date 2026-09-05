// Package connections stores an account's credentials for an external service
// and hands back something that can act with them.
//
// It exists because that is the one job a connector must not do. A connector
// describes a provider; it should not know which database this application
// uses or which key service wraps its secrets. So the row, the decryption and
// the tenancy check live here, and the import list is the proof: this package
// imports store, crypto and connector, while connector and every connector
// under it import none of them.
//
// It is also the only package in the application that decrypts anything. A
// decrypted credential exists inside a Connection and nowhere else -- never in
// a workflow argument, an API response or a log -- and is discarded with the
// request that needed it.
package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"gocloud.dev/secrets"

	"github.com/dbiderman/identityhub/backend/internal/capabilities/issuetracker"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/crypto"
	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

// ErrNotConnected is returned when an account has no usable connection. It is
// distinct from a rejected credential: the remedy is to connect, not reconnect.
var ErrNotConnected = errors.New(
	"No issue tracker is connected for this account. Connect one before filing findings.")

// Service reads, writes and verifies stored connections.
type Service struct {
	pool *pgxpool.Pool
	// keeper is the key service client. It lives here because this is the only
	// package that encrypts or decrypts anything: a credential is encrypted on
	// the way into the database and decrypted on the way out, both in this file.
	keeper   *secrets.Keeper
	registry *connector.Registry
}

// New returns a Service.
func New(pool *pgxpool.Pool, keeper *secrets.Keeper, registry *connector.Registry) *Service {
	return &Service{pool: pool, keeper: keeper, registry: registry}
}

// Connection is a stored connection resolved for use: the row's identity, and
// a client bound to the credential the row held.
//
// The ID is carried alongside because acting on a connection and recording what
// happened to it are the same moment -- a rejected credential is noted against
// the row it came from.
type Connection struct {
	ID uuid.UUID
	issuetracker.Client
}

// Configure turns an account's stored connection into one that can be used.
//
// Five steps, in this order and no other:
//
//	read the row              -- scoped to the account, so a connection is
//	                             only ever found for its own tenancy
//	resolve the connector     -- cheap, and it cannot fail for a reason that
//	                             involves the credential
//	confirm the capability    -- the contract this product drives it through
//	decrypt                   -- last, so a credential is only ever in memory
//	                             when there is something to do with it
//	decode into its own type  -- the connector's job; only it knows its fields
//
// Resolving and checking the connector before decrypting is deliberate on both
// counts. A connector this build no longer has produces "IdentityHub does not
// have a github connector" rather than "the credential could not be decrypted",
// which is the wrong sentence and sends somebody to reconnect a credential that
// is fine. And a credential that is not going to be used is not worth
// decrypting: the plaintext exists for fewer instructions, on fewer paths.
//
// Every handler and activity goes through here, so the order exists once.
func (s *Service) Configure(ctx context.Context, sc store.Scope, connectorType connector.Type) (Connection, error) {
	row, err := store.Read(s.pool).ActiveConnection(ctx, sqlcgen.ActiveConnectionParams{
		OrgID: sc.OrgID, AccountID: sc.AccountID, ConnectorType: string(connectorType),
	})
	if err = store.MapError(err); errors.Is(err, store.ErrNotFound) {
		return Connection{}, ErrNotConnected
	}
	if err != nil {
		return Connection{}, fmt.Errorf("read the active %s connection: %w", connectorType, err)
	}

	conn, err := s.registry.Get(connectorType)
	if err != nil {
		return Connection{}, err
	}
	if err := issuetracker.Implements(conn); err != nil {
		return Connection{}, err
	}

	plaintext, err := crypto.Decrypt(ctx, s.keeper, sc.String(), row.ConfigEncrypted)
	if err != nil {
		// Decryption failing means the row is unusable: a rotated key it was
		// not re-wrapped for, or corruption. Neither is retryable, and neither
		// should surface as a generic server error.
		return Connection{}, connector.Errorf(connector.ErrConfigInvalid,
			"The stored credential could not be decrypted. Reconnect the integration to continue.")
	}
	defer zero(plaintext)

	cfg, err := conn.DecodeConfig(plaintext)
	if err != nil {
		return Connection{}, err
	}

	return Connection{ID: row.ID, Client: issuetracker.Client{Connector: conn, Config: cfg}}, nil
}

// Save encrypts and stores a connection after verifying the credential works.
//
// Verification happens before the write on purpose: storing a credential that
// has never succeeded would leave an account in a broken state that only
// reveals itself when someone tries to file a ticket.
func (s *Service) Save(
	ctx context.Context,
	sc store.Scope,
	connectorType connector.Type,
	plaintextConfig []byte,
	createdBy uuid.UUID,
) (issuetracker.Owner, uuid.UUID, error) {
	defer zero(plaintextConfig)

	conn, err := s.registry.Get(connectorType)
	if err != nil {
		return issuetracker.Owner{}, uuid.Nil, err
	}
	cfg, err := conn.DecodeConfig(plaintextConfig)
	if err != nil {
		return issuetracker.Owner{}, uuid.Nil, err
	}

	// Identify may complete the configuration -- normalising what was typed,
	// and discovering values only the provider can supply -- so what gets
	// stored is what the connector ended up with, not what arrived.
	owner, err := issuetracker.Client{Connector: conn, Config: cfg}.Identify(ctx)
	if err != nil {
		return issuetracker.Owner{}, uuid.Nil, err
	}

	resolved, err := connector.EncodeConfig(cfg)
	if err != nil {
		return issuetracker.Owner{}, uuid.Nil, err
	}
	defer zero(resolved)

	encrypted, err := crypto.Encrypt(ctx, s.keeper, sc.String(), resolved)
	if err != nil {
		return issuetracker.Owner{}, uuid.Nil, fmt.Errorf("encrypt connection configuration: %w", err)
	}

	metadata, err := json.Marshal(cfg.Metadata())
	if err != nil {
		return issuetracker.Owner{}, uuid.Nil, fmt.Errorf("encode connection metadata: %w", err)
	}

	// Reconnecting is the remediation path for a rejected credential, so it
	// must not be able to leave an account with two active connections or
	// none. Both statements are in one transaction for exactly that reason.
	var id uuid.UUID
	err = store.InTx(ctx, s.pool, func(q *sqlcgen.Queries) error {
		if _, err := q.RevokeConnectionsOfType(ctx, sqlcgen.RevokeConnectionsOfTypeParams{
			OrgID: sc.OrgID, AccountID: sc.AccountID, ConnectorType: string(connectorType),
		}); err != nil {
			return err
		}

		var err error
		id, err = q.InsertConnection(ctx, sqlcgen.InsertConnectionParams{
			OrgID:           sc.OrgID,
			AccountID:       sc.AccountID,
			ConnectorType:   string(connectorType),
			DisplayName:     owner.DisplayName,
			Metadata:        metadata,
			ConfigEncrypted: encrypted,
			CreatedBy:       &createdBy,
		})
		return err
	})
	if err != nil {
		return issuetracker.Owner{}, uuid.Nil, fmt.Errorf(
			"store the %s connection: %w", connectorType, store.MapError(err))
	}
	return owner, id, nil
}

// NoteFailure records that a credential was rejected, so the UI can prompt for
// a reconnect rather than failing every action silently.
//
// Only an outright rejection marks a connection invalid. A rate limit or an
// outage says nothing about the credential, and marking it invalid would turn a
// transient upstream problem into a manual reconnect for every account.
func (s *Service) NoteFailure(ctx context.Context, sc store.Scope, connectionID uuid.UUID, err error) {
	if !errors.Is(err, connector.ErrUnauthorized) {
		return
	}
	var ce *connector.Error
	message := "The stored credential was rejected."
	if errors.As(err, &ce) && ce.Message != "" {
		message = ce.Message
	}
	_ = store.InTx(ctx, s.pool, func(q *sqlcgen.Queries) error {
		return q.MarkConnectionInvalid(ctx, sqlcgen.MarkConnectionInvalidParams{
			ID: connectionID, OrgID: sc.OrgID, AccountID: sc.AccountID, LastError: &message,
		})
	})
}

// NoteSuccess records a working credential.
func (s *Service) NoteSuccess(ctx context.Context, sc store.Scope, connectionID uuid.UUID) {
	_ = store.InTx(ctx, s.pool, func(q *sqlcgen.Queries) error {
		return q.MarkConnectionVerified(ctx, sqlcgen.MarkConnectionVerifiedParams{
			ID: connectionID, OrgID: sc.OrgID, AccountID: sc.AccountID,
		})
	})
}

// zero overwrites a decrypted buffer once it is no longer needed.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
