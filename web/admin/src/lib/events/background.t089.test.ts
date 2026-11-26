// @vitest-environment jsdom
// T089 CR-3 (Q-2=A, FR-031; independent code review): requests the panel makes on its own
// because of an event carry `X-Admin-Background: 1` and the event socket handshake carries
// `?bg=1`, so an idle tab does not extend its session; user actions and page loads do.
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { act } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, resetAuthStateForTests } from "@/lib/api/client";
import { queryKeys } from "@/lib/query";
import { STATS_DEBOUNCE_MS, createAppliers } from "./appliers";
import { defaultSocketFactory, eventsUrl, resyncTopic } from "./provider";
import { EventStore } from "./store";
import { parseFrame } from "./types";
import type { TopicFrame } from "./types";

const ROW = (over: Record<string, unknown> = {}) => ({
  id: "a1", name: "web", binaryPath: "/b", serviceName: "web.service", githubRepo: "o/r", artifactName: "x", enabled: true,
  runAsUser: "", hardening: false, healthUrl: "", requireDigest: true,
  createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-25T10:00:00Z", ...over,
});

describe("T089 CR-3: background requests are marked, user requests are not", () => {
  let requests: { url: string; method: string; headers: Record<string, string> }[];
  beforeEach(() => {
    resetAuthStateForTests();
    vi.useFakeTimers();
    requests = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init: RequestInit) => {
        requests.push({ url, method: init.method ?? "GET", headers: { ...(init.headers as Record<string, string>) } });
        const body = url.includes("/stats") ? { summary: {} } : url.includes("/apps/") ? { status: "updated", app: ROW() } : { apps: [] };
        return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
      }),
    );
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
  const settle = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
  const ev = (type: string, extra: Record<string, unknown>) => parseFrame(JSON.stringify({ protocol: "3", id: "evt-1", ts: "2026-11-25T10:00:00Z", type, ...extra })) as TopicFrame;

  it("the page load is not marked; the refetch caused by stats.changed is; a mutation is not", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    const obs = new QueryObserver(client, { queryKey: queryKeys.stats, queryFn: ({ signal }) => api.getStats({}, { signal }), staleTime: Infinity });
    const unsub = obs.subscribe(() => {});
    await settle(0);
    expect(requests).toHaveLength(1);
    expect(requests[0].headers["X-Admin-Background"]).toBeUndefined(); // page load

    const appliers = createAppliers(client);
    appliers.apply(ev("stats.changed", { seq: 3, topic: "stats", payload: { reasons: ["window"] } }), { own: false });
    await settle(STATS_DEBOUNCE_MS + 50);
    expect(requests).toHaveLength(2);
    expect(requests[1].url).toContain("/stats");
    expect(requests[1].headers["X-Admin-Background"]).toBe("1");

    await api.toggleApp("a1", false); // a user action
    const mutation = requests[requests.length - 1];
    expect(mutation.method).toBe("POST");
    expect(mutation.headers["X-Admin-Background"]).toBeUndefined();

    // A user's own request after the background one is not marked either.
    await client.refetchQueries({ queryKey: queryKeys.stats });
    expect(requests[requests.length - 1].headers["X-Admin-Background"]).toBeUndefined();
    appliers.dispose();
    unsub();
  });

  it("a resync refetch (reconnect, server restart) is marked", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    const obs = new QueryObserver(client, { queryKey: queryKeys.apps, queryFn: ({ signal }) => api.listApps({ signal }), staleTime: Infinity });
    const unsub = obs.subscribe(() => {});
    await settle(0);
    expect(requests[0].headers["X-Admin-Background"]).toBeUndefined();
    await resyncTopic(client, new EventStore(), "apps", { reason: "epoch" } as never);
    await settle(0);
    expect(requests).toHaveLength(2);
    expect(requests[1].headers["X-Admin-Background"]).toBe("1");
    unsub();
  });

  it("the real event socket handshake carries bg=1", () => {
    const opened: string[] = [];
    vi.stubGlobal(
      "WebSocket",
      class {
        constructor(url: string) {
          opened.push(url);
        }
      },
    );
    defaultSocketFactory(eventsUrl({ protocol: "https:", host: "panel.example" }));
    expect(opened).toHaveLength(1);
    const url = new URL(opened[0]);
    expect(url.protocol).toBe("wss:");
    expect(url.pathname).toBe("/admin/events/v1/ws");
    expect(url.searchParams.get("protocol")).toBe("3");
    expect(url.searchParams.get("bg")).toBe("1");
  });
});
