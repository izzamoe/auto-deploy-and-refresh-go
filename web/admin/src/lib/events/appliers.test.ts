// @vitest-environment jsdom
// 002 T051 (FR-004, FR-015; subscription.md §4): topic events -> TanStack Query cache.
// Deltas never fetch; invalidations refetch ONLY active queries, debounced; an event caused by
// this tab (origin = its X-Admin-Client) refetches nothing. Every mutation carries X-Admin-Client.
import { QueryClient, QueryClientProvider, QueryObserver } from "@tanstack/react-query";
import { act, cleanup, render } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, getAdminClientId, resetAuthStateForTests, tabClientId } from "@/lib/api/client";
import { queryKeys } from "@/lib/query";
import { INVALIDATE_DEBOUNCE_MS, STATS_DEBOUNCE_MS, createAppliers } from "./appliers";
import { AdminEventProvider, useTopicFrames } from "./provider";
import type { SocketLike } from "./provider";
import { parseFrame } from "./types";
import type { TopicFrame } from "./types";

const APP = (over: Record<string, unknown> = {}) => ({
  id: "a1", name: "web", binaryPath: "/b", serviceName: "web.service", githubRepo: "o/r", artifactName: "x", enabled: true,
  createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-25T10:00:00Z", lastJobId: "j1", lastJobStatus: "succeeded", ...over,
});
const ev = (type: string, extra: Record<string, unknown>) => parseFrame(JSON.stringify({ protocol: "3", id: "evt-1", ts: "2026-11-25T10:00:00Z", type, ...extra })) as TopicFrame;

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const appliers = createAppliers(client);
  const fetches: string[] = [];
  /** An active query (an observer is subscribed, like a mounted component). */
  const active = async (queryKey: readonly unknown[], data: unknown) => {
    const obs = new QueryObserver(client, { queryKey, queryFn: async () => (fetches.push(JSON.stringify(queryKey)), data), staleTime: Infinity });
    const unsub = obs.subscribe(() => {});
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    return unsub;
  };
  /** Cached but not on screen. */
  const inactive = (queryKey: readonly unknown[], data: unknown) => client.setQueryData(queryKey, data);
  const settle = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
  const count = () => fetches.length;
  return { client, appliers, fetches, active, inactive, settle, count };
}

beforeEach(() => {
  resetAuthStateForTests();
  vi.useFakeTimers();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("appliers: deltas (no request)", () => {
  it("app.upserted replaces the row and the app's own entry without any fetch; an older updatedAt never wins", async () => {
    const t = setup();
    await t.active(queryKeys.apps, { apps: [APP()] });
    await t.active(queryKeys.app("a1"), { app: APP() });
    const base = t.count();
    t.appliers.apply(ev("app.upserted", { seq: 2, topic: "apps", appId: "a1", payload: { app: APP({ enabled: false, updatedAt: "2026-11-25T11:00:00Z" }) } }), { own: false });
    await t.settle(2_000);
    expect(t.count()).toBe(base);
    expect((t.client.getQueryData(queryKeys.apps) as { apps: { enabled: boolean }[] }).apps[0].enabled).toBe(false);
    expect((t.client.getQueryData(queryKeys.app("a1")) as { app: { enabled: boolean } }).app.enabled).toBe(false);
    // a replayed, older delta does not undo the newer state
    t.appliers.apply(ev("app.upserted", { seq: 1, topic: "apps", appId: "a1", payload: { app: APP({ enabled: true, updatedAt: "2026-11-25T10:30:00Z" }) } }), { own: false });
    expect((t.client.getQueryData(queryKeys.apps) as { apps: { enabled: boolean }[] }).apps[0].enabled).toBe(false);
  });

  it("app.upserted of a new app inserts it; app.deleted removes it; still no fetch", async () => {
    const t = setup();
    await t.active(queryKeys.apps, { apps: [APP()] });
    const base = t.count();
    t.appliers.apply(ev("app.upserted", { seq: 2, topic: "apps", appId: "a2", payload: { app: APP({ id: "a2", name: "new" }) } }), { own: false });
    expect((t.client.getQueryData(queryKeys.apps) as { apps: { id: string }[] }).apps.map((a) => a.id)).toEqual(["a1", "a2"]);
    t.appliers.apply(ev("app.deleted", { seq: 3, topic: "apps", appId: "a1", payload: {} }), { own: false });
    expect((t.client.getQueryData(queryKeys.apps) as { apps: { id: string }[] }).apps.map((a) => a.id)).toEqual(["a2"]);
    await t.settle(2_000);
    expect(t.count()).toBe(base);
  });

  it("job_status of a new job makes it the app's last job (pending -> in_progress -> failed) without a fetch", async () => {
    const t = setup();
    await t.active(queryKeys.apps, { apps: [APP()] });
    const base = t.count();
    const row = () => (t.client.getQueryData(queryKeys.apps) as { apps: { lastJobId: string; lastJobStatus: string }[] }).apps[0];
    t.appliers.apply(ev("job_status", { seq: 2, topic: "jobs", appId: "a1", jobId: "j2", payload: { status: "pending" } }), { own: false });
    expect(row()).toMatchObject({ lastJobId: "j2", lastJobStatus: "pending" });
    t.appliers.apply(ev("job_status", { seq: 3, topic: "jobs", appId: "a1", jobId: "j1", payload: { status: "failed" } }), { own: false }); // an older job
    expect(row()).toMatchObject({ lastJobId: "j2", lastJobStatus: "pending" });
    t.appliers.apply(ev("job_status", { seq: 4, topic: "jobs", appId: "a1", jobId: "j2", payload: { status: "failed" } }), { own: false });
    expect(row()).toMatchObject({ lastJobId: "j2", lastJobStatus: "failed" });
    await t.settle(2_000);
    expect(t.count()).toBe(base); // no history query is active: nothing fetched
  });
});

describe("appliers: invalidations (active only, debounced)", () => {
  it("stats.changed: ONE fetch after 1 s for an active stats query, however many events arrive", async () => {
    const t = setup();
    await t.active(queryKeys.statsRange("7d"), { n: 1 });
    const base = t.count();
    for (let i = 0; i < 5; i++) t.appliers.apply(ev("stats.changed", { seq: 10 + i, topic: "stats", payload: { reasons: ["job"] } }), { own: false });
    await t.settle(STATS_DEBOUNCE_MS - 1);
    expect(t.count()).toBe(base);
    await t.settle(1);
    await t.settle(0);
    expect(t.fetches.slice(base)).toEqual(['["stats",{"range":"7d"}]']);
  });

  it("stats.changed: NO fetch when the stats are only cached (not on screen); they are marked stale", async () => {
    const t = setup();
    t.inactive(queryKeys.statsRange("7d"), { n: 1 });
    t.appliers.apply(ev("stats.changed", { seq: 1, topic: "stats", payload: { reasons: ["window"] } }), { own: false });
    await t.settle(STATS_DEBOUNCE_MS + 10);
    expect(t.count()).toBe(0);
    expect(t.client.getQueryState(queryKeys.statsRange("7d"))?.isInvalidated).toBe(true);
  });

  it("settings.changed / account.changed / app.config_changed refetch only their own active query", async () => {
    const t = setup();
    await t.active(queryKeys.telegram, { config: {} });
    await t.active(queryKeys.github, { config: {} });
    await t.active(queryKeys.tokens, { tokens: [] });
    await t.active(queryKeys.appEnv("a1"), { envVars: [] });
    await t.active(queryKeys.appArgs("a1"), { args: [] });
    const base = t.count();
    t.appliers.apply(ev("settings.changed", { seq: 1, topic: "settings", payload: { section: "telegram" } }), { own: false });
    t.appliers.apply(ev("account.changed", { seq: 2, topic: "account", payload: { section: "tokens" } }), { own: false });
    t.appliers.apply(ev("app.config_changed", { seq: 3, topic: "apps", appId: "a1", payload: { part: "env", updatedAt: "x" } }), { own: false });
    await t.settle(INVALIDATE_DEBOUNCE_MS);
    await t.settle(0);
    expect(t.fetches.slice(base).sort()).toEqual(['["app-env","a1"]', '["telegram"]', '["tokens"]']);
  });

  it("an event caused by this tab (own origin) fetches nothing", async () => {
    const t = setup();
    await t.active(queryKeys.telegram, { config: {} });
    await t.active(queryKeys.statsRange("7d"), { n: 1 });
    await t.active(queryKeys.tokens, { tokens: [] });
    await t.active(queryKeys.appEnv("a1"), { envVars: [] });
    await t.active(queryKeys.historyPage(undefined, 1, 20), { jobs: [] });
    const base = t.count();
    const own = { own: true };
    t.appliers.apply(ev("settings.changed", { seq: 1, topic: "settings", origin: "c-11111111", payload: { section: "telegram" } }), own);
    t.appliers.apply(ev("stats.changed", { seq: 2, topic: "stats", origin: "c-11111111", payload: { reasons: ["app"] } }), own);
    t.appliers.apply(ev("account.changed", { seq: 3, topic: "account", origin: "c-11111111", payload: { section: "tokens" } }), own);
    t.appliers.apply(ev("app.config_changed", { seq: 4, topic: "apps", appId: "a1", origin: "c-11111111", payload: { part: "env", updatedAt: "x" } }), own);
    t.appliers.apply(ev("job_status", { seq: 5, topic: "jobs", appId: "a1", jobId: "j9", origin: "c-11111111", payload: { status: "pending" } }), own);
    await t.settle(5_000);
    expect(t.count()).toBe(base);
  });
});

// ---- X-Admin-Client ------------------------------------------------------------------------

class FakeSocket implements SocketLike {
  static all: FakeSocket[] = [];
  onopen: SocketLike["onopen"] = null;
  onmessage: SocketLike["onmessage"] = null;
  onclose: SocketLike["onclose"] = null;
  onerror: SocketLike["onerror"] = null;
  close = vi.fn();
  send = vi.fn();
  constructor(public url: string) {
    FakeSocket.all.push(this);
  }
  emit(frame: object) { this.onmessage?.({ data: JSON.stringify(frame) }); }
}

describe("X-Admin-Client (T051)", () => {
  it("every mutation carries the hello clientId (before it: the tab id); reads carry none; events with that origin are this tab's own", async () => {
    FakeSocket.all = [];
    const calls: { url: string; headers: Record<string, string> }[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, headers: init.headers as Record<string, string> });
      return { ok: true, status: 200, json: async () => ({ status: "ok", app: APP() }), headers: new Headers() };
    }));
    const client = new QueryClient();
    const seen: boolean[] = [];
    const applyFrame = (_f: TopicFrame, ctx: { own: boolean }) => seen.push(ctx.own);
    function Probe() { useTopicFrames(() => {}); return null; }
    render(createElement(QueryClientProvider, { client }, createElement(AdminEventProvider, { socketFactory: (u: string) => new FakeSocket(u), applyFrame }, createElement(Probe))));

    await api.toggleApp("a1", false);
    expect(calls[0].headers["X-Admin-Client"]).toBe(tabClientId());
    expect(tabClientId()).toMatch(/^t-[0-9a-f]{8}$/);

    await act(async () => {
      FakeSocket.all[0].emit({ protocol: "3", id: "evt-1", type: "hello", ts: "2026-11-25T10:00:00Z", payload: { serverVersion: "v", heartbeatSeconds: 30, epoch: "E", seq: 0, clientId: "c-abc12345" } });
    });
    expect(getAdminClientId()).toBe("c-abc12345");
    await api.deleteApp("a1");
    await api.saveTelegram({ appId: 0, chatUsername: "", enabled: false, clearAppHash: false, clearBotToken: false });
    await api.listApps();
    expect(calls.slice(1).map((c) => [c.url.split("?")[0], c.headers["X-Admin-Client"]])).toEqual([
      ["/admin/api/v1/apps/a1", "c-abc12345"],
      ["/admin/api/v1/telegram", "c-abc12345"],
      ["/admin/api/v1/apps", undefined],
    ]);

    await act(async () => {
      FakeSocket.all[0].emit({ protocol: "3", id: "evt-2", seq: 1, topic: "summary", type: "summary", ts: "2026-11-25T10:00:00Z", origin: "c-abc12345", payload: { appsTotal: 1, appsEnabled: 1, running: 0, queued: 0 } });
      FakeSocket.all[0].emit({ protocol: "3", id: "evt-3", seq: 2, topic: "summary", type: "summary", ts: "2026-11-25T10:00:00Z", origin: tabClientId(), payload: { appsTotal: 2, appsEnabled: 1, running: 0, queued: 0 } });
      FakeSocket.all[0].emit({ protocol: "3", id: "evt-4", seq: 3, topic: "summary", type: "summary", ts: "2026-11-25T10:00:00Z", origin: "c-99999999", payload: { appsTotal: 3, appsEnabled: 1, running: 0, queued: 0 } });
    });
    expect(seen).toEqual([true, true, false]);
  });
});
