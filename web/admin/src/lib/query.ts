// TanStack Query setup (D13, B56, B57): abortable fetches (the query `signal` goes to
// apiFetch), calm retries and ONE place that knows which cached data a finished job makes
// stale.
import { QueryClient } from "@tanstack/react-query";
import { ApiError, isAbortError } from "@/lib/api/client";
import type { Ok, Schemas } from "@/lib/api/client";

type AppRow = Schemas["App"];
type AppList = Ok<"listApps">;
type AppOne = Ok<"getApp">;

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
  appEnv: (appId: string | undefined) => ["app-env", appId] as const,
  appArgs: (appId: string | undefined) => ["app-args", appId] as const,
  unitPreview: (appId: string) => ["service-unit-preview", appId] as const,
};

const newer = (a: string | undefined, b: string | undefined) => {
  const ta = a ? Date.parse(a) : NaN;
  const tb = b ? Date.parse(b) : NaN;
  return !Number.isNaN(ta) && !Number.isNaN(tb) && ta > tb;
};

/**
 * Puts one app into the cached list and its own entry (app.upserted, or a mutation response):
 * replace or insert, no request. Idempotent: an older `updatedAt` never replaces a newer row.
 */
export function upsertAppInCache(client: QueryClient, app: AppRow) {
  client.setQueryData<AppList>(queryKeys.apps, (old) => {
    if (!old) return old;
    const i = old.apps.findIndex((a) => a.id === app.id);
    if (i < 0) return { ...old, apps: [...old.apps, app] };
    if (newer(old.apps[i].updatedAt, app.updatedAt)) return old;
    const apps = old.apps.slice();
    apps[i] = app;
    return { ...old, apps };
  });
  client.setQueryData<AppOne>(queryKeys.app(app.id), (old) => (old && !newer(old.app.updatedAt, app.updatedAt) ? { ...old, app } : old));
}

/** app.deleted: drops the row (no request). */
export function removeAppFromCache(client: QueryClient, appId: string) {
  client.setQueryData<AppList>(queryKeys.apps, (old) => (old && old.apps.some((a) => a.id === appId) ? { ...old, apps: old.apps.filter((a) => a.id !== appId) } : old));
}

/** A job_status / cancel_requested of the app's job: the row's last job fields (no request). */
export function setAppJobInCache(client: QueryClient, appId: string, jobId: string, status: Schemas["JobStatus"]) {
  client.setQueryData<AppList>(queryKeys.apps, (old) => {
    if (!old) return old;
    const i = old.apps.findIndex((a) => a.id === appId);
    if (i < 0) return old;
    const row = old.apps[i];
    // A newer job (pending) becomes the last job; an update of another, older job is ignored.
    if (row.lastJobId !== jobId && status !== "pending" && status !== "in_progress") return old;
    if (row.lastJobId === jobId && row.lastJobStatus === status) return old;
    const apps = old.apps.slice();
    apps[i] = { ...row, lastJobId: jobId, lastJobStatus: status };
    return { ...old, apps };
  });
}

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
