/** Shapes the API returns. Kept in one file so a change to the API is one diff. */

/** What the caller's access token asserts about them. */
export interface Me {
  subject: string;
  email: string;
  name: string;
  roles: string[];
  orgId: string;
  accountId: string;
}

/** What POST /api/auth/signin answers with. */
export interface SignInResult {
  accessToken: string;
  tokenType: "Bearer";
  expiresIn: number;
}

export interface Connection {
  id: string;
  connectorType: string;
  displayName: string;
  metadata: Record<string, string>;
  status: "active" | "invalid" | "revoked";
  lastVerifiedAt: string | null;
  lastError?: string;
  createdAt: string;
}

export interface ConnectionsResponse {
  connections: Connection[];
  availableTypes: ConnectorType[];
}

export interface Project {
  id: string;
  key: string;
  name: string;
}

export interface IssueType {
  id: string;
  name: string;
}

/** How a ticket came to exist, which the interface shows so a person can tell
 *  their own work from a scanner's. */
export type TicketSource = "ui" | "api" | "automation";

export interface Ticket {
  id: string;
  projectKey: string;
  issueKey: string;
  issueUrl: string;
  title: string;
  source: TicketSource;
  status?: string;
  createdAt: string;
}

/**
 * A ticket that is still being filed.
 *
 * Filing is durable: the API waits a few seconds for the workflow and then
 * answers 202 rather than failing a request that will probably still succeed.
 * The ticket appears in the recent list when it lands.
 */
export interface PendingTicket {
  status: "pending";
  workflowId: string;
  detail: string;
}

/**
 * A ticket a filing request answered with.
 *
 * `created` is false when an earlier request with the same Idempotency-Key
 * already filed it. That is a success -- the finding exists, which is what was
 * asked for -- so the interface says "already filed" rather than presenting it
 * as something that just happened.
 */
export interface CreatedTicket extends Ticket {
  created: boolean;
}

/** What POST /api/tickets answers with -- one or the other, never both. */
export type FiledTicket = CreatedTicket | PendingTicket;

/** isPending narrows a filing result to the 202 case. */
export function isPending(result: FiledTicket): result is PendingTicket {
  return "status" in result && result.status === "pending";
}

/** How a configuration value should be rendered and validated. */
export type FieldKind = "text" | "url" | "email" | "secret";

/** One value a connector needs before it can be configured. */
export interface ConnectorField {
  name: string;
  label: string;
  kind: FieldKind;
  required: boolean;
  help?: string;
  example?: string;
}

/**
 * A connector that could be added, and what it needs.
 *
 * The fields come from the connector, so the connect form is built from the
 * response rather than written here. Jira needs a site, an email and a token;
 * the next connector needs something else, and this file does not change.
 */
/** One instruction in obtaining a credential. */
export interface SetupStep {
  text: string;
  url?: string;
  linkText?: string;
}

export interface ConnectorType {
  type: string;
  fields: ConnectorField[];
  /** How to obtain the credential, in order. Comes from the connector. */
  setup?: SetupStep[];
}

/**
 * The recurring NHI Blog Digest for this account.
 *
 * `projectKey` absent means it resolves the first project the account can reach
 * at run time, so the feature works without anybody configuring one.
 */
export interface Digest {
  scheduled: boolean;
  paused: boolean;
  projectKey?: string;
  nextRunAt: string | null;
  lastRunAt: string | null;
  everyHours: number;
}

export interface ApiKey {
  id: string;
  keyId: string;
  name: string;
  scopes: string[];
  createdAt: string;
  lastUsedAt: string | null;
  revokedAt: string | null;
}

export interface CreatedApiKey {
  apiKey: ApiKey;
  secret: string;
  notice: string;
}

export interface AuditEvent {
  id: number;
  actorType: "user" | "api_key" | "system";
  action: string;
  target: string;
  metadata: Record<string, unknown>;
  createdAt: string;
}
