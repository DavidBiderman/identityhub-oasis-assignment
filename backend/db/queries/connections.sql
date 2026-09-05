-- Revokes whatever the account currently has of this type, whether it still
-- works or not. 'invalid' is included deliberately: a rejected credential is
-- what a reconnect replaces, and matching only 'active' left the invalid row
-- behind on every failure cycle, where the listing query returned it forever.
--
-- One statement, reported. The caller that does not care about the count
-- ignores it; disconnect turns a count of zero into a 404.
-- name: RevokeConnectionsOfType :execrows
update connections
   set status = 'revoked', updated_at = now()
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and connector_type = sqlc.arg(connector_type)
   and status in ('active', 'invalid');

-- name: InsertConnection :one
insert into connections (
    org_id, account_id, connector_type, display_name, metadata,
    config_encrypted, status, last_verified_at, created_by
)
values (
    sqlc.arg(org_id), sqlc.arg(account_id), sqlc.arg(connector_type),
    sqlc.arg(display_name), sqlc.arg(metadata),
    sqlc.arg(config_encrypted),
    'active', now(), sqlc.narg(created_by)
)
returning id;

-- Reads the encrypted configuration, so it is used only when a credential is
-- about to be needed.
-- name: ActiveConnection :one
select id, connector_type, display_name, metadata, config_encrypted,
       status, last_verified_at, last_error, created_at
  from connections
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and connector_type = sqlc.arg(connector_type)
   and status = 'active';

-- Deliberately omits config_encrypted: nothing in a listing needs the
-- credential, so it is never read into memory.
-- name: ListConnections :many
select id, connector_type, display_name, metadata, status,
       last_verified_at, last_error, created_at
  from connections
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and status <> 'revoked'
 order by created_at desc;

-- name: MarkConnectionInvalid :exec
update connections
   set status = 'invalid', last_error = sqlc.arg(last_error), updated_at = now()
 where id = sqlc.arg(id)
   and org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and status = 'active';

-- name: MarkConnectionVerified :exec
update connections
   set last_verified_at = now(), last_error = null, updated_at = now()
 where id = sqlc.arg(id)
   and org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id);
