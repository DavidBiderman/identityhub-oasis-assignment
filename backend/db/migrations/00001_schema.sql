-- +goose Up

-- Multi-tenancy is two layers deep. An organization is a customer; an account
-- is an isolated workspace inside that customer, and one organization may have
-- many. Every tenant-owned row below carries both, and every query filters on
-- both. Nothing is scoped by organization alone.
create table organizations (
    id         uuid primary key default gen_random_uuid(),
    name       text        not null,
    slug       text        not null unique,
    created_at timestamptz not null default now()
);

create table accounts (
    id         uuid primary key default gen_random_uuid(),
    org_id     uuid        not null references organizations (id) on delete cascade,
    name       text        not null,
    slug       text        not null,
    created_at timestamptz not null default now(),
    unique (org_id, slug)
);

create index accounts_org_idx on accounts (org_id);

-- A local projection of the people an identity provider tells us about.
--
-- This application does not authenticate anyone and stores no credential: no
-- password, no session, no token. Identity is asserted by the access token on
-- each request, and this table exists only so that an audit event can say
-- "Dana Admin" instead of a subject identifier.
--
-- A row is created the first time a subject is seen and refreshed when its
-- claims change. Nothing here is authoritative -- the token is.
create table users (
    id           uuid primary key default gen_random_uuid(),
    org_id       uuid        not null references organizations (id) on delete cascade,
    account_id   uuid        not null references accounts (id) on delete cascade,

    -- The provider's stable identifier for this person: the "sub" claim.
    -- It is the key, because an email address can change and be reassigned.
    subject      text        not null,

    email        text        not null default '',
    display_name text        not null default '',

    -- bcrypt, which stores its own salt and cost inside the value. Null for a
    -- person who cannot sign in with a password -- which is every person in a
    -- deployment that federates, where an identity provider authenticates them
    -- and this column is never written.
    password_hash bytea,

    -- What this person may do. A column because this application signs people
    -- in, so it is also the thing that has to say who is an administrator.
    -- Federate instead and the provider asserts roles in the token; this column
    -- stops being read, and nothing downstream of Claims notices.
    roles        text[]      not null default '{member}',

    created_at   timestamptz not null default now(),
    last_seen_at timestamptz not null default now()
);

-- Unique per account, not globally.
--
-- Two customers federating different identity providers can both emit sub "1"
-- or "admin" -- they are each provider's namespace, not a shared one. With the
-- index on subject alone, the second customer's login flipped the first
-- customer's row to their organization, taking the email and display name with
-- it, and every audit event pointing at that row then resolved to the wrong
-- person in the wrong tenant. The row is tenant-owned like every other, so it
-- is keyed like every other.
create unique index users_subject_uniq on users (org_id, account_id, subject);

-- Signing in starts from an email address and nothing else, so it has to
-- identify exactly one person across the whole table -- not one per account.
-- Case-insensitively, because nobody types their address the same way twice.
create unique index users_email_uniq on users (lower(email)) where email <> '';
create index users_scope_idx on users (org_id, account_id);

-- Credentials for any connector, not just Jira.
--
-- The connector-specific configuration is an encrypted blob whose plaintext is
-- JSON. It is deliberately not a plain jsonb column: the configuration contains
-- the credential, and a credential must not be readable by anyone who can read
-- the table. Each connector decodes this blob into its own configuration
-- struct and validates it before use.
--
-- Fields that are not secret live in `metadata` instead, so listing connections
-- for the UI requires no decryption and no key service round-trip.
create table connections (
    id             uuid primary key default gen_random_uuid(),
    org_id         uuid        not null references organizations (id) on delete cascade,
    account_id     uuid        not null references accounts (id) on delete cascade,
    connector_type text        not null,
    display_name   text        not null default '',

    -- Non-secret, displayable connection details, e.g. site URL and account
    -- email. Never credentials.
    metadata       jsonb       not null default '{}'::jsonb,

    -- The configuration, encrypted by the key service. One opaque blob: the
    -- provider generates and carries its own nonce, so there is nothing else
    -- for this schema to keep correct.
    config_encrypted bytea not null,

    status           text        not null default 'active'
                     check (status in ('active', 'invalid', 'revoked')),
    last_verified_at timestamptz,
    last_error       text,
    created_by       uuid        references users (id) on delete set null,
    created_at       timestamptz not null default now(),
    updated_at       timestamptz not null default now()
);

-- One active connection per connector type per account. Historical rows are
-- kept so that the audit trail retains who connected what and when.
create unique index connections_active_uniq
    on connections (org_id, account_id, connector_type) where status = 'active';

-- API keys authenticate machine callers of the public REST API. As with
-- sessions, only a hash of the secret is stored and the key is shown once.
create table api_keys (
    id           uuid primary key default gen_random_uuid(),
    org_id       uuid        not null references organizations (id) on delete cascade,
    account_id   uuid        not null references accounts (id) on delete cascade,
    key_id       text        not null unique,
    secret_hash  bytea       not null,
    name         text        not null,
    scopes       text[]      not null default '{findings:write}',
    created_by   uuid        references users (id) on delete set null,
    created_at   timestamptz not null default now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);

create index api_keys_scope_idx on api_keys (org_id, account_id);

-- Every issue this application created. This table, not a Jira label, is the
-- source of truth for "tickets created from this app": a label is mutable by
-- any Jira user and can be added to issues we did not create.
create table tickets (
    id              uuid primary key default gen_random_uuid(),
    org_id          uuid        not null references organizations (id) on delete cascade,
    account_id      uuid        not null references accounts (id) on delete cascade,
    connection_id   uuid        references connections (id) on delete set null,
    project_key     text        not null,
    issue_key       text        not null,
    issue_id        text        not null default '',
    issue_url       text        not null,
    summary         text        not null,
    source          text        not null check (source in ('ui', 'api', 'automation')),
    created_by_user uuid        references users (id) on delete set null,
    created_by_key  uuid        references api_keys (id) on delete set null,
    jira_created_at timestamptz,
    created_at      timestamptz not null default now()
);

create unique index tickets_issue_uniq on tickets (org_id, account_id, issue_key);
create index tickets_recent_idx on tickets (org_id, account_id, project_key, created_at desc);

-- Replay protection for the public API: a scanner or CI job that retries after
-- a timeout must not file the same finding twice.
create table idempotency_keys (
    org_id       uuid        not null references organizations (id) on delete cascade,
    account_id   uuid        not null references accounts (id) on delete cascade,
    key          text        not null,
    request_hash bytea       not null,
    ticket_id    uuid        references tickets (id) on delete set null,
    created_at   timestamptz not null default now(),
    primary key (org_id, account_id, key)
);

-- Posts the digest automation has already filed a ticket for. The unique index
-- is the real deduplication guarantee: a replayed or concurrent workflow is
-- rejected by the database rather than by application ordering.
create table blog_posts (
    id           uuid primary key default gen_random_uuid(),
    org_id       uuid        not null references organizations (id) on delete cascade,
    account_id   uuid        not null references accounts (id) on delete cascade,
    post_url     text        not null,
    title        text        not null,
    published_at timestamptz,
    summary      text        not null default '',
    ticket_id    uuid        references tickets (id) on delete set null,
    created_at   timestamptz not null default now()
);

create unique index blog_posts_url_uniq on blog_posts (org_id, account_id, post_url);

-- Append-only record of security-relevant actions. An NHI platform is an audit
-- trail with a user interface attached.
create table audit_events (
    id         bigserial primary key,
    org_id     uuid        not null references organizations (id) on delete cascade,
    account_id uuid        not null references accounts (id) on delete cascade,
    actor_type text        not null check (actor_type in ('user', 'api_key', 'system')),
    actor_id   uuid,
    action     text        not null,
    target     text        not null default '',
    metadata   jsonb       not null default '{}'::jsonb,
    ip         inet,
    created_at timestamptz not null default now()
);

create index audit_events_scope_idx on audit_events (org_id, account_id, created_at desc);

-- +goose Down
drop table if exists audit_events;
drop table if exists blog_posts;
drop table if exists idempotency_keys;
drop table if exists tickets;
drop table if exists api_keys;
drop table if exists connections;
drop table if exists users;
drop table if exists accounts;
drop table if exists organizations;
