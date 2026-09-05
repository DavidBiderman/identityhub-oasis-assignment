import * as Dialog from "@radix-ui/react-dialog";
import { ExternalLink, HelpCircle, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { SetupStep } from "@/lib/types";

/**
 * SetupGuide explains how to get the credential a connector needs.
 *
 * The steps come from the connector rather than from this file. Finding the
 * page that mints an Atlassian API token is three clicks through a menu nobody
 * would guess, and the scopes are chosen on that same screen — a form that asks
 * for a token and offers no way to get one is a form people abandon. Another
 * provider's instructions are different and equally unguessable, so each
 * connector carries its own and this renders whatever it is given.
 */
export function SetupGuide({ title, steps }: { title: string; steps: SetupStep[] }) {
  if (steps.length === 0) return null;

  return (
    <Dialog.Root>
      <Dialog.Trigger asChild>
        {/* A visible control rather than small print. Nobody reads a hint under
            a password field, and this is the step people get stuck on: the
            token is created somewhere in Atlassian that is not Jira. */}
        <button
          type="button"
          className={
            "inline-flex items-center gap-1.5 rounded-md border border-accent/30 " +
            "bg-accent/5 px-3 py-1.5 text-sm font-medium text-accent " +
            "hover:bg-accent/10"
          }
        >
          <HelpCircle className="size-4" aria-hidden />
          How do I create a token?
        </button>
      </Dialog.Trigger>

      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/40" />
        <Dialog.Content
          className={
            "fixed left-1/2 top-1/2 z-50 w-[min(38rem,calc(100vw-2rem))] max-h-[85vh] " +
            "-translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-lg border " +
            "border-line bg-surface p-6 shadow-xl"
          }
        >
          <div className="flex items-start justify-between gap-4">
            <Dialog.Title className="text-base font-semibold text-ink">{title}</Dialog.Title>
            <Dialog.Close asChild>
              <button
                type="button"
                aria-label="Close"
                className="rounded p-1 text-muted hover:bg-canvas hover:text-ink"
              >
                <X className="size-4" aria-hidden />
              </button>
            </Dialog.Close>
          </div>

          <Dialog.Description className="mt-1 text-sm text-muted">
            The token is created on your Atlassian account, not inside Jira.
          </Dialog.Description>

          <ol className="mt-5 space-y-4">
            {steps.map((step, i) => (
              <li key={i} className="flex gap-3">
                <span
                  className={
                    "flex size-6 shrink-0 items-center justify-center rounded-full " +
                    "bg-accent/10 text-xs font-medium text-accent"
                  }
                  aria-hidden
                >
                  {i + 1}
                </span>
                <div className="space-y-1 text-sm text-ink">
                  <p>{step.text}</p>
                  {step.url && (
                    <a
                      href={step.url}
                      target="_blank"
                      rel="noreferrer noopener"
                      className="inline-flex items-center gap-1 text-accent hover:underline"
                    >
                      {step.linkText || step.url}
                      <ExternalLink className="size-3" aria-hidden />
                    </a>
                  )}
                </div>
              </li>
            ))}
          </ol>

          <div className="mt-6 flex justify-end">
            <Dialog.Close asChild>
              <Button size="sm" variant="secondary">
                Close
              </Button>
            </Dialog.Close>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
