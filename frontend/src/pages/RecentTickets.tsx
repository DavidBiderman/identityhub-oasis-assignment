import * as React from "react";
import { Link } from "react-router-dom";
import { ExternalLink, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Combobox } from "@/components/ui/combobox";
import { Card, CardHeader } from "@/components/ui/card";
import { Badge, EmptyState, ErrorAlert, Spinner } from "@/components/ui/feedback";
import { useProjects, useTickets } from "@/hooks/useApi";
import { ApiError } from "@/lib/api";
import { formatDateTime, formatRelative } from "@/lib/utils";
import type { TicketSource } from "@/lib/types";

/** Where a ticket came from, in words rather than a raw enum value. */
const sourceLabel: Record<TicketSource, string> = {
  ui: "Filed here",
  api: "API",
  automation: "Digest",
};

export function RecentTickets() {
  // Which project's tickets to show. Empty means every project, which is the
  // more useful default: someone opening this page wants to know what was filed,
  // not to choose a project first.
  const [projectKey, setProjectKey] = React.useState("");

  const projects = useProjects(true);
  const tickets = useTickets(projectKey);
  const notConnected = tickets.error instanceof ApiError && tickets.error.needsConnection;

  // "All projects" is a real choice, so it is an option rather than a cleared
  // input.
  const projectOptions = React.useMemo(
    () => [
      { value: "", label: "All projects" },
      ...(projects.data ?? []).map((p) => ({ value: p.key, label: p.name, hint: p.key })),
    ],
    [projects.data],
  );

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <div className="flex items-end justify-between gap-4">
        <div>
          <h1 className="text-lg font-semibold text-ink">Recent tickets</h1>
          <p className="text-sm text-muted">
            The ten most recent findings filed from IdentityHub
            {projectKey ? ` into ${projectKey}` : ""}.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <div className="w-56">
            <label htmlFor="filterProject" className="sr-only">
              Filter by project
            </label>
            <Combobox
              id="filterProject"
              options={projectOptions}
              value={projectKey}
              onChange={setProjectKey}
              loading={projects.isLoading}
              placeholder="All projects"
              emptyMessage="No project matches."
            />
          </div>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void tickets.refetch()}
            loading={tickets.isFetching}
          >
            <RefreshCw className="size-4" aria-hidden />
            Refresh
          </Button>
        </div>
      </div>

      {!notConnected && <ErrorAlert error={tickets.error} />}

      <Card>
        <CardHeader
          title="Filed from this application"
          description="Tickets created directly in Jira do not appear here."
        />

        {tickets.isLoading ? (
          <Spinner label="Loading tickets…" />
        ) : tickets.data && tickets.data.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-line text-left text-xs text-muted">
                  <th scope="col" className="px-5 py-2 font-medium">Issue</th>
                  <th scope="col" className="px-5 py-2 font-medium">Title</th>
                  <th scope="col" className="px-5 py-2 font-medium">Status</th>
                  <th scope="col" className="px-5 py-2 font-medium">Source</th>
                  <th scope="col" className="px-5 py-2 font-medium">Created</th>
                </tr>
              </thead>
              <tbody>
                {tickets.data.map((ticket) => (
                  <tr key={ticket.id} className="border-b border-line last:border-0">
                    <td className="px-5 py-3">
                      {/* Opens in a new tab, as the brief requires. rel guards
                          the opener from the new page. */}
                      <a
                        href={ticket.issueUrl}
                        target="_blank"
                        rel="noreferrer noopener"
                        className="inline-flex items-center gap-1 font-medium text-accent hover:underline"
                      >
                        {ticket.issueKey}
                        <ExternalLink className="size-3" aria-hidden />
                      </a>
                    </td>
                    <td className="max-w-sm px-5 py-3 text-ink">{ticket.title}</td>
                    <td className="px-5 py-3">
                      {ticket.status ? (
                        <Badge>{ticket.status}</Badge>
                      ) : (
                        <span className="text-xs text-muted">—</span>
                      )}
                    </td>
                    <td className="px-5 py-3">
                      <Badge tone={ticket.source === "ui" ? "accent" : "neutral"}>
                        {sourceLabel[ticket.source] ?? ticket.source}
                      </Badge>
                    </td>
                    {/* Relative time is what people scan for; the exact
                        timestamp is on hover for when it matters. */}
                    <td className="whitespace-nowrap px-5 py-3">
                      {/* The timestamp, and the relative time under it. An
                          audit trail is read to answer "when exactly", and
                          "3 hours ago" hidden behind a tooltip does not answer
                          it — but the relative form is what makes a list
                          scannable, so both are shown. */}
                      <div className="text-ink">{formatDateTime(ticket.createdAt)}</div>
                      <div className="text-xs text-muted">{formatRelative(ticket.createdAt)}</div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : notConnected ? (
          <EmptyState
            title="No issue tracker connected"
            description="Connect Jira to start filing findings."
            action={
              <Button asChild size="sm" variant="secondary">
                <Link to="/connections">Go to connections</Link>
              </Button>
            }
          />
        ) : projectKey ? (
          // A filter that matches nothing is a different situation from having
          // filed nothing, and the remedy is different too.
          <EmptyState
            title={`Nothing filed into ${projectKey}`}
            description="Findings may have been filed into another project."
            action={
              <Button size="sm" variant="secondary" onClick={() => setProjectKey("")}>
                Show all projects
              </Button>
            }
          />
        ) : (
          <EmptyState
            title="No findings yet"
            description="Findings filed here, through the API, or by the blog digest will appear in this list."
            action={
              <Button asChild size="sm">
                <Link to="/findings/new">File a finding</Link>
              </Button>
            }
          />
        )}
      </Card>
    </div>
  );
}
