import type * as React from "react";
import { AlertTriangle, CheckCircle2, Info, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { ApiError } from "@/lib/api";

/**
 * Alert states a problem and, where possible, what to do about it.
 *
 * Every message shown to a user should either be actionable or explain why no
 * action is needed. "Something went wrong" is neither.
 */
export function Alert({
  tone = "info",
  title,
  children,
}: {
  tone?: "info" | "warn" | "danger" | "ok";
  title?: string;
  children?: React.ReactNode;
}) {
  const Icon = tone === "ok" ? CheckCircle2 : tone === "info" ? Info : AlertTriangle;
  const toneClass = {
    info: "border-line text-ink",
    ok: "border-ok/40 text-ink",
    warn: "border-warn/50 text-ink",
    danger: "border-danger/50 text-ink",
  }[tone];
  const iconClass = {
    info: "text-muted",
    ok: "text-ok",
    warn: "text-warn",
    danger: "text-danger",
  }[tone];

  return (
    <div
      role={tone === "danger" ? "alert" : "status"}
      className={cn("flex gap-3 rounded-md border bg-surface px-4 py-3 text-sm", toneClass)}
    >
      <Icon className={cn("mt-0.5 size-4 shrink-0", iconClass)} aria-hidden />
      <div className="space-y-1">
        {title && <p className="font-medium">{title}</p>}
        {children && <div className="text-muted">{children}</div>}
      </div>
    </div>
  );
}

/**
 * ErrorAlert renders whatever the API said went wrong.
 *
 * The server distinguishes a rejected credential from a permission problem from
 * an upstream outage, and writes a message for each. Showing that message is
 * more useful than any generic string this component could substitute.
 */
export function ErrorAlert({ error, action }: { error: unknown; action?: React.ReactNode }) {
  if (!error) return null;

  if (error instanceof ApiError) {
    return (
      <Alert tone={error.status >= 500 ? "danger" : "warn"} title={error.message}>
        {Object.keys(error.fields).length > 0 && (
          <ul className="list-inside list-disc">
            {Object.entries(error.fields).map(([field, reason]) => (
              <li key={field}>
                <span className="font-medium">{field}</span>: {reason}
              </li>
            ))}
          </ul>
        )}
        {action && <div className="mt-2">{action}</div>}
      </Alert>
    );
  }

  return (
    <Alert tone="danger" title="Something went wrong">
      <p>{error instanceof Error ? error.message : "The request could not be completed."}</p>
    </Alert>
  );
}

export function Spinner({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-2 px-5 py-8 text-sm text-muted">
      <Loader2 className="size-4 animate-spin" aria-hidden />
      <span>{label}</span>
    </div>
  );
}

/**
 * EmptyState explains why a list is empty and what would fill it.
 *
 * An empty table with no explanation reads as a failure; saying "no tickets
 * yet" and offering the action that creates one does not.
 */
export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="px-5 py-12 text-center">
      <p className="text-sm font-medium text-ink">{title}</p>
      {description && <p className="mx-auto mt-1 max-w-md text-sm text-muted">{description}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

export function Badge({
  tone = "neutral",
  children,
}: {
  tone?: "neutral" | "accent" | "ok" | "warn" | "danger";
  children: React.ReactNode;
}) {
  const toneClass = {
    neutral: "border-line text-muted",
    accent: "border-accent/40 text-accent",
    ok: "border-ok/40 text-ok",
    warn: "border-warn/50 text-warn",
    danger: "border-danger/50 text-danger",
  }[tone];

  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium",
        toneClass,
      )}
    >
      {children}
    </span>
  );
}
