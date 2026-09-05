import { NavLink, Outlet } from "react-router-dom";
import {
  ExternalLink,
  FilePlus2,
  KeyRound,
  ListChecks,
  LogOut,
  Plug,
  ScrollText,
  ShieldCheck,
} from "lucide-react";
import { temporalURL } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/hooks/useAuth";
import { cn } from "@/lib/utils";

const nav = [
  { to: "/findings/new", label: "New finding", icon: FilePlus2 },
  { to: "/tickets", label: "Recent tickets", icon: ListChecks },
  { to: "/connections", label: "Connections", icon: Plug },
  { to: "/api-keys", label: "API keys", icon: KeyRound },
  { to: "/audit", label: "Audit", icon: ScrollText },
];

export function Layout() {
  const { me, discardToken } = useAuth();

  return (
    <div className="min-h-full">
      <header className="border-b border-line bg-surface">
        <div className="mx-auto flex max-w-6xl items-center gap-6 px-4 py-3">
          <div className="flex items-center gap-2">
            <ShieldCheck className="size-5 text-accent" aria-hidden />
            <span className="text-sm font-semibold text-ink">IdentityHub</span>
          </div>

          <nav aria-label="Main" className="flex flex-1 items-center gap-1 overflow-x-auto">
            {nav.map(({ to, label, icon: Icon }) => (
              <NavLink
                key={to}
                to={to}
                className={({ isActive }) =>
                  cn(
                    "inline-flex items-center gap-2 whitespace-nowrap rounded-md px-3 py-1.5 text-sm transition-colors",
                    isActive
                      ? "bg-canvas font-medium text-ink"
                      : "text-muted hover:bg-canvas hover:text-ink",
                  )
                }
              >
                <Icon className="size-4" aria-hidden />
                {label}
              </NavLink>
            ))}
          </nav>

          {me && (
            <div className="flex items-center gap-3">
              {/* Shown because tenancy is the product's central idea: a person
                  should never be unsure which account they are acting in. */}
              <div className="hidden text-right sm:block">
                <p className="text-sm text-ink">{me.name || me.email}</p>
                <p className="text-xs text-muted">
                  {me.email} · {me.roles.join(", ") || "no roles"}
                </p>
              </div>
              {/* "Sign out" is what a person is looking for, so that is what it
                  says. What it does is discard the access token and everything
                  cached with it, which ends this application's access
                  immediately. It does not end the identity provider's session,
                  because that is the provider's to end — in a real deployment
                  this is an RP-initiated logout redirect. */}
              <Button variant="ghost" size="sm" onClick={discardToken} title="Discard the access token">
                <LogOut className="size-4" aria-hidden />
                <span className="sr-only sm:not-sr-only">Sign out</span>
              </Button>
            </div>
          )}
        </div>
      </header>

      <main className="mx-auto max-w-6xl px-4 py-8">
        <Outlet />
      </main>

      {/* Every ticket is filed by a durable workflow, and the schedules run
          there. It is a separate application on its own port, not something
          this one proxies, so it is a link rather than a page. */}
      <footer className="mx-auto max-w-6xl px-4 pb-8 text-xs text-muted">
        Filing is durable.{" "}
        <a
          href={temporalURL("/namespaces/identityhub/workflows")}
          target="_blank"
          rel="noreferrer noopener"
          className="inline-flex items-center gap-1 text-accent hover:underline"
        >
          Inspect every workflow, retry and schedule in Temporal
          <ExternalLink className="size-3" aria-hidden />
        </a>
      </footer>
    </div>
  );
}
