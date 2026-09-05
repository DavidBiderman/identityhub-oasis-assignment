import * as React from "react";
import { Link } from "react-router-dom";
import { ExternalLink } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Combobox } from "@/components/ui/combobox";
import { Field, Input, Textarea } from "@/components/ui/field";
import { Alert, ErrorAlert } from "@/components/ui/feedback";
import { useCreateTicket, useIssueTypes, useProjects } from "@/hooks/useApi";
import { ApiError } from "@/lib/api";
import { isPending } from "@/lib/types";
import type { CreatedTicket } from "@/lib/types";

const MAX_TITLE = 255;

export function NewFinding() {
  const projects = useProjects(true);
  const create = useCreateTicket();

  const [projectKey, setProjectKey] = React.useState("");
  const [issueTypeId, setIssueTypeId] = React.useState("");
  const [title, setTitle] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [filed, setFiled] = React.useState<CreatedTicket | null>(null);
  const [pending, setPending] = React.useState<string | null>(null);

  const issueTypes = useIssueTypes(projectKey);

  // The key is what the API takes, so it is the value; the name is what a
  // person recognises, so it is the label. Both are searchable.
  const projectOptions = React.useMemo(
    () => (projects.data ?? []).map((p) => ({ value: p.key, label: p.name, hint: p.key })),
    [projects.data],
  );

  // An empty value is a real choice here: the connector picks a suitable type
  // when none is named, which is what most people want.
  const issueTypeOptions = React.useMemo(
    () => [
      { value: "", label: "Choose automatically" },
      ...(issueTypes.data ?? []).map((t) => ({ value: t.id, label: t.name })),
    ],
    [issueTypes.data],
  );

  // Pick the first project once they load, so the common case is one fewer
  // interaction. The picker still allows any of them.
  React.useEffect(() => {
    if (!projectKey && projects.data?.[0]) setProjectKey(projects.data[0].key);
  }, [projects.data, projectKey]);

  // Issue types are per project, so a type chosen for one is meaningless for
  // another and must be cleared rather than silently carried over.
  React.useEffect(() => {
    setIssueTypeId("");
  }, [projectKey]);

  const notConnected = projects.error instanceof ApiError && projects.error.needsConnection;

  // What the API requires, checked here so the button says so before a round
  // trip. The server checks the same three things and is the authority.
  const canFile =
    projectKey !== "" &&
    title.trim() !== "" &&
    description.trim() !== "" &&
    title.trim().length <= MAX_TITLE;

  /** fieldError surfaces a server-side rejection next to the field it names. */
  function fieldError(name: string): string | undefined {
    if (!(create.error instanceof ApiError)) return undefined;
    return create.error.fields[name];
  }

  function onSubmit(event: React.FormEvent) {
    event.preventDefault();
    setFiled(null);
    setPending(null);

    // mutate rather than mutateAsync: a rejected mutation belongs to React
    // Query, which puts it in create.error for ErrorAlert below. Awaiting it
    // here without a catch would raise an unhandled rejection instead.
    create.mutate(
      {
        projectKey,
        issueTypeId: issueTypeId || undefined,
        title: title.trim(),
        description: description.trim(),
      },
      {
        onSuccess: (result) => {
          // Two answers are possible. The provider was quick and the ticket
          // exists, or it was slow and the workflow is still retrying -- in
          // which case there is no issue key yet and claiming one would be a
          // lie.
          if (isPending(result)) {
            setPending(result.detail);
          } else {
            setFiled(result);
          }
          setTitle("");
          setDescription("");
        },
      },
    );
  }

  if (notConnected) {
    return (
      <div className="mx-auto max-w-2xl space-y-6">
        <h1 className="text-lg font-semibold text-ink">New finding</h1>
        <Alert tone="warn" title="No issue tracker connected">
          <p>Connect Jira before filing findings.</p>
          <Button asChild size="sm" variant="secondary" className="mt-3">
            <Link to="/connections">Go to connections</Link>
          </Button>
        </Alert>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div>
        <h1 className="text-lg font-semibold text-ink">New finding</h1>
        <p className="text-sm text-muted">File an NHI finding as a ticket.</p>
      </div>

      {pending && (
        <Alert tone="info" title="Filing in progress">
          <p>{pending}</p>
          <Button asChild size="sm" variant="secondary" className="mt-3">
            <Link to="/tickets">View recent tickets</Link>
          </Button>
        </Alert>
      )}

      {filed && (
        <Alert
          tone="ok"
          title={filed.created ? `Filed ${filed.issueKey}` : `Already filed as ${filed.issueKey}`}
        >
          <p>{filed.title}</p>
          {/* A replayed idempotency key returns the original ticket. Saying so
              is the difference between "your finding is filed" and letting
              somebody believe they just created a second one. */}
          {!filed.created && (
            <p className="mt-1 text-xs text-muted">
              This finding had already been filed. Nothing was created twice.
            </p>
          )}
          <a
            href={filed.issueUrl}
            target="_blank"
            rel="noreferrer noopener"
            className="mt-2 inline-flex items-center gap-1 text-accent hover:underline"
          >
            Open {filed.issueKey} in Jira
            <ExternalLink className="size-3" aria-hidden />
          </a>
        </Alert>
      )}

      <Card>
        <CardHeader title="Finding details" />
        <CardBody>
          <form onSubmit={onSubmit} className="space-y-4">
            {/* A combobox rather than a select: an account with two hundred
                projects makes a select unusable, and a free-text box invites a
                key that does not exist. Every project is reachable by
                scrolling, typing narrows them, and nothing off the list can be
                submitted. */}
            <Field label="Project" htmlFor="projectKey" error={fieldError("projectKey")}>
              <Combobox
                id="projectKey"
                options={projectOptions}
                value={projectKey}
                onChange={setProjectKey}
                loading={projects.isLoading}
                invalid={Boolean(fieldError("projectKey"))}
                placeholder="Select a project…"
                emptyMessage="No project matches."
              />
            </Field>

            <Field
              label="Issue type"
              htmlFor="issueTypeId"
              hint="Optional. A suitable type is chosen automatically."
              error={fieldError("issueTypeId")}
            >
              <Combobox
                id="issueTypeId"
                options={issueTypeOptions}
                value={issueTypeId}
                onChange={setIssueTypeId}
                loading={Boolean(projectKey) && issueTypes.isLoading}
                disabled={!projectKey}
                invalid={Boolean(fieldError("issueTypeId"))}
                placeholder="Choose automatically"
                emptyMessage="This project accepts no issue type the connected account can create."
              />
            </Field>

            <Field
              label="Title"
              htmlFor="title"
              hint={`${title.length}/${MAX_TITLE} — what was found, and which identity.`}
              error={fieldError("title")}
            >
              <Input
                required
                maxLength={MAX_TITLE}
                placeholder="Stale service account: svc-deploy-prod"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
            </Field>

            <Field
              label="Description"
              htmlFor="description"
              hint="Evidence, blast radius, and what should happen next."
              error={fieldError("description")}
            >
              <Textarea
                required
                rows={8}
                placeholder={
                  "svc-deploy-prod has not authenticated in 400 days but retains write " +
                  "access to production deployment pipelines.\n\n" +
                  "Recommended action: revoke the credential and remove the account."
                }
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </Field>

            <ErrorAlert error={create.error} />

            <div className="flex justify-end">
              {/* Disabled until the request would actually be valid. The
                  inputs carry `required` too, so the browser would refuse the
                  submit — but a button that looks ready and then does nothing
                  is worse than one that shows it is not. The rules match the
                  API's: a project, a title and a description. */}
              <Button type="submit" loading={create.isPending} disabled={!canFile}>
                File finding
              </Button>
            </div>
          </form>
        </CardBody>
      </Card>
    </div>
  );
}
