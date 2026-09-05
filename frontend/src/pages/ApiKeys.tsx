import * as React from "react";
import { Check, Copy, KeyRound } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/field";
import { Alert, Badge, EmptyState, ErrorAlert, Spinner } from "@/components/ui/feedback";
import { useApiKeys, useCreateApiKey, useRevokeApiKey } from "@/hooks/useApi";
import { formatRelative } from "@/lib/utils";

export function ApiKeys() {
  const keys = useApiKeys();
  const create = useCreateApiKey();
  const revoke = useRevokeApiKey();

  const [name, setName] = React.useState("");
  const [secret, setSecret] = React.useState<string | null>(null);
  const [copied, setCopied] = React.useState(false);

  async function onCreate(event: React.FormEvent) {
    event.preventDefault();
    const created = await create.mutateAsync({ name: name.trim() });
    setSecret(created.secret);
    setName("");
  }

  async function copy() {
    if (!secret) return;
    await navigator.clipboard.writeText(secret);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 2000);
  }

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div>
        <h1 className="text-lg font-semibold text-ink">API keys</h1>
        <p className="text-sm text-muted">
          For scanners and CI pipelines filing findings through the REST API.
        </p>
      </div>

      {secret && (
        <Alert tone="warn" title="Copy this key now">
          <p>It is stored only as a hash and cannot be shown again.</p>
          <div className="mt-2 flex items-center gap-2">
            <code className="flex-1 overflow-x-auto rounded border border-line bg-canvas px-3 py-2 font-mono text-xs text-ink">
              {secret}
            </code>
            <Button size="sm" variant="secondary" onClick={copy}>
              {copied ? <Check className="size-4" aria-hidden /> : <Copy className="size-4" aria-hidden />}
              {copied ? "Copied" : "Copy"}
            </Button>
          </div>
          <Button size="sm" variant="ghost" className="mt-2" onClick={() => setSecret(null)}>
            I have saved it
          </Button>
        </Alert>
      )}

      <Card>
        <CardHeader title="Create a key" description="Scoped to this account, with findings:write." />
        <CardBody>
          <form onSubmit={onCreate} className="flex items-end gap-3">
            <div className="flex-1">
              <Field label="Name" htmlFor="keyName" hint="What will use this key.">
                <Input
                  required
                  maxLength={100}
                  placeholder="nightly-scanner"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </Field>
            </div>
            <Button type="submit" loading={create.isPending}>
              Create
            </Button>
          </form>
          <div className="mt-3">
            <ErrorAlert error={create.error} />
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader title="Keys" />
        <ErrorAlert error={keys.error} />

        {keys.isLoading ? (
          <Spinner label="Loading keys…" />
        ) : keys.data && keys.data.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-line text-left text-xs text-muted">
                  <th scope="col" className="px-5 py-2 font-medium">Name</th>
                  <th scope="col" className="px-5 py-2 font-medium">Key ID</th>
                  <th scope="col" className="px-5 py-2 font-medium">Last used</th>
                  <th scope="col" className="px-5 py-2 font-medium">Status</th>
                  <th scope="col" className="px-5 py-2" />
                </tr>
              </thead>
              <tbody>
                {keys.data.map((key) => (
                  <tr key={key.id} className="border-b border-line last:border-0">
                    <td className="px-5 py-3 text-ink">{key.name}</td>
                    <td className="px-5 py-3 font-mono text-xs text-muted">{key.keyId}</td>
                    {/* Last use is the signal that retires a key: one never
                        used, or unused for months, is a non-human identity
                        worth removing. */}
                    <td className="px-5 py-3 text-muted">
                      {key.lastUsedAt ? formatRelative(key.lastUsedAt) : "Never used"}
                    </td>
                    <td className="px-5 py-3">
                      {key.revokedAt ? (
                        <Badge tone="danger">Revoked</Badge>
                      ) : key.lastUsedAt ? (
                        <Badge tone="ok">Active</Badge>
                      ) : (
                        <Badge tone="warn">Unused</Badge>
                      )}
                    </td>
                    <td className="px-5 py-3 text-right">
                      {!key.revokedAt && (
                        <Button
                          size="sm"
                          variant="ghost"
                          loading={revoke.isPending && revoke.variables === key.id}
                          onClick={() => revoke.mutate(key.id)}
                        >
                          Revoke
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState
            title="No API keys"
            description="Create one to let a scanner or CI pipeline file findings."
          />
        )}
      </Card>

      <Card>
        <CardHeader title="Using a key" />
        <CardBody className="space-y-3 text-sm text-muted">
          <p className="inline-flex items-center gap-2">
            <KeyRound className="size-4" aria-hidden />
            Send the key as a bearer token. Repeating a request with the same
            <code className="mx-1 rounded bg-canvas px-1 font-mono text-xs">Idempotency-Key</code>
            returns the original ticket rather than filing a second one.
          </p>
          <pre className="overflow-x-auto rounded border border-line bg-canvas p-3 font-mono text-xs text-ink">
{`curl -X POST ${window.location.origin}/api/v1/findings \\
  -H "Authorization: Bearer ih_..." \\
  -H "Idempotency-Key: nightly-scan-001" \\
  -H "Content-Type: application/json" \\
  -d '{"projectKey":"NHI",
       "title":"Stale service account: svc-legacy-etl",
       "description":"No authentication in 412 days."}'`}
          </pre>
        </CardBody>
      </Card>
    </div>
  );
}
