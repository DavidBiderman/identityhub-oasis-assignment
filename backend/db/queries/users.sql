-- Who a person is, and -- when this deployment is the one signing them in --
-- what they present to prove it.

-- name: UpsertUser :one
insert into users (org_id, account_id, subject, email, display_name)
values (
    sqlc.arg(org_id), sqlc.arg(account_id), sqlc.arg(subject),
    left(sqlc.arg(email), 320), left(sqlc.arg(display_name), 200)
)
-- The conflict target matches the index: same subject, same account. A subject
-- seen in a different account is a different person and gets its own row, so
-- org_id and account_id are not in the update -- there is nothing to move.
on conflict (org_id, account_id, subject) do update
   set email        = excluded.email,
       display_name = excluded.display_name,
       last_seen_at = now()
returning id;

-- name: UserForSignIn :one
-- Everything the login endpoint needs, in one read.
--
-- Deliberately not scoped: a person types an email address, not an
-- organization, so this is the second place in the application where a tenancy
-- is discovered rather than supplied. The email index is unique across the
-- table, which is what makes that safe.
--
-- Roles come back with it because they are claims on the token this issues, and
-- fetching them separately would mean a second round trip on the one path that
-- has a person waiting.
select u.id, u.org_id, u.account_id, u.subject, u.email, u.display_name,
       u.password_hash,
       coalesce(u.roles, '{}')::text[] as roles
from users u
where lower(u.email) = lower(sqlc.arg(email));

-- name: TouchUser :exec
-- Records that somebody signed in. Best effort; nothing depends on it.
update users set last_seen_at = now() where id = sqlc.arg(id);
