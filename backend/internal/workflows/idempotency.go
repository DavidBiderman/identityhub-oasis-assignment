package workflows

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

// abandonedClaimAfter is how long a claim may sit without a result before
// another attempt may take it over.
//
// A claim is made before the work starts and completed after it finishes. If
// the process handling it dies in between -- a crashed worker, a lost node --
// nothing releases the claim, and without this the caller would be locked out
// of that key permanently. The window is long enough that it cannot overlap a
// request still legitimately in flight.
//
// It is passed to the query as an interval and compared against the database's
// own clock, so this process's clock does not decide what counts as abandoned.
var abandonedClaimAfter = pgtype.Interval{Microseconds: 15 * 60 * 1_000_000, Valid: true}

// errIdempotencyMismatch is returned when a key is reused with a different
// request body. Returning the first response would be wrong, and creating a
// second ticket would defeat the point of the key.
var errIdempotencyMismatch = errors.New(
	"This Idempotency-Key was already used with a different request body.")

// errIdempotencyInProgress is returned when the key is held by a request that
// has not finished. There is no result to hand back yet, and filing a second
// ticket is the one thing the key exists to prevent.
var errIdempotencyInProgress = errors.New(
	"A request with this Idempotency-Key is still being processed. Retry shortly.")

// claimKey reserves an idempotency key for the account.
//
// A scanner or CI job that retries after a timeout must not file the same
// finding twice. The first caller claims the key and goes on to do the work; a
// replay gets back the ticket the original attempt produced, if it finished.
//
// It returns claimed=true when the caller owns the key and should proceed.
// Otherwise the key was already claimed and ticketID identifies the resulting
// ticket, or is nil if the original attempt has not completed.
//
// The whole sequence is one transaction: the insert, the read of an existing
// row and the takeover of an abandoned claim have to agree with each other, and
// two callers racing must not both come away believing they own it. This is why
// InTx takes a function rather than returning a transaction -- the several
// statements are the point.
func (a *Activities) claimKey(
	ctx context.Context,
	sc store.Scope,
	key string,
	requestHash []byte,
) (claimed bool, ticketID *uuid.UUID, err error) {
	err = store.InTx(ctx, a.Pool, func(q *sqlcgen.Queries) error {
		// ON CONFLICT DO NOTHING makes the claim atomic: exactly one concurrent
		// caller inserts, and the others fall through to the read below.
		inserted, err := q.ClaimIdempotencyKey(ctx, sqlcgen.ClaimIdempotencyKeyParams{
			OrgID: sc.OrgID, AccountID: sc.AccountID, Key: key, RequestHash: requestHash,
		})
		if err != nil {
			return err
		}
		if inserted == 1 {
			claimed = true
			return nil
		}

		existing, err := q.GetIdempotencyKey(ctx, sqlcgen.GetIdempotencyKeyParams{
			OrgID: sc.OrgID, AccountID: sc.AccountID, Key: key,
		})
		if err != nil {
			return err
		}
		if !bytes.Equal(existing.RequestHash, requestHash) {
			return errIdempotencyMismatch
		}

		// An old claim with no result belongs to an attempt that never
		// finished. Take it over rather than leaving the caller stuck.
		//
		// Whether it is old enough is decided by the UPDATE, not here: two
		// callers reading the same stale row would both pass a check written
		// in Go, and both would take the key. The statement matches at most
		// one of them, and the row count says which.
		taken, err := q.TakeOverAbandonedIdempotencyKey(ctx, sqlcgen.TakeOverAbandonedIdempotencyKeyParams{
			OrgID: sc.OrgID, AccountID: sc.AccountID, Key: key,
			RequestHash: requestHash, AbandonedAfter: abandonedClaimAfter,
		})
		if err != nil {
			return err
		}
		if taken == 1 {
			claimed = true
			return nil
		}

		ticketID = existing.TicketID
		return nil
	})
	if errors.Is(err, errIdempotencyMismatch) {
		return false, nil, err
	}
	if err != nil {
		return false, nil, fmt.Errorf("claim the idempotency key: %w", err)
	}
	return claimed, ticketID, nil
}

// completeKey attaches the resulting ticket to a claimed key, so a later replay
// can return the original result.
func (a *Activities) completeKey(ctx context.Context, sc store.Scope, key string, ticketID uuid.UUID) error {
	err := store.InTx(ctx, a.Pool, func(q *sqlcgen.Queries) error {
		return q.CompleteIdempotencyKey(ctx, sqlcgen.CompleteIdempotencyKeyParams{
			OrgID: sc.OrgID, AccountID: sc.AccountID, Key: key, TicketID: &ticketID,
		})
	})
	if err != nil {
		return fmt.Errorf("complete the idempotency key: %w", err)
	}
	return nil
}

// releaseKey drops a claim whose work failed, so the caller can retry with the
// same key rather than being locked out by a transient failure.
func (a *Activities) releaseKey(ctx context.Context, sc store.Scope, key string) error {
	err := store.InTx(ctx, a.Pool, func(q *sqlcgen.Queries) error {
		return q.ReleaseIdempotencyKey(ctx, sqlcgen.ReleaseIdempotencyKeyParams{
			OrgID: sc.OrgID, AccountID: sc.AccountID, Key: key,
		})
	})
	if err != nil {
		return fmt.Errorf("release the idempotency key: %w", err)
	}
	return nil
}
