import * as React from "react";
import { Link } from "react-router-dom";
import { CheckCircle2, ExternalLink, Play, Plug, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/field";
import { SetupGuide } from "@/components/SetupGuide";
import { Alert, Badge, ErrorAlert, Spinner } from "@/components/ui/feedback";
import {
  useConnect,
  useConnections,
  useDigest,
  useDisconnect,
  useRunDigest,
} from "@/hooks/useApi";
import { useAuth } from "@/hooks/useAuth";
import { formatRelative, temporalURL } from "@/lib/utils";

export function Connections() {
  const { isAdmin } = useAuth();
  const { data, isLoading, error } = useConnections();
  const connect = useConnect();
  const disconnect = useDisconnect();
  const digest = useDigest();
  const runDigest = useRunDigest();

  const [siteUrl, setSiteUrl] = React.useState("");
  const [email, setEmail] = React.useState("");
  const [apiToken, setApiToken] = React.useState("");

  const jira = data?.connections.find((c) => c.connectorType === "jira");

  // What the connector says it needs, and how to get it. The form asks for
  // three named fields because that is what Jira needs; the help text and the
  // setup steps are the connector's own words.
  const jiraType = data?.availableTypes.find((t) => t.type === "jira");

  async function onConnect(event: React.FormEvent) {
    event.preventDefault();
    await connect.mutateAsync({
      connectorType: "jira",
      config: { siteUrl: siteUrl.trim(), email: email.trim(), apiToken: apiToken.trim() },
    });
    // The token is cleared as soon as it has been sent. It is not needed again
    // and should not sit in a form field.
    setApiToken("");
  }

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div>
        <h1 className="text-lg font-semibold text-ink">Connections</h1>
        <p className="text-sm text-muted">
          The issue tracker findings are filed into.
        </p>
      </div>

      <ErrorAlert error={error} />

      <Card>
        <CardHeader
          title="Jira"
          description="Atlassian Jira Cloud"
          action={
            jira ? (
              jira.status === "active" ? (
                <Badge tone="ok">Connected</Badge>
              ) : (
                <Badge tone="danger">Needs attention</Badge>
              )
            ) : (
              <Badge>Not connected</Badge>
            )
          }
        />

        {isLoading ? (
          <Spinner label="Checking connection…" />
        ) : jira ? (
          <CardBody className="space-y-4">
            {jira.status !== "active" && (
              <Alert tone="danger" title="This connection stopped working">
                <p>{jira.lastError || "The stored credential was rejected."}</p>
                <p className="mt-1">Reconnect below to continue filing findings.</p>
              </Alert>
            )}

            <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
              <dt className="text-muted">Site</dt>
              <dd className="text-ink">
                <a
                  href={jira.metadata.siteUrl}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="inline-flex items-center gap-1 hover:underline"
                >
                  {jira.metadata.siteUrl}
                  <ExternalLink className="size-3" aria-hidden />
                </a>
              </dd>

              {/* Whose credential this is, which is the point: a connection acts
                  as an identity, and that identity should be visible. */}
              <dt className="text-muted">Acting as</dt>
              <dd className="text-ink">
                {jira.displayName}
                {jira.metadata.email && (
                  <span className="text-muted"> · {jira.metadata.email}</span>
                )}
              </dd>

              <dt className="text-muted">Last verified</dt>
              <dd className="text-ink">
                {jira.lastVerifiedAt ? formatRelative(jira.lastVerifiedAt) : "—"}
              </dd>
            </dl>

            {isAdmin && (
              <div className="flex justify-end border-t border-line pt-4">
                <Button
                  variant="secondary"
                  size="sm"
                  loading={disconnect.isPending}
                  onClick={() => disconnect.mutate("jira")}
                >
                  Disconnect
                </Button>
              </div>
            )}
          </CardBody>
        ) : (
          <CardBody>
            <div className="flex items-start gap-3 text-sm text-muted">
              <Plug className="mt-0.5 size-4 shrink-0" aria-hidden />
              <p>
                Connect a Jira workspace to start filing findings. The credential is
                encrypted before it is stored and is never shown again.
              </p>
            </div>
          </CardBody>
        )}
      </Card>

      {isAdmin && (
        <Card>
          <CardHeader
            title={jira ? "Reconnect Jira" : "Connect Jira"}
            description="Uses an Atlassian API token. Both scoped and unscoped tokens work."
          />
          <CardBody>
            <form onSubmit={onConnect} className="space-y-4">
              <Field
                label="Site URL"
                htmlFor="siteUrl"
                hint="Your Jira address, with no path."
              >
                <Input
                  type="url"
                  placeholder="https://your-site.atlassian.net"
                  required
                  value={siteUrl}
                  onChange={(e) => setSiteUrl(e.target.value)}
                />
              </Field>

              <Field
                label="Account email"
                htmlFor="jiraEmail"
                hint="The Atlassian account the API token belongs to."
              >
                <Input
                  type="email"
                  placeholder="you@example.com"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                />
              </Field>

              <Field
                label="API token"
                htmlFor="apiToken"
                hint={jiraType?.fields.find((f) => f.name === "apiToken")?.help}
              >
                <Input
                  id="apiToken"
                  type="password"
                  autoComplete="off"
                  required
                  value={apiToken}
                  onChange={(e) => setApiToken(e.target.value)}
                />
                {/* The steps come from the connector, so a second integration
                    brings its own without touching this page. */}
                <div className="pt-1">
                  <SetupGuide
                    title="Creating an Atlassian API token"
                    steps={jiraType?.setup ?? []}
                  />
                </div>
              </Field>

              <ErrorAlert error={connect.error} />

              {connect.isSuccess && !connect.isPending && (
                <Alert tone="ok" title="Connected">
                  <p className="inline-flex items-center gap-1">
                    <CheckCircle2 className="size-3" aria-hidden />
                    The credential was verified against Jira before it was stored.
                  </p>
                </Alert>
              )}

              <div className="flex items-center justify-between gap-4">
                <p className="text-xs text-muted">
                  Connecting also starts the daily NHI blog digest.
                </p>
                <Button type="submit" loading={connect.isPending}>
                  {jira ? "Reconnect" : "Connect"}
                </Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      {/* The digest is external to the interface by design -- nothing here has
          to be clicked for it to run. It is shown because a scheduled feature
          that only proves itself a day later is one nobody checks, and "does it
          work?" deserves an answer that is not "wait until tomorrow". */}
      {jira?.status === "active" && (
        <Card>
          <CardHeader
            title="NHI Blog Digest"
            description="Reads the Oasis blog on a schedule, summarises each new post, and files a ticket for it."
          />
          <CardBody>
            {digest.isLoading ? (
              <Spinner label="Loading…" />
            ) : digest.data?.scheduled ? (
              <div className="space-y-4">
                <dl className="grid gap-4 sm:grid-cols-3">
                  <div>
                    <dt className="text-xs text-muted">Files into</dt>
                    <dd className="text-sm text-ink">
                      {digest.data.projectKey || "the first project available"}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-xs text-muted">Next run</dt>
                    <dd className="text-sm text-ink">
                      {digest.data.nextRunAt ? formatRelative(digest.data.nextRunAt) : "—"}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-xs text-muted">Last run</dt>
                    <dd className="text-sm text-ink">
                      {digest.data.lastRunAt ? formatRelative(digest.data.lastRunAt) : "never"}
                    </dd>
                  </div>
                </dl>

                <ErrorAlert error={runDigest.error} />

                {runDigest.isSuccess && !runDigest.isPending && (
                  <Alert tone="ok" title="Run started">
                    <p>
                      Tickets appear in{" "}
                      <Link to="/tickets" className="text-accent hover:underline">
                        Recent tickets
                      </Link>{" "}
                      as they are filed. A post already catalogued is skipped, so
                      running again files nothing twice.
                    </p>
                  </Alert>
                )}

                <div className="flex items-center gap-3">
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => runDigest.mutate()}
                    loading={runDigest.isPending}
                  >
                    <Play className="size-4" aria-hidden />
                    Run now
                  </Button>
                  <Button asChild size="sm" variant="ghost">
                    <a
                      href={temporalURL("/namespaces/identityhub/schedules")}
                      target="_blank"
                      rel="noreferrer noopener"
                    >
                      Open in Temporal
                      <ExternalLink className="size-3" aria-hidden />
                    </a>
                  </Button>
                  <p className="text-xs text-muted">
                    Every {digest.data.everyHours} hours. Running now does not change the schedule.
                  </p>
                </div>
              </div>
            ) : (
              <p className="text-sm text-muted">
                No digest is scheduled. One is created when an integration is connected.
              </p>
            )}
          </CardBody>
        </Card>
      )}

      {!isAdmin && (
        <Alert tone="info" title="Read only">
          <p className="inline-flex items-center gap-1">
            <TriangleAlert className="size-3" aria-hidden />
            Only an account administrator can change integration settings.
          </p>
        </Alert>
      )}
    </div>
  );
}
