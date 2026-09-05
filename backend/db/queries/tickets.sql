-- name: InsertTicket :one
insert into tickets (
    org_id, account_id, connection_id, project_key, issue_key, issue_id,
    issue_url, summary, source, created_by_user, created_by_key, jira_created_at
)
values (
    sqlc.arg(org_id), sqlc.arg(account_id), sqlc.narg(connection_id),
    sqlc.arg(project_key), sqlc.arg(issue_key), sqlc.arg(issue_id),
    sqlc.arg(issue_url), left(sqlc.arg(summary), 500), sqlc.arg(source),
    sqlc.narg(created_by_user), sqlc.narg(created_by_key), sqlc.narg(jira_created_at)
)
returning *;

-- name: TicketByID :one
select *
  from tickets
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and id = sqlc.arg(id);

-- name: TicketByIssueKey :one
select *
  from tickets
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and issue_key = sqlc.arg(issue_key);

-- This table, not a provider-side label, answers "created from this app": a
-- label is mutable by anyone with access and cannot be trusted for it.
-- An empty project_key means every project.
-- name: RecentTickets :many
select *
  from tickets
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and (sqlc.arg(project_key)::text = '' or project_key = sqlc.arg(project_key)::text)
 order by created_at desc
 limit sqlc.arg(row_limit);
