import * as React from "react";
import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";

interface State {
  error: Error | null;
}

/**
 * ErrorBoundary catches a render failure and says what it was.
 *
 * Without one, React unmounts the whole tree and leaves a blank page — which
 * tells the person nothing and tells whoever has to fix it even less. A blank
 * page is also indistinguishable from a network problem, a bad deploy, or a
 * browser extension, so it costs a round of guessing before anyone knows where
 * to look.
 *
 * The message is shown rather than hidden. This is an application a small
 * number of people run for themselves; a stack trace on screen is more useful
 * to all of them than a polite apology.
 */
export class ErrorBoundary extends React.Component<{ children: React.ReactNode }, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    // Also to the console, so it is in the place anyone debugging looks first.
    console.error("render failed", error, info.componentStack);
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;

    return (
      <div className="mx-auto max-w-2xl px-4 py-12">
        <div className="rounded-md border border-danger/40 bg-surface p-5">
          <div className="flex items-center gap-2 text-danger">
            <AlertTriangle className="size-5" aria-hidden />
            <h1 className="text-base font-semibold">This page failed to render</h1>
          </div>

          <p className="mt-2 text-sm text-muted">
            A defect in the interface, not in your data. Nothing was lost.
          </p>

          <pre className="mt-4 overflow-x-auto rounded bg-canvas p-3 text-xs text-ink">
            {error.message}
            {error.stack && `\n\n${error.stack}`}
          </pre>

          <div className="mt-4 flex gap-2">
            <Button size="sm" onClick={() => this.setState({ error: null })}>
              Try again
            </Button>
            <Button size="sm" variant="secondary" onClick={() => window.location.reload()}>
              Reload
            </Button>
          </div>
        </div>
      </div>
    );
  }
}
