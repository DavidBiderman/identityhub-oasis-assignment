-- +goose Up

-- The application connects as a role that cannot change the schema.
--
-- Migrations run as the owner; the API and worker run as this role. It can read
-- and write rows and nothing else: no DDL, so an injection or a bug cannot drop
-- a table, and no ownership, so it cannot grant itself more.
--
-- The role itself is created by cmd/migrate before goose runs, not here.
--
-- It used to be created here with the literal password 'identityhub_app' --
-- written in this file, therefore published in the repository, and applied
-- unconditionally to every environment a migration was ever run against.
-- Anyone able to reach port 5432 could read and write every tenant's rows, and
-- no amount of query-level scoping matters at that point.
--
-- Creating a role is provisioning, not a schema change, and the password has to
-- come from the environment. Both belong in Go, where the value can be quoted
-- by the database itself and refused when it is the published default and
-- APP_ENV=production. What is left here is the grants, which are about the
-- schema and belong with it.

grant usage on schema public to identityhub_app;
grant select, insert, update, delete on all tables in schema public to identityhub_app;
grant usage, select on all sequences in schema public to identityhub_app;

-- The audit trail is append-only for the application: it may add events and
-- read them back, but not rewrite or erase them.
revoke update, delete on audit_events from identityhub_app;

-- Tables added by a later migration should inherit the same grants rather than
-- being silently unreachable until someone notices.
alter default privileges in schema public
    grant select, insert, update, delete on tables to identityhub_app;
alter default privileges in schema public
    grant usage, select on sequences to identityhub_app;

-- +goose Down
alter default privileges in schema public
    revoke select, insert, update, delete on tables from identityhub_app;
alter default privileges in schema public
    revoke usage, select on sequences from identityhub_app;
revoke all on all sequences in schema public from identityhub_app;
revoke all on all tables in schema public from identityhub_app;
revoke usage on schema public from identityhub_app;
