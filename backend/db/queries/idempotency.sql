-- ON CONFLICT DO NOTHING makes the claim atomic: exactly one concurrent caller
-- inserts, and the others fall through to reading the existing row.
-- name: ClaimIdempotencyKey :execrows
insert into idempotency_keys (org_id, account_id, key, request_hash)
values (sqlc.arg(org_id), sqlc.arg(account_id), sqlc.arg(key), sqlc.arg(request_hash))
on conflict (org_id, account_id, key) do nothing;

-- name: GetIdempotencyKey :one
select request_hash, ticket_id, created_at
  from idempotency_keys
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and key = sqlc.arg(key);

-- A claim with no result, old enough that the attempt which made it cannot
-- still be running, is taken over rather than left blocking the caller.
--
-- The two conditions that make a claim abandoned are here rather than in Go.
-- They were in Go, above an unconditional UPDATE, which meant two callers could
-- read the same stale row, both decide it was abandoned, and both take it over
-- -- two tickets under one idempotency key, the exact outcome the key exists to
-- prevent. As one statement the row is locked by the update itself, so the
-- second caller matches nothing and the row count says so.
--
-- now() is the database's clock on both sides of the comparison, so a skewed
-- application clock cannot widen or narrow the window.
-- name: TakeOverAbandonedIdempotencyKey :execrows
update idempotency_keys
   set created_at = now(), request_hash = sqlc.arg(request_hash)
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and key = sqlc.arg(key)
   and ticket_id is null
   and created_at < now() - sqlc.arg(abandoned_after)::interval;

-- Attaching a result is only correct while there is not one already: a claim
-- that has been taken over belongs to a different attempt now.
-- name: CompleteIdempotencyKey :exec
update idempotency_keys
   set ticket_id = sqlc.arg(ticket_id)
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and key = sqlc.arg(key)
   and ticket_id is null;

-- Releases a claim whose work failed, so a transient failure does not lock the
-- key. A completed claim is left alone.
-- name: ReleaseIdempotencyKey :exec
delete from idempotency_keys
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and key = sqlc.arg(key)
   and ticket_id is null;
