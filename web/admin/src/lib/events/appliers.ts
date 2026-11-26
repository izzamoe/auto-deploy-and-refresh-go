// Appliers (specs/002 subscription.md §4, plan.md §5): what one topic event does to the cached
// data of TanStack Query.
//   - a delta (app.upserted, app.deleted, job_status of the app's job) -> setQueryData, NO request;
//   - an invalidation (stats.changed, settings.changed, account.changed, app.config_changed,
//     job history) -> invalidateQueries({refetchType: "active"}), debounced (300 ms; stats 1 s):
//     only what is on screen is fetched, once, however many events arrive;
//   - an event caused by this very tab (origin = our X-Admin-Client) changes data the tab
//     already updated from its own response: its invalidation is skipped (0 requests).
// Deltas are idempotent (`updatedAt`), so a replayed or own delta is harmless.
import type { QueryClient, QueryKey } from "@tanstack/react-query";
import { queryKeys, removeAppFromCache, setAppJobInCache, upsertAppInCache } from "@/lib/query";
import { isTerminalStatus } from "./types";
import type { TopicFrame } from "./types";

export const INVALIDATE_DEBOUNCE_MS = 300;
export const STATS_DEBOUNCE_MS = 1_000;

export interface Appliers {
  apply(frame: TopicFrame, ctx: { own: boolean }): void;
  dispose(): void;
}

export function createAppliers(client: QueryClient): Appliers {
  const timers = new Map<string, ReturnType<typeof setTimeout>>();

  /** Refetch the ACTIVE queries under `queryKey` once, `delay` ms after the last event. */
  const invalidateActive = (queryKey: QueryKey, delay = INVALIDATE_DEBOUNCE_MS) => {
    const id = JSON.stringify(queryKey);
    const prev = timers.get(id);
    if (prev) clearTimeout(prev);
    timers.set(
      id,
      setTimeout(() => {
        timers.delete(id);
        void client.invalidateQueries({ queryKey, refetchType: "active" });
      }, delay),
    );
  };

  const apply = (frame: TopicFrame, { own }: { own: boolean }) => {
    switch (frame.kind) {
      case "app.upserted":
        upsertAppInCache(client, frame.app);
        return;
      case "app.deleted":
        removeAppFromCache(client, frame.appId);
        return;
      case "app.config_changed": {
        if (own) return; // the editing tab saved it and keeps what it sent
        const key = frame.part === "env" ? queryKeys.appEnv(frame.appId) : frame.part === "args" ? queryKeys.appArgs(frame.appId) : queryKeys.unitPreview(frame.appId);
        invalidateActive(key);
        return;
      }
      case "job_status":
        if (frame.appId) setAppJobInCache(client, frame.appId, frame.jobId, frame.status);
        if (frame.appId && isTerminalStatus(frame.status)) {
          // The release list may have changed; the deploy dialog fetches it when opened (no request now).
          void client.invalidateQueries({ queryKey: queryKeys.releases(frame.appId), refetchType: "none" });
        }
        if (!own) invalidateActive(queryKeys.history);
        return;
      case "cancel_requested":
        if (frame.appId && frame.status) setAppJobInCache(client, frame.appId, frame.jobId, frame.status);
        if (!own) invalidateActive(queryKeys.history);
        return;
      case "stats.changed":
        if (!own) invalidateActive(queryKeys.stats, STATS_DEBOUNCE_MS);
        return;
      case "settings.changed":
        if (!own) invalidateActive(frame.section === "github" ? queryKeys.github : queryKeys.telegram);
        return;
      case "account.changed":
        if (own) return;
        if (frame.section === "tokens") invalidateActive(queryKeys.tokens);
        else if (frame.section === "profile") invalidateActive(queryKeys.session);
        return;
      default:
        return; // summary, service, progress, notice, logs: stores and components (not queries)
    }
  };

  return {
    apply,
    dispose() {
      for (const t of timers.values()) clearTimeout(t);
      timers.clear();
    },
  };
}
