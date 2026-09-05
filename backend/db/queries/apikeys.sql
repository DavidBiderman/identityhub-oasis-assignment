-- name: CreateAPIKey :one
insert into api_keys (org_id, account_id, key_id, secret_hash, name, scopes, created_by)
values (
    sqlc.arg(org_id), sqlc.arg(account_id), sqlc.arg(key_id),
    sqlc.arg(secret_hash), sqlc.arg(name), sqlc.arg(scopes), sqlc.narg(created_by)
)
returning id, key_id, name, scopes, created_at, last_used_at, revoked_at;

-- Resolves a tenancy from a credential, so it cannot be scoped.
-- name: FindAPIKey :one
select id, org_id, account_id, secret_hash, scopes,
       (revoked_at is not null)::boolean as revoked
  from api_keys
 where key_id = sqlc.arg(key_id);

-- name: TouchAPIKey :exec
update api_keys
   set last_used_at = now()
 where id = sqlc.arg(id)
   and org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id);

-- name: ListAPIKeys :many
select id, key_id, name, scopes, created_at, last_used_at, revoked_at
  from api_keys
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
 order by created_at desc
 limit 100;

-- Revocation is a timestamp rather than a delete, so audit events referencing
-- the key stay resolvable.
-- name: RevokeAPIKey :execrows
update api_keys
   set revoked_at = now()
 where id = sqlc.arg(id)
   and org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and revoked_at is null;
