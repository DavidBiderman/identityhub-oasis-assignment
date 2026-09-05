# Connecting Jira

How to give IdentityHub access to a Jira Cloud workspace, what permissions it
asks for, and why.

This document has two audiences. The first half is the user-facing walkthrough —
it is the source for the "How to connect Jira" copy in the app. The second half
is the reference behind it: which scopes map to which API calls, and why the
choices were made.

---

## Before you start

You need:

- a Jira Cloud site — a free one at [atlassian.com](https://www.atlassian.com/software/jira/free) is enough,
- a project to file findings into, and its **project key** (the short uppercase
  prefix on issue numbers, e.g. `NHI` in `NHI-42`),
- an Atlassian account that can **Browse Projects** and **Create Issues** in it.

---

## Step 1 — Create an API token

> ### Make sure you are in the right place
>
> Atlassian has three different things called an "API key" or "API token", and
> only one of them works here.
>
> | Where | What it is | Use it here? |
> |---|---|---|
> | **id.atlassian.com** → Security → API tokens | Your Atlassian account token | **Yes** |
> | admin.atlassian.com → Settings → API keys | Organization admin API — users, groups, domains, policies | No |
> | developer.atlassian.com/console | OAuth 2.0 (3LO) app for third-party integrations | No |
>
> If the scope list you are looking at is full of names ending in `:admin`
> — `read:accounts:admin`, `write:groups:admin` — you are on the organization
> admin page. That key cannot touch a Jira issue. Go to id.atlassian.com
> instead.

1. Open **<https://id.atlassian.com/manage-profile/security/api-tokens>**.
2. Choose **Create API token with scopes**.
3. Name it something recognisable, e.g. `identityhub`.
4. Set an expiry. Prefer the shortest that covers your needs — a token you have
   to renew is a token you remember exists.
5. Select the app: **Jira**.
6. Select these three scopes:

   ```
   read:jira-user
   read:jira-work
   write:jira-work
   ```

7. Copy the token. **Atlassian shows it once.** If you lose it, revoke it and
   create another rather than leaving an unaccounted-for credential live.

### If you would rather not use scopes

Choosing **Create API token** without scopes also works. It grants the full
permissions of your Atlassian account, so the scoped token is the better choice
where it is available.

Either kind connects the same way — IdentityHub works out which Atlassian API
entry point your token needs. See [Two entry points](#two-entry-points) for what
it is doing and why.

---

## Step 2 — Connect it in IdentityHub

Sign in, open **Connections**, and enter:

| Field | Value | Example |
|---|---|---|
| **Site URL** | Your Jira site, with no path | `https://your-site.atlassian.net` |
| **Email** | The Atlassian account the token belongs to | `you@example.com` |
| **API token** | The token from step 1 | `ATATT3xFfGF0…` |

IdentityHub verifies the credential against Jira before storing it, so a typo
fails immediately with a specific message rather than silently later. On success
the connection shows which Jira account it acts as.

The token is encrypted before it is written down, and is never shown again — not
in the interface, not in the API, not in logs.

---

## Step 3 — File a finding

Open **New finding**, choose your project from the picker, give the finding a
title and description, and submit. The ticket appears in Jira, and in **Recent
tickets** with a link straight to it.

---

## Troubleshooting

| What you see | What it means | What to do |
|---|---|---|
| *Jira rejected the stored credential* | The token is wrong, expired, or revoked | Create a new token and reconnect |
| *The connected Jira account does not have permission* | The token works, but the account lacks a Jira permission | Grant **Browse Projects** and **Create Issues** on the project |
| *Project … was not found* | The project key is wrong, or invisible to this account | Check the key in Jira's project list |
| *Jira rejected the request* | A field the project requires was not supplied | Check whether the project has mandatory custom fields |
| *Could not reach the Jira site* | The site URL is wrong or unreachable | Confirm it is `https://<site>.atlassian.net` with no path |
| *Jira rejected the credential at both the site and the Atlassian API gateway* | The email or token is wrong, or the token lacks the scopes | Recheck the email, and that the token has all three scopes |

### Two entry points

Atlassian serves the same API at two addresses, and which one accepts a
credential depends on how the token was created:

| Token | Address |
|---|---|
| Created **without** scopes | `https://your-site.atlassian.net/rest/api/3/…` |
| Created **with** scopes | `https://api.atlassian.com/ex/jira/{cloudId}/rest/api/3/…` |

Using the wrong one returns a bare `401` with nothing to indicate why — a
genuinely hard error to diagnose, and Atlassian's own interface does not make
the distinction obvious.

**You do not need to care.** IdentityHub tries the site first, and on a `401`
resolves the site's cloud ID and retries through the gateway, remembering the
answer. The cloud ID comes from `/_edge/tenant_info`, which is public and
unauthenticated, so this costs no extra credential and no configuration.

Only a `401` triggers the fallback. A `403` means the credential authenticated
and then lacked a Jira permission — retrying that elsewhere would turn a precise
permissions error into a misleading credential one.

If you want to check by hand which kind of token you hold:

```bash
SITE=your-site.atlassian.net
EMAIL=you@example.com
TOKEN=<paste>

# 200 for a token created WITHOUT scopes
curl -s -o /dev/null -w 'site:    %{http_code}\n' \
  -u "$EMAIL:$TOKEN" "https://$SITE/rest/api/3/myself"

# 200 for a token created WITH scopes
CLOUD=$(curl -s "https://$SITE/_edge/tenant_info" | sed -E 's/.*"cloudId":"([^"]+)".*/\1/')
curl -s -o /dev/null -w 'gateway: %{http_code}\n' \
  -u "$EMAIL:$TOKEN" "https://api.atlassian.com/ex/jira/$CLOUD/rest/api/3/myself"
```

---

# Reference

## What IdentityHub actually calls

Five endpoints, and nothing else. The scopes below are quoted from Atlassian's
own OpenAPI specification rather than from documentation prose.

| Call | Why | Classic scope | Granular scopes |
|---|---|---|---|
| `GET /rest/api/3/myself` | Confirm the credential works and show whose it is | `read:jira-user` | `read:user:jira`, `read:application-role:jira`, `read:group:jira`, `read:avatar:jira` |
| `GET /rest/api/3/project/search` | Populate the project picker | `read:jira-work` | `read:project:jira`, `read:issue-type:jira`, `read:project.property:jira`, `read:project-category:jira`, `read:project-version:jira`, `read:project.component:jira`, `read:issue-type-hierarchy:jira`, `read:user:jira`, `read:application-role:jira`, `read:group:jira`, `read:avatar:jira` |
| `GET /rest/api/3/issue/createmeta/{key}/issuetypes` | Discover which issue types a project accepts | `read:jira-work` | `read:issue-meta:jira`, `read:field-configuration:jira`, `read:avatar:jira` |
| `POST /rest/api/3/issue` | File the finding | `write:jira-work` | `write:issue:jira`, `write:comment:jira`, `write:comment.property:jira`, `write:attachment:jira`, `read:issue:jira` |
| `POST /rest/api/3/issue/bulkfetch` | Show current status in the recent tickets list | `read:jira-work` | `read:issue:jira`, `read:issue-meta:jira`, `read:status:jira`, `read:issue-security-level:jira`, `read:issue.changelog:jira`, `read:issue.vote:jira`, `read:field-configuration:jira`, `read:user:jira`, `read:avatar:jira` |

Nothing here edits, transitions, deletes, or comments on an issue.

## Why three classic scopes rather than twenty-two granular ones

Deduplicated, the granular set for the same five calls is 22 scopes. Choosing
the classic three is a deliberate trade-off, not the lazy option:

**For granular.** It is genuinely less privilege. `write:jira-work` grants every
write in Jira — issues, projects, boards, sprints, worklogs, filters — whereas
`write:issue:jira` grants writes to issues. A longer list of narrow scopes is
less access than a short list of broad ones, which is worth saying plainly
because the opposite is the intuitive reading.

**Against granular.** Atlassian marks the granular scopes **Beta** in its own
specification; the classic ones are **Current**. A 22-line scope list is also
one nobody reviews carefully, and an unreviewed list is not really least
privilege — it is least privilege in appearance.

**The decision.** Three Current scopes, documented against the exact calls that
need them, in a table short enough that a reviewer can check every row. If this
were production and the granular scopes were GA, the answer would flip.

## A limit worth naming

`write:issue:jira` does not stand alone. Atlassian bundles `write:comment:jira`
and `write:attachment:jira` with the create-issue endpoint, because that
endpoint can create comments and attachments in the same call. IdentityHub uses
neither.

So least privilege is bounded by how the provider chose to slice its scopes, not
only by what the client actually calls. Worth knowing before claiming a
integration is minimally privileged.

## The credential is itself a non-human identity

A long-lived Jira API token is exactly the kind of artifact this product exists
to find: a machine credential with write access to a SaaS system, which does not
rotate, does not expire by default, and belongs to a person who may leave.

IdentityHub treats it accordingly — encrypted under a per-account data key,
revocable, never displayed after entry, and every use attributed in the audit
trail. See [DECISIONS.md](DECISIONS.md) §3 and §5.

Two consequences worth acting on:

- **Prefer a dedicated Atlassian account** over a personal one. A token minted
  from someone's own login is a human identity doing a machine's job, which is
  the finding rather than the fix. Note that Atlassian service accounts issue
  scoped tokens, so they require the gateway path.
- **Set a short expiry.** The connections screen shows when a credential was
  last verified, so an expired one is visible rather than silent.

## Verified against a real site, through the whole stack

Every flow described here has been exercised against a live Atlassian site
through the running Compose stack: connect and verify a credential, discover
projects and issue types, file a finding from the interface, file one through
the public API with an `Idempotency-Key`, replay it and get the original ticket
back rather than a second one, read current status back from Jira, and see a
revoked token reported as a credential problem rather than an outage.

The tests in this repository stub Atlassian rather than calling it. That is
deliberate: a test suite that needs someone's Jira credentials is a suite most
people cannot run, and `make test` is meant to work on a clean machine with
nothing but Docker. What the stub cannot prove — that Atlassian's API behaves as
the connector expects — was proven by hand against a real site during
development.

`backend/.env` holds a live credential when one is configured, and is listed in
`.gitignore`.

## Related

- [DECISIONS.md](DECISIONS.md) — architecture and the reasoning behind it
- [Atlassian: Manage API tokens](https://support.atlassian.com/atlassian-account/docs/manage-api-tokens-for-your-atlassian-account/)
- [Jira Cloud platform REST API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/)
