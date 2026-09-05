-- The application only ever inserts here. UPDATE and DELETE on this table are
-- revoked from the application role, so the trail is append-only in the
-- database as well as in this package.
-- name: AppendAuditEvent :exec
insert into audit_events (org_id, account_id, actor_type, actor_id, action, target, metadata, ip)
values (
    sqlc.arg(org_id), sqlc.arg(account_id), sqlc.arg(actor_type), sqlc.narg(actor_id),
    sqlc.arg(action), sqlc.arg(target), sqlc.arg(metadata), sqlc.narg(ip)
);

-- name: RecentAuditEvents :many
select id, actor_type, action, target, metadata, created_at
  from audit_events
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
 order by created_at desc
 limit sqlc.arg(row_limit);
