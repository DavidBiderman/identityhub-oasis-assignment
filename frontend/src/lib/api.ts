/**
 * The single place the application talks to the API.
 *
 * Two things matter here. Every request carries the access token as a bearer
 * header — identity comes from the token, not from a cookie this application
 * set. And every failure becomes an ApiError carrying the server's own message,
 * so a caller can show what the server said rather than inventing something.
 */

import { readToken } from "@/lib/token";

/** What the server sends when a request does not succeed. */
export interface Failure {
  code: string;
  message: string;
  /** Field name to the reason it was rejected, when the failure named fields. */
  fields?: Record<string, string>;
}

/** Every response is one of these: data on success, error on failure. */
interface Envelope<T> {
  data?: T;
  error?: Failure;
}

/**
 * ApiError carries the server's own explanation.
 *
 * The API is deliberate about its messages -- it distinguishes a rejected
 * credential from a permission problem from an upstream outage -- so showing
 * `message` verbatim is almost always better than a string written here.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly failure: Failure;

  constructor(status: number, failure: Failure) {
    super(failure.message);
    this.name = "ApiError";
    this.status = status;
    this.failure = failure;
  }

  /** The server's stable, machine-readable code for what went wrong. */
  get code(): string {
    return this.failure.code;
  }

  /** True when the token is missing, expired or rejected. */
  get isUnauthenticated(): boolean {
    return this.status === 401;
  }

  /** Field-level rejections, keyed by the input each one names. */
  get fields(): Record<string, string> {
    return this.failure.fields ?? {};
  }

  /** True when the remedy is to connect or reconnect an integration. */
  get needsConnection(): boolean {
    return (
      this.code === "not_connected" ||
      this.code === "connection_unusable" ||
      this.code === "credential_rejected"
    );
  }
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

/** request performs an API call and returns the decoded body. */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = "GET", body, headers = {}, signal } = options;

  const token = readToken();

  const response = await fetch(path, {
    method,
    headers: {
      Accept: "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...headers,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
  });

  if (response.status === 204) {
    return undefined as T;
  }

  const text = await response.text();
  const envelope = (text ? safeParse(text) : undefined) as Envelope<T> | undefined;

  if (!response.ok) {
    throw new ApiError(response.status, envelope?.error ?? fallbackFailure(response.status));
  }
  return envelope?.data as T;
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

/**
 * fallbackFailure covers the case where the body is not ours.
 *
 * The API always sends an envelope, but a proxy or a network failure can
 * produce something else, and the interface should still have something to
 * show.
 */
function fallbackFailure(status: number): Failure {
  return {
    code: "request_failed",
    message:
      status >= 500
        ? "The server could not complete the request. Try again shortly."
        : "The request could not be completed.",
  };
}

export const api = {
  get: <T,>(path: string, signal?: AbortSignal) => request<T>(path, { signal }),
  post: <T,>(path: string, body?: unknown, headers?: Record<string, string>) =>
    request<T>(path, { method: "POST", body, headers }),
  delete: <T,>(path: string) => request<T>(path, { method: "DELETE" }),
};
