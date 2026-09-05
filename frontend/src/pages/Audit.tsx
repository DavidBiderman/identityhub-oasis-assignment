import { Card, CardHeader } from "@/components/ui/card";
import { Badge, EmptyState, ErrorAlert, Spinner } from "@/components/ui/feedback";
import { useAuditEvents } from "@/hooks/useApi";
import { formatDateTime, formatRelative } from "@/lib/utils";

/** Actions in plain language, so the trail reads without a decoder ring. */
const actionLabel: Record<string, string> = {
  "user.signed_out": "Signed out",
  "jira.connected": "Connected Jira",
  "jira.disconnected": "Disconnected Jira",
  "jira.credential_rejected": "Jira credential rejected",
  "api_key.created": "Created API key",
  "api_key.revoked": "Revoked API key",
  "ticket.created": "Filed a finding",
  "ticket.failed": "Finding could not be filed",
  "digest.ran": "Blog digest filed a finding",
  "digest.failed": "Blog digest could not file a post",
};

const actorLabel: Record<string, string> = {
  user: "Person",
  api_key: "API key",
  system: "Automation",
};

export function Audit() {
  const events = useAuditEvents();

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <div>
        <h1 className="text-lg font-semibold text-ink">Audit trail</h1>
        <p className="text-sm text-muted">
          Every security-relevant action in this account, and who took it.
        </p>
      </div>

      <ErrorAlert error={events.error} />

      <Card>
        <CardHeader
          title="Recent activity"
          description="Append-only: the application can add events but not change or remove them."
        />

        {events.isLoading ? (
          <Spinner label="Loading activity…" />
        ) : events.data && events.data.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-line text-left text-xs text-muted">
                  <th scope="col" className="px-5 py-2 font-medium">Action</th>
                  <th scope="col" className="px-5 py-2 font-medium">Actor</th>
                  <th scope="col" className="px-5 py-2 font-medium">Target</th>
                  <th scope="col" className="px-5 py-2 font-medium">Recorded</th>
                </tr>
              </thead>
              <tbody>
                {events.data.map((event) => (
                  <tr key={event.id} className="border-b border-line last:border-0">
                    <td className="px-5 py-3 text-ink">
                      {actionLabel[event.action] ?? event.action}
                    </td>
                    <td className="px-5 py-3">
                      <Badge tone={event.actorType === "user" ? "accent" : "neutral"}>
                        {actorLabel[event.actorType] ?? event.actorType}
                      </Badge>
                    </td>
                    <td className="px-5 py-3 font-mono text-xs text-muted">
                      {event.target || "—"}
                    </td>
                    <td className="whitespace-nowrap px-5 py-3">
                      {/* The timestamp, and the relative time under it. An
                          audit trail is read to answer "when exactly", and
                          "3 hours ago" hidden behind a tooltip does not answer
                          it — but the relative form is what makes a list
                          scannable, so both are shown. */}
                      <div className="text-ink">{formatDateTime(event.createdAt)}</div>
                      <div className="text-xs text-muted">{formatRelative(event.createdAt)}</div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState title="No activity yet" />
        )}
      </Card>
    </div>
  );
}
