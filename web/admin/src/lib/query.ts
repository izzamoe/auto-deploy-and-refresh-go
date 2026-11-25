// TanStack Query setup (D13, B56, B57): abortable fetches (the query `signal` goes to
// apiFetch), calm retries and ONE place that knows which cached data a finished job makes
// stale.
import { QueryClient } from "@tanstack/react-query";
import { ApiError, isAbortError } from "@/lib/api/client";

export const queryKeys = {
  session: ["session"] as const,
  apps: ["apps"] as const,
  app: (appId: string) => ["apps", appId] as const,
  history: ["history"] as const,
  historyPage: (appId: string | undefined, page: number, pageSize: number) => ["history", { appId, page, pageSize }] as const,
  stats: ["stats"] as const,
  statsRange: (range: string) => ["stats", { range }] as const,
  github: ["github"] as const,
  telegram: ["telegram"] as const,
  systemLogs: (lines: number) => ["system-logs", { lines }] as const,
  releases: (appId: string) => ["releases", appId] as const,
  serviceStatus: (appId: string) => ["service-status", appId] as const,
  tokens: ["tokens"] as const,
};

// Client errors (4xx) will not change on retry; network and 5xx may.
export function shouldRetry(failureCount: number, error: unknown): boolean {
  if (isAbortError(error)) return false;
  if (error instanceof ApiError && error.kind === "http" && error.status >= 400 && error.status < 500) return false;
  return failureCount < 2;
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        retry: shouldRetry,
        refetchOnWindowFocus: true,
      },
      mutations: { retry: false },
    },
  });
}

/**
 * A job reached a terminal state (FR-078): the app list (last job), the history, the
 * dashboard stats, the systemd status of that app (the deploy restarted the service) and
 * its release list are all stale now.
 */
export function invalidateAfterJob(client: QueryClient, appId?: string) {
  void client.invalidateQueries({ queryKey: queryKeys.apps });
  void client.invalidateQueries({ queryKey: queryKeys.history });
  void client.invalidateQueries({ queryKey: queryKeys.stats });
  if (appId) {
    void client.invalidateQueries({ queryKey: queryKeys.serviceStatus(appId) });
    void client.invalidateQueries({ queryKey: queryKeys.releases(appId) });
  } else {
    void client.invalidateQueries({ queryKey: ["service-status"] });
    void client.invalidateQueries({ queryKey: ["releases"] });
  }
}

/** After a reconnect the socket may have missed events: refetch everything visible. */
export function invalidateAll(client: QueryClient) {
  void client.invalidateQueries();
}
