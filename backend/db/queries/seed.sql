-- Queries used only by the bootstrap step, which creates the demo tenancy.
--
-- They live here with everything else rather than as hand-written SQL in the
-- binary: a seed that drifts from the schema fails at first run, and finding
-- that at generation time is cheaper than finding it in a reviewer's terminal.

-- name: OrganizationBySlug :one
select id from organizations where slug = sqlc.arg(slug);

-- name: DeleteOrganizationBySlug :exec
delete from organizations where slug = sqlc.arg(slug);

-- name: CreateOrganization :one
insert into organizations (name, slug)
values (sqlc.arg(name), sqlc.arg(slug))
returning id;

-- name: CreateAccount :one
insert into accounts (org_id, name, slug)
values (sqlc.arg(org_id), sqlc.arg(name), sqlc.arg(slug))
returning id;

-- name: CreateUser :exec
insert into users (org_id, account_id, subject, email, display_name,
                   password_hash, roles)
values (
    sqlc.arg(org_id), sqlc.arg(account_id), sqlc.arg(subject),
    sqlc.arg(email), sqlc.arg(display_name),
    sqlc.arg(password_hash), sqlc.arg(roles)
);
