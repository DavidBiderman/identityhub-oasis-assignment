# IdentityHub

A proof-of-concept for reporting Non-Human Identity findings into Jira: a web
interface for people, a REST API for scanners and CI pipelines, and a scheduled
automation that files a ticket for each new post on the Oasis blog.

---

## Run it

```bash
docker compose up --build
```

That is the whole setup. It brings up PostgreSQL, Redis, a local KMS, Temporal,
the API, the worker and the web interface, applies the database schema, creates
the encryption key, and seeds the demo data described below.

| | |
|---|---|
| **Application** | <http://localhost:3000> |
| **API** (for `curl`) | <http://localhost:8080> |
| **API documentation** | <http://localhost:3000/docs> — sign in, then every endpoint is live with Try it out |
| **Temporal** | <http://localhost:8233> — workflows and the digest schedule. Served by the Temporal dev server itself, so the stack has one Temporal container rather than two |

Nothing needs to be configured first. No cloud account, no API keys, no
`.env` file. To connect a real Jira workspace, see
[docs/CONNECTING-JIRA.md](docs/CONNECTING-JIRA.md).

---

## Seeded data

The `bootstrap` service runs before the API starts and creates everything the
flows need. It is idempotent, so restarting the stack changes nothing.

### Sign in

Three seeded people, in two accounts inside one organization. **The password is
`identityhub-demo` for all three.**

| Email | Password | Role | Account |
|---|---|---|---|
| `admin@acme.test` | `identityhub-demo` | admin | Platform Engineering |
| `member@acme.test` | `identityhub-demo` | member | Platform Engineering |
| `secops@acme.test` | `identityhub-demo` | admin | Security Operations |

Sign in as two of them in separate browser profiles to see that the accounts
cannot see each other's connections or tickets. `member@acme.test` is there to
show the other half: a member can file findings but cannot connect Jira.

**In a real deployment this page does not exist.** An identity provider — Auth0,
Clerk, Okta — authenticates the person and issues the token, and this
application reads it either way. Passwords are here because the exercise asks
for login; the token they produce is the same token every other path consumes,
which is what makes that swap a change to one file. See
[docs/DECISIONS.md](docs/DECISIONS.md) §2a.

### The tenancy it creates

```
Acme Corp                        organization
├── Platform Engineering         account   ← admin@acme.test, member@acme.test
└── Security Operations          account   ← secops@acme.test
```

**Two accounts inside one organization, on purpose.** Tenancy here is two layers
deep, and the isolation claim is only checkable if there is a sibling account to
check it against. Act as `admin@acme.test`, connect Jira and file a finding;
then act as `secops@acme.test` and confirm that neither the ticket nor the
Jira connection is visible, even though both users belong to the same
organization.

The two roles in the Platform Engineering account exist for the same reason:
`member@acme.test` cannot manage integrations or API keys, and gets a `403` that
says so.

### Re-running the seed

It skips when the demo organization already exists. To recreate it — which
deletes the demo data and everything belonging to it:

```bash
docker compose run --rm bootstrap -force
```

Other flags: `-skip-kms` and `-skip-seed` run one half without the other.

### Getting a token for the REST API

`make token` signs in as a seeded person and prints the token, which is what the
interface does. To use one from `curl` or from the API documentation:

```bash
make token                          # admin@acme.test
EMAIL=secops@acme.test make token   # a different account, to see isolation
```

The organization and account IDs live in the token's claims, and are also
readable directly:

```bash
docker compose exec postgres psql -U identityhub -d identityhub -c \
  "select o.slug as org, o.id as org_id, a.slug as account, a.id as account_id
     from organizations o join accounts a on a.org_id = o.id order by a.slug;"
```

---

## What to try

**1. File a finding from the interface.** Sign in, connect Jira, pick a
project, submit. The ticket appears in Jira and in Recent tickets with a link
to it.

**2. Watch it run.** Open <http://localhost:8233>. Every ticket is filed by a
Temporal workflow, so each one is a durable execution you can inspect —
including its activities, its inputs and any retries.

**3. File one as a scanner would.** Create an API key in the interface, then:

```bash
curl -X POST http://localhost:8080/api/v1/findings \
  -H "Authorization: Bearer ih_..." \
  -H "Idempotency-Key: nightly-scan-001" \
  -H "Content-Type: application/json" \
  -d '{"projectKey":"NHI","title":"Stale service account: svc-legacy-etl",
       "description":"No authentication in 412 days."}'
```

Run it twice. The second call returns `200` with the original ticket rather than
`201` with a second one — a CI job that retries after a timeout must not file
the same finding twice.

**4. Check the isolation.** Switch identity to `secops@acme.test`. No tickets, no
connection, despite sharing an organization with the account that has both.

**5. Try to smuggle a tenancy.** Add `"accountId"` to any request body. It
succeeds, and the write lands in the account the *token* names. Nothing rejects
the field because nothing reads it: the request types have no such field, so the
value is inert. The property is the missing field, not a check that could be
forgotten — and a `400` would only have proved that something checked.

**6. Read the API, and try it.** Open <http://localhost:3000/docs> and sign in
with the same credentials as the application. **The document itself requires a
token** — it lists every endpoint, field and constraint of an API that manages
credentials, and publishing that to anyone who asks hands an attacker the map.

Signing in there fills in the authorization for you, so **Try it out** works on
the first click with nothing to paste. Sign in to the application first and the
page skips the form: one origin, one session. The page renders
`api/openapi.yaml`, the same document that generates the server's routing and
decides which operations require which credential — so it cannot describe an
endpoint that does not exist.

**7. See the digest schedule.** Connecting Jira creates a Temporal schedule that
runs every 24 hours, reads the Oasis blog, skips posts already catalogued, and
files a ticket for each new one. It is visible under **Schedules** in the
Temporal interface, where you can also trigger a run immediately rather than
waiting a day. It files into the first project the account can reach unless
`DIGEST_PROJECT_KEY` names one.

---

## Documentation

| | |
|---|---|
| [docs/DECISIONS.md](docs/DECISIONS.md) | Architecture, and the reasoning behind every significant choice |
| [docs/CONNECTING-JIRA.md](docs/CONNECTING-JIRA.md) | Connecting a Jira workspace, which API scopes are needed and why |

---

## Layout

```
backend/
  db/
    migrations/  schema, applied by goose
    queries/     every query, compiled by sqlc
  cmd/
    api/         HTTP API
    worker/      Temporal workflows and activities
    migrate/     schema migrations
    bootstrap/   encryption key + demo data
  internal/
    httpapi/       routing, authentication middleware, request and response shapes
    auth/          sign-in, access tokens, API keys, roles and revocation
    crypto/        encrypt and decrypt, over the Go CDK
    cache/         the Redis connection: signed-out tokens, keys with deadlines
    connector/     the provider port  ── jira/  the Jira implementation
    capabilities/  what the product asks of a connector ── issuetracker/
    connections/   an account's stored credential, decrypted and ready to act
    workflows/     durable execution
    store/         tenant-scoped data access over sqlc-generated queries
    blog/          blog reader for the digest
    summarize/     Claude and offline summarizers
frontend/        React single-page application, served by nginx
docs/
```

---

## Tests

```bash
cd backend && go test ./...
```

Queries are generated, so after changing anything in `db/queries` or
`db/migrations`:

```bash
cd backend && sqlc generate
```

Tests that need infrastructure skip themselves when it is absent, so this runs
on a clean machine. With the stack up, they run for real:

```bash
export TEST_DATABASE_URL="postgres://identityhub_app:identityhub_app@localhost:5432/identityhub?sslmode=disable"
export TEST_TEMPORAL_HOSTPORT=localhost:7233
# Database 1, so a test run cannot expire the sign-outs of the stack you are using.
export TEST_REDIS_URL=redis://localhost:6379/1
export AWS_ENDPOINT_URL=http://localhost:4566 AWS_REGION=us-east-1
export AWS_ACCESS_KEY_ID=local AWS_SECRET_ACCESS_KEY=local
go test ./...
```

That adds end-to-end tests over the real HTTP handlers, real PostgreSQL, real
Redis, real KMS and a real Temporal worker, with only Jira stubbed — so the suite
runs on a clean machine with nothing but Docker, and needs nobody's Atlassian
credentials. What was verified by hand against a live site is listed in
[docs/CONNECTING-JIRA.md](docs/CONNECTING-JIRA.md).

---

## Configuration

Every value has a working default; the table is what exists, not what must be
set.

| Variable | Default | |
|---|---|---|
| `APP_ENV` | `development` | `production` requires an https public URL, refuses the local key provider, and refuses both published defaults — the database password and the token signing key |
| `WEB_PORT` | `3000` | Where the interface is published |
| `API_PORT` | `8080` | Direct API access, for `curl` |
| `REDIS_PORT` | `6379` | Where signed-out tokens are remembered until they expire |
| `REDIS_URL` | `redis://redis:6379/0` | Where the API records a sign-out. A hard dependency: if it is unreachable the API refuses every request rather than honouring a token it cannot check |
| `SECRETS_KEEPER_URL` | `awskms://alias/identityhub` | Key provider. Any Go CDK scheme: `awskms://`, `gcpkms://`, `azurekeyvault://`, `hashivault://`, `base64key://` |
| `TOKEN_SIGNING_KEY` | `dev-signing-key-not-for-production` | Signs and verifies every access token, HMAC-SHA256. Published here, so it is refused when `APP_ENV=production`: anyone holding it can mint a token for any tenant |
| `TOKEN_TTL` | `1h` | How long a token lasts. There is no refresh flow |
| `TOKEN_ISSUER` | `https://identityhub.local` | Stamped on a token's `iss` claim, and checked against it |
| `TOKEN_AUDIENCE` | `identityhub` | Stamped on a token's `aud` claim, and checked against it |
| `APP_DB_PASSWORD` | `identityhub_app` | Password for the role the API and worker connect as. Published here, so it is refused when `APP_ENV=production` |
| `DIGEST_PROJECT_KEY` | — | Where the blog digest files. An override: unset, it uses the first project the connected account can reach |
| `SUMMARIZER_PROVIDER` | `auto` | `anthropic`, `openai`, `offline`, or `auto` to use whichever key is set |
| `ANTHROPIC_API_KEY` | — | Optional. Without any model key the digest summarizes offline, so the stack runs with no account and no network |
| `OPENAI_API_KEY` | — | Optional alternative to the above |
| `BLOG_FEED_URL` | `https://www.oasis.security/blog` | Source for the digest |

---

## Notes

- **The Jira credential is itself a non-human identity**, and is treated as one:
  encrypted by a key service with the tenancy sealed inside the ciphertext,
  revocable, never displayed after entry, and every use attributed in the audit
  trail.
- **The tenancy is in the token, and the token is signed.** Organization,
  account and roles are claims, checked on every request against an HMAC-SHA256
  signature — so acting in another tenancy means forging a signature, not
  editing a request. No request body, query parameter or header names a tenancy;
  the request types have no field for one.
- **Signing in is the part a real deployment replaces.** Passwords are bcrypt
  and this application issues the token; federate instead and Auth0 or Clerk
  does both. The seam is `auth.TokenResolver`, and the token it produces is
  identical either way. See [docs/DECISIONS.md](docs/DECISIONS.md) §2a.
- **The stack is self-contained.** The local KMS speaks the real AWS KMS
  protocol, so the key-management code path in development is the one that runs
  against AWS KMS in production — only `SECRETS_KEEPER_URL` differs.
- **`docker compose down -v` removes the volumes**, including the encryption
  key. Stored credentials cannot be decrypted afterwards and must be
  reconnected. `docker compose down` on its own is safe.
- **`make test` leaves the demo data alone.** The end-to-end tests seed their own
  organization and delete it afterwards, so running them does not change what a
  reviewer sees in the interface.
