import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/** cn merges Tailwind classes, letting a caller override a component's own. */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/**
 * formatDateTime renders a timestamp in the reader's own locale and timezone.
 *
 * Ticket times come from the server as UTC. Showing them as-is would be
 * technically accurate and practically useless: "was this filed during my
 * shift?" is the question people actually have.
 */
export function formatDateTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

/** formatRelative renders a short "2 hours ago" style string. */
export function formatRelative(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";

  const seconds = Math.round((date.getTime() - Date.now()) / 1000);
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["year", 60 * 60 * 24 * 365],
    ["month", 60 * 60 * 24 * 30],
    ["day", 60 * 60 * 24],
    ["hour", 60 * 60],
    ["minute", 60],
  ];

  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  for (const [unit, secondsPerUnit] of units) {
    if (Math.abs(seconds) >= secondsPerUnit) {
      return formatter.format(Math.round(seconds / secondsPerUnit), unit);
    }
  }
  return formatter.format(seconds, "second");
}

/**
 * temporalURL is where the durable execution interface is published.
 *
 * Derived from the current host rather than hardcoded, so it is right whether
 * the stack is on localhost or on a machine somebody reached by name. The port
 * is Temporal's own: it is a separate application, not something this one
 * proxies.
 */
export function temporalURL(path = ""): string {
  return `${window.location.protocol}//${window.location.hostname}:8233${path}`;
}
