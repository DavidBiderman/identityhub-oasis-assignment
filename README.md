# IdentityHub

A proof-of-concept for reporting Non-Human Identity findings into Jira: a web
interface for people, a REST API for scanners and CI pipelines, and a scheduled
automation that files a ticket for each new post on the Oasis blog.

---

## How to run

```bash
docker compose up -d
```

That is the whole setup. It brings up PostgreSQL, Redis, a local KMS, Temporal,
the API, the worker and the web interface, applies the database schema, creates
the encryption key, and seeds the demo data described below.

The first run builds the images, so it takes a few minutes; after that the stack
comes up quickly.

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

### The organization and account IDs

They live in the token's claims and are never sent by a caller, but they are
readable directly if you want to check what the isolation is isolating:

```bash
docker compose exec postgres psql -U identityhub -d identityhub -c \
  "select o.slug as org, o.id as org_id, a.slug as account, a.id as account_id
     from organizations o join accounts a on a.org_id = o.id order by a.slug;"
```

---

## API documentation

<http://localhost:3000/docs> renders `api/openapi.yaml` — the same document that
generates the server's routing and decides which operations require which
credential, so it cannot describe an endpoint that does not exist.

**Sign in on that page with the same credentials as the application.** The
document itself requires a token, and signing in there fills the authorization
in for you, so **Try it out** works on the first click with nothing to paste.
Sign in to the application first and the page skips the form entirely: one
origin, one session.

### Why there are two kinds of credential

The brief asks for two different callers — people using an interface, and
scanners and CI pipelines calling an API — and they need different credentials
for reasons that are not interchangeable:

| | Access token | API key |
|---|---|---|
| Who holds it | A person, in their browser | A machine: a scanner, a CI job |
| Looks like | A signed JWT | `ih_<id>_<secret>` |
| Obtained by | Signing in | Minting one in the interface |
| Lives for | One hour | Until it is revoked |
| Opens | `/api/*` — **15 of the 20 endpoints** | `POST /api/v1/findings`, and nothing else |
| Revoked by | Signing out, immediately | Revoking it in the interface |

A scanner cannot complete an interactive sign-in, so it cannot hold the first.
A person should not carry a credential that lives until somebody remembers to
revoke it, so they should not hold the second. **Neither is a substitute for the
other**: an API key gets a `401` from every endpoint except the one it is for,
and an access token gets a `401` from that one.

`GET /api/tickets/{id}` is the single exception and accepts either, because the
`201` from `POST /api/v1/findings` returns a `Location` pointing at it — and
refusing the key there would hand a caller a link it cannot follow.

### Using each one

**The access token** is filled in automatically when you sign in on the
documentation page. To get one for `curl` or Postman instead:

```bash
make token                          # admin@acme.test
EMAIL=secops@acme.test make token   # a different account, to see the isolation
```

That signs in as a seeded person and prints the token — the same request the
interface makes. Note that it prints an **access token, not an API key**; the
two are not interchangeable.

```bash
curl -H "Authorization: Bearer $(make token)" http://localhost:8080/api/tickets
```

**The API key** is minted in the interface, under **API keys**. It is shown once
and stored only as a hash, so copy it when it appears. In the documentation
page, press **Authorize** and paste it into the `apiKey` field — leave that
field empty otherwise, since only one endpoint reads it:

```bash
curl -X POST http://localhost:8080/api/v1/findings \
  -H "Authorization: Bearer ih_..." \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: scan-run-42" \
  -d '{"projectKey":"NHI","title":"Stale service account: svc-deploy-prod",
       "description":"Last used 400 days ago."}'
```

Repeat that request with the same `Idempotency-Key` and it returns the original
ticket with `created: false` rather than filing a second one.

---

## Design documents

| | |
|---|---|
| [docs/DECISIONS.md](docs/DECISIONS.md) | Architecture, and the reasoning behind every significant choice. The table at the top is the whole document in ten minutes |
| [docs/FLOWS.md](docs/FLOWS.md) | What happens on each path, drawn: a request arriving, connecting Jira, filing a finding, the digest |
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
    config/        environment parsing, validated once at startup
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

Running the tests is the one thing that needs tools on your machine, because
they run on the host rather than in a container: **Go 1.26+ and Node 22+**, plus
`jq` for `make token`. On macOS:

```bash
brew bundle      # go, node, jq, and the two generators below
make tools       # oapi-codegen, which Homebrew does not carry
```

Then:

```bash
cd backend && go test ./...
```

Queries and API types are generated, so after changing anything in `db/queries`,
`db/migrations` or `api/openapi.yaml`:

```bash
make generate
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
