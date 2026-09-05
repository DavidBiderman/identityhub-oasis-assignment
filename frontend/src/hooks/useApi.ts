import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type {
  ApiKey,
  AuditEvent,
  ConnectionsResponse,
  CreatedApiKey,
  Digest,
  FiledTicket,
  IssueType,
  Project,
  Ticket,
} from "@/lib/types";

/** Query keys in one place, so an invalidation cannot miss a cache by typo. */
export const keys = {
  connections: ["connections"] as const,
  projects: (query: string) => ["projects", query] as const,
  issueTypes: (projectKey: string) => ["issue-types", projectKey] as const,
  tickets: (projectKey: string) => ["tickets", projectKey] as const,
  apiKeys: ["api-keys"] as const,
  audit: ["audit"] as const,
  digest: ["digest"] as const,
};

export function useConnections() {
  return useQuery({
    queryKey: keys.connections,
    queryFn: ({ signal }) => api.get<ConnectionsResponse>("/api/connections", signal),
  });
}

export function useConnect() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { connectorType: string; config: Record<string, string> }) =>
      api.post("/api/connections", input),
    onSuccess: () => {
      // A new connection changes what projects exist, so those caches are stale.
      void queryClient.invalidateQueries();
    },
  });
}

export function useDisconnect() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (connectorType: string) => api.delete(`/api/connections/${connectorType}`),
    onSuccess: () => {
      void queryClient.invalidateQueries();
    },
  });
}

export function useProjects(enabled: boolean) {
  return useQuery({
    queryKey: keys.projects(""),
    queryFn: ({ signal }) =>
      api.get<{ projects: Project[] }>("/api/projects", signal).then((r) => r.projects),
    enabled,
    // Projects change rarely, and each fetch is a call to the provider.
    staleTime: 5 * 60_000,
    retry: false,
  });
}

export function useIssueTypes(projectKey: string) {
  return useQuery({
    queryKey: keys.issueTypes(projectKey),
    queryFn: ({ signal }) =>
      api
        .get<{ issueTypes: IssueType[] }>(
          `/api/projects/${encodeURIComponent(projectKey)}/issue-types`,
          signal,
        )
        .then((r) => r.issueTypes),
    enabled: projectKey.length > 0,
    staleTime: 5 * 60_000,
    retry: false,
  });
}

export function useTickets(projectKey: string) {
  return useQuery({
    queryKey: keys.tickets(projectKey),
    queryFn: ({ signal }) => {
      const params = new URLSearchParams({ limit: "10" });
      if (projectKey) params.set("projectKey", projectKey);
      return api
        .get<{ tickets: Ticket[] }>(`/api/tickets?${params}`, signal)
        .then((r) => r.tickets);
    },
    retry: false,
  });
}

export function useCreateTicket() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      projectKey: string;
      issueTypeId?: string;
      title: string;
      description: string;
    }) => api.post<FiledTicket>("/api/tickets", input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["tickets"] });
      void queryClient.invalidateQueries({ queryKey: keys.audit });
    },
  });
}

export function useDigest() {
  return useQuery({
    queryKey: keys.digest,
    queryFn: ({ signal }) => api.get<Digest>("/api/digest", signal),
    retry: false,
  });
}

/**
 * useRunDigest starts a run now rather than waiting for the next scheduled one.
 *
 * Tickets are invalidated on success because that is where the result shows up:
 * a digest run files findings, and the recent list is how anyone sees them.
 */
export function useRunDigest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<Digest>("/api/digest", {}),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.digest });
      void queryClient.invalidateQueries({ queryKey: ["tickets"] });
      void queryClient.invalidateQueries({ queryKey: keys.audit });
    },
  });
}

export function useApiKeys() {
  return useQuery({
    queryKey: keys.apiKeys,
    queryFn: ({ signal }) =>
      api.get<{ apiKeys: ApiKey[] }>("/api/api-keys", signal).then((r) => r.apiKeys),
  });
}

export function useCreateApiKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { name: string }) => api.post<CreatedApiKey>("/api/api-keys", input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.apiKeys });
    },
  });
}

export function useRevokeApiKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete(`/api/api-keys/${id}`),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.apiKeys });
    },
  });
}

export function useAuditEvents() {
  return useQuery({
    queryKey: keys.audit,
    queryFn: ({ signal }) =>
      api.get<{ events: AuditEvent[] }>("/api/audit?limit=50", signal).then((r) => r.events),
  });
}
