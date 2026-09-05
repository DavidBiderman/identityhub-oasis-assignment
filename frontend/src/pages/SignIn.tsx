import * as React from "react";
import { useNavigate } from "react-router-dom";
import { LogIn, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Alert } from "@/components/ui/feedback";
import { Field, Input } from "@/components/ui/field";
import { useAuth } from "@/hooks/useAuth";
import { api, ApiError } from "@/lib/api";
import type { SignInResult } from "@/lib/types";

/**
 * The sign-in page.
 *
 * In a real deployment this page does not exist: the application redirects to
 * an identity provider, the person signs in however their organization
 * requires, and a token comes back. Everything after that point -- the token in
 * the Authorization header, the tenancy read from its claims, signing out --
 * is identical either way, which is what makes that swap a change to one file.
 */
export function SignIn() {
  const { useToken } = useAuth();
  const navigate = useNavigate();

  const [email, setEmail] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [failure, setFailure] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setFailure(null);
    setBusy(true);

    try {
      const result = await api.post<SignInResult>("/api/auth/signin", { email, password });
      useToken(result.accessToken);
      navigate("/findings/new", { replace: true });
    } catch (error) {
      // The API answers the same way for an unknown address and a wrong
      // password, and so does this: repeating its message rather than guessing
      // at a friendlier one keeps the two indistinguishable here too.
      setFailure(
        error instanceof ApiError
          ? error.message
          : "Could not reach the server. Try again shortly.",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mx-auto flex min-h-screen max-w-md flex-col justify-center px-4">
      <div className="mb-6 text-center">
        <ShieldCheck className="mx-auto size-8 text-accent" aria-hidden />
        <h1 className="mt-3 text-xl font-semibold text-ink">IdentityHub</h1>
        <p className="mt-1 text-sm text-muted">
          File non-human-identity findings into your issue tracker.
        </p>
      </div>

      <Card>
        <form onSubmit={submit} className="space-y-4 p-5" noValidate>
          <Field label="Email" htmlFor="email">
            <Input
              id="email"
              type="email"
              autoComplete="username"
              autoFocus
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="admin@acme.test"
            />
          </Field>

          <Field label="Password" htmlFor="password">
            <Input
              id="password"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>

          {failure && <Alert tone="danger" title="Could not sign in">{failure}</Alert>}

          <Button type="submit" className="w-full" loading={busy} disabled={!email || !password}>
            <LogIn className="size-4" aria-hidden />
            Sign in
          </Button>
        </form>
      </Card>

      {/* The seeded accounts, so a reviewer can get in without reading the
          README first. Three people across two accounts in one organization,
          which is what makes the isolation visible. */}
      <div className="mt-6 rounded-md border border-line p-4 text-xs text-muted">
        <p className="font-medium text-ink">Demo accounts</p>
        <p className="mt-1">
          Password <code className="text-ink">identityhub-demo</code> for all three.
        </p>
        <ul className="mt-2 space-y-1">
          <li><code className="text-ink">admin@acme.test</code> — administrator, Platform Engineering</li>
          <li><code className="text-ink">member@acme.test</code> — member, Platform Engineering</li>
          <li><code className="text-ink">secops@acme.test</code> — administrator, Security Operations</li>
        </ul>
        <p className="mt-2">
          The two accounts cannot see each other&rsquo;s connections or tickets.
        </p>
      </div>
    </div>
  );
}
