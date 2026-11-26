// @vitest-environment jsdom
// 002 T050 (FR-011, FR-012, FR-015, FR-018; subscription.md §2, §6): the SubscriptionManager
// and the provider's topic life cycle: refcount + 1 s unsub debounce, one batched sub per tick,
// resume per topic, visibility (hidden > 10 s) and offline/online.
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, cleanup, render } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetAuthStateForTests } from "@/lib/api/client";
import { createQueryClient, liveQueryOptions } from "@/lib/query";
import { AdminEventProvider, useConnection, useTopic } from "./provider";
import type { SocketLike } from "./provider";
import { HIDDEN_AFTER_MS, SubscriptionManager, UNSUB_DELAY_MS } from "./subscriptions";
import type { ClientOp } from "./subscriptions";
import type { Topic } from "./types";

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
  open() { this.onopen?.({}); }
  emit(frame: object) { this.onmessage?.({ data: JSON.stringify(frame) }); }
  drop(code: number) { this.onclose?.({ code }); }
  ops(): ClientOp[] { return this.send.mock.calls.map((c) => JSON.parse(String(c[0])) as ClientOp); }
}
const latest = () => FakeSocket.all[FakeSocket.all.length - 1];
const v3 = (type: string, extra: Record<string, unknown> = {}) => ({ protocol: "3", id: `evt-${type}`, type, ts: "2026-11-25T10:00:00Z", ...extra });
const hello = (epoch = "E", seq = 0) => v3("hello", { payload: { serverVersion: "v", heartbeatSeconds: 30, epoch, seq, clientId: "c-00000001" } });
const flush = () => act(async () => { await vi.advanceTimersByTimeAsync(0); });

function ack(s: FakeSocket) {
  for (const op of s.ops()) {
    if (op.op !== "sub") continue;
    s.emit(v3("subscribed", { payload: { ref: op.ref, topics: op.topics.map((t) => ({ name: t.name })) } }));
    if (op.resume) s.emit(v3("resumed", { payload: { ref: op.ref, fromSeq: op.resume.seq, toSeq: op.resume.seq, replayed: 0 } }));
  }
}

function Uses({ topic, progress }: { topic: Topic; progress?: boolean }) {
  useTopic(topic, progress === undefined ? undefined : { progress });
  return null;
}

let status = "";
function Status() {
  status = useConnection().status;
  return null;
}

function mount(children: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(createElement(QueryClientProvider, { client }, createElement(AdminEventProvider, { socketFactory: (u: string) => new FakeSocket(u) }, createElement(Status), children)));
  const rerender = (next: ReactNode) => view.rerender(createElement(QueryClientProvider, { client }, createElement(AdminEventProvider, { socketFactory: (u: string) => new FakeSocket(u) }, createElement(Status), next)));
  return { ...view, client, rerender };
}

beforeEach(() => {
  FakeSocket.all = [];
  status = "";
  resetAuthStateForTests();
  vi.useFakeTimers();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("SubscriptionManager (unit)", () => {
  function manager() {
    const sent: ClientOp[] = [];
    const left: Topic[] = [];
    const m = new SubscriptionManager({ send: (op) => (sent.push(op), true), onLeave: (t) => left.push(t) });
    return { m, sent, left };
  }

  it("two users of one topic -> ONE sub; both gone -> ONE unsub after 1 s", async () => {
    const { m, sent, left } = manager();
    m.attach({ epoch: "E", seq: 0 });
    const r1 = m.acquire("apps");
    const r2 = m.acquire("apps");
    await flush();
    expect(sent).toEqual([{ op: "sub", ref: "r1", topics: [{ name: "apps" }] }]);
    m.onSubscribed("r1", [{ name: "apps" }]);
    r1();
    r2();
    r2(); // a second release call is ignored
    await act(async () => { await vi.advanceTimersByTimeAsync(UNSUB_DELAY_MS - 1); });
    expect(sent).toHaveLength(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(sent).toEqual([{ op: "sub", ref: "r1", topics: [{ name: "apps" }] }, { op: "unsub", ref: "r2", topics: ["apps"] }]);
    expect(left).toEqual(["apps"]);
  });

  it("navigating between two pages that both use a topic does not churn (release + acquire within 1 s)", async () => {
    const { m, sent, left } = manager();
    m.attach({ epoch: "E", seq: 0 });
    const r = m.acquire("apps");
    await flush();
    m.onSubscribed("r1", [{ name: "apps" }]);
    r();
    await act(async () => { await vi.advanceTimersByTimeAsync(300); });
    m.acquire("apps");
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(sent).toHaveLength(1);
    expect(left).toEqual([]);
  });

  it("batches the topics acquired in one tick into ONE sub frame", async () => {
    const { m, sent } = manager();
    m.attach({ epoch: "E", seq: 0 });
    m.acquire("summary");
    m.acquire("apps");
    m.acquire("jobs", { progress: true });
    m.acquire("service");
    await flush();
    expect(sent).toEqual([{ op: "sub", ref: "r1", topics: [{ name: "summary" }, { name: "apps" }, { name: "jobs", progress: true }, { name: "service" }] }]);
  });

  it("a topic opened again after it was left is subscribed with resume{epoch, seq} of that topic (RV-8)", async () => {
    const { m, sent } = manager();
    m.attach({ epoch: "E", seq: 3 });
    const r = m.acquire("apps");
    m.acquire("summary");
    await flush();
    m.onSubscribed("r1", [{ name: "apps" }, { name: "summary" }]);
    expect(m.accept("apps", 7)).toBe(true);
    expect(m.accept("summary", 9)).toBe(true);
    r();
    await act(async () => { await vi.advanceTimersByTimeAsync(UNSUB_DELAY_MS); });
    expect(m.accept("summary", 12)).toBe(true);
    m.acquire("apps");
    await flush();
    // complete up to 9 (the newest seq seen when apps was left), not the newer 12
    expect(sent[sent.length - 1]).toEqual({ op: "sub", ref: "r3", topics: [{ name: "apps" }], resume: { epoch: "E", seq: 9 } });
  });

  it("a topic the server refuses is not asked again and does not block `live`", async () => {
    const settled: boolean[] = [];
    const sent: ClientOp[] = [];
    const m = new SubscriptionManager({ send: (op) => (sent.push(op), true), onSettled: (s) => settled.push(s) });
    m.attach({ epoch: "E", seq: 0 });
    m.acquire("logs:system");
    await flush();
    expect(settled[settled.length - 1]).toBe(false);
    m.onError("r1", "logs:system", "forbidden_topic");
    expect(settled[settled.length - 1]).toBe(true);
    m.detach();
    m.attach({ epoch: "E", seq: 0 });
    await flush();
    expect(sent.filter((o) => o.op === "sub")).toHaveLength(1);
  });
});

describe("provider topic life cycle", () => {
  it("subscribes after hello, is live only once every topic is confirmed", async () => {
    mount(createElement("div", null, createElement(Uses, { topic: "summary" }), createElement(Uses, { topic: "apps" })));
    await flush();
    expect(latest().send).not.toHaveBeenCalled(); // nothing before hello
    await act(async () => { latest().open(); latest().emit(hello()); });
    expect(latest().ops()).toEqual([{ op: "sub", ref: "r1", topics: [{ name: "summary" }, { name: "apps" }] }]);
    expect(status).toBe("connecting");
    await act(async () => { ack(latest()); });
    expect(status).toBe("live");
  });

  it("unmounting the last user sends ONE unsub 1 s later", async () => {
    const view = mount(createElement("div", null, createElement(Uses, { topic: "apps" }), createElement(Uses, { topic: "apps" })));
    await act(async () => { latest().open(); latest().emit(hello()); });
    await act(async () => { ack(latest()); });
    view.rerender(createElement("div", null, createElement(Uses, { topic: "summary" })));
    await act(async () => { await vi.advanceTimersByTimeAsync(UNSUB_DELAY_MS); });
    expect(latest().ops().filter((o) => o.op === "unsub")).toEqual([{ op: "unsub", ref: expect.any(String), topics: ["apps"] }]);
  });

  it("hidden > 10 s: jobs without progress and logs:system dropped; visible: progress back and logs after the cursor", async () => {
    let visibility: DocumentVisibilityState = "visible";
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
    mount(createElement("div", null, createElement(Uses, { topic: "jobs", progress: true }), createElement(Uses, { topic: "logs:system" })));
    await act(async () => { latest().open(); latest().emit(hello()); });
    await act(async () => { ack(latest()); });
    await act(async () => { latest().emit(v3("logs.lines", { topic: "logs:system", payload: { lines: [{ ts: "t", priority: 6, message: "m" }], cursor: "s=9" } })); });
    latest().send.mockClear();

    visibility = "hidden";
    await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
    await act(async () => { await vi.advanceTimersByTimeAsync(HIDDEN_AFTER_MS - 1); });
    expect(latest().send).not.toHaveBeenCalled(); // a short glance away changes nothing
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    const hiddenOps = latest().ops();
    expect(hiddenOps).toContainEqual({ op: "unsub", ref: expect.any(String), topics: ["logs:system"] });
    expect(hiddenOps).toContainEqual({ op: "sub", ref: expect.any(String), topics: [{ name: "jobs", progress: false }] });
    latest().send.mockClear();

    visibility = "visible";
    await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
    await flush();
    const ops = latest().ops();
    expect(ops).toHaveLength(1);
    expect(ops[0]).toMatchObject({ op: "sub" });
    expect((ops[0] as Extract<ClientOp, { op: "sub" }>).topics).toEqual(expect.arrayContaining([{ name: "jobs", progress: true }, { name: "logs:system", after: "s=9" }]));
    delete (document as unknown as Record<string, unknown>).visibilityState;
  });

  it("navigate away and back: changes made meanwhile are shown with <= 1 REST (RV-8, T052)", async () => {
    // The server's list; another context changes it while this tab is on another page.
    let server = [{ id: "a1", name: "web", enabled: true }];
    const listApps = vi.fn(async () => ({ apps: server.map((a) => ({ ...a })) }));
    let shown: string[] = [];
    function AppsPage() {
      const fresh = useTopic("apps");
      const q = useQuery({ queryKey: ["apps"], queryFn: listApps, ...liveQueryOptions(fresh) });
      shown = (q.data?.apps ?? []).map((a) => `${a.id}:${a.enabled ? "on" : "off"}`);
      return null;
    }
    function AccountPage() {
      useTopic("account");
      return null;
    }
    const client = createQueryClient();
    const tree = (page: ReactNode) => createElement(QueryClientProvider, { client }, createElement(AdminEventProvider, { socketFactory: (u: string) => new FakeSocket(u) }, createElement(Status), page));
    const view = render(tree(createElement(AppsPage)));
    await act(async () => { latest().open(); latest().emit(hello("E", 4)); });
    await act(async () => { ack(latest()); await vi.advanceTimersByTimeAsync(10); });
    expect(shown).toEqual(["a1:on"]);
    expect(listApps).toHaveBeenCalledTimes(1);

    // Leave /apps/: one unsub after 1 s; the cached list is marked stale WITHOUT a fetch.
    view.rerender(tree(createElement(AccountPage)));
    await act(async () => { await vi.advanceTimersByTimeAsync(UNSUB_DELAY_MS); });
    await act(async () => { ack(latest()); });
    expect(latest().ops().filter((o) => o.op === "unsub")).toEqual([{ op: "unsub", ref: expect.any(String), topics: ["apps"] }]);
    expect(client.getQueryState(["apps"])?.isInvalidated).toBe(true);
    expect(listApps).toHaveBeenCalledTimes(1);

    // Meanwhile another context disables the app (this tab no longer receives `apps`).
    server = [{ id: "a1", name: "web", enabled: false }];
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(listApps).toHaveBeenCalledTimes(1);

    // Back to /apps/: the change is shown, with at most ONE request.
    latest().send.mockClear();
    view.rerender(tree(createElement(AppsPage)));
    await act(async () => { await vi.advanceTimersByTimeAsync(10); });
    const op = latest().ops()[0];
    expect(op).toMatchObject({ op: "sub", topics: [{ name: "apps" }], resume: { epoch: "E", seq: 4 } }); // resume from where it was left
    await act(async () => { ack(latest()); await vi.advanceTimersByTimeAsync(10); });
    expect(shown).toEqual(["a1:off"]);
    expect(listApps.mock.calls.length - 1).toBeLessThanOrEqual(1);
  });

  describe("K-1: account resync (subscription.md §4 amendment)", () => {
    function accountPage() {
      const getSession = vi.fn(async () => ({ username: "admin" }));
      const listTokens = vi.fn(async () => ({ tokens: [] as unknown[] }));
      function AccountPage() {
        useTopic("account");
        useQuery({ queryKey: ["session"], queryFn: getSession, staleTime: Infinity });
        useQuery({ queryKey: ["tokens"], queryFn: listTokens, staleTime: Infinity });
        return null;
      }
      return { getSession, listTokens, AccountPage };
    }

    it("resync account reason=epoch refetches tokens only", async () => {
      const { getSession, listTokens, AccountPage } = accountPage();
      mount(createElement(AccountPage));
      await act(async () => { latest().open(); latest().emit(hello("E", 4)); });
      await act(async () => { ack(latest()); await vi.advanceTimersByTimeAsync(10); });
      expect(getSession).toHaveBeenCalledTimes(1);
      expect(listTokens).toHaveBeenCalledTimes(1);

      // 1) a resync{epoch} frame for the account topic.
      await act(async () => { latest().emit(v3("resync", { payload: { topics: ["account"], reason: "epoch" } })); await vi.advanceTimersByTimeAsync(10); });
      expect(listTokens).toHaveBeenCalledTimes(2);
      expect(getSession).toHaveBeenCalledTimes(1);

      // 2) a real server restart: the socket drops and the new hello carries another epoch.
      act(() => latest().drop(1006));
      await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
      expect(FakeSocket.all.length).toBeGreaterThanOrEqual(2);
      await act(async () => { latest().open(); latest().emit(hello("F", 0)); });
      await act(async () => { ack(latest()); await vi.advanceTimersByTimeAsync(10); });
      expect(listTokens).toHaveBeenCalledTimes(3);
      expect(getSession).toHaveBeenCalledTimes(1);
      expect(status).toBe("live");
    });

    it("resync account reason=overflow refetches session and tokens", async () => {
      const { getSession, listTokens, AccountPage } = accountPage();
      mount(createElement(AccountPage));
      await act(async () => { latest().open(); latest().emit(hello("E", 4)); });
      await act(async () => { ack(latest()); await vi.advanceTimersByTimeAsync(10); });
      expect(getSession).toHaveBeenCalledTimes(1);
      expect(listTokens).toHaveBeenCalledTimes(1);

      await act(async () => { latest().emit(v3("resync", { payload: { topics: ["account"], reason: "overflow" } })); await vi.advanceTimersByTimeAsync(10); });
      expect(getSession).toHaveBeenCalledTimes(2);
      expect(listTokens).toHaveBeenCalledTimes(2);

      // gap behaves like overflow: both again, exactly once each.
      await act(async () => { latest().emit(v3("resync", { payload: { topics: ["account"], reason: "gap" } })); await vi.advanceTimersByTimeAsync(10); });
      expect(getSession).toHaveBeenCalledTimes(3);
      expect(listTokens).toHaveBeenCalledTimes(3);
    });
  });

  it("offline: no reconnect attempts until the browser is online again, then ONE immediate reconnect", async () => {
    let online = true;
    vi.stubGlobal("navigator", { ...navigator, get onLine() { return online; } });
    mount(createElement(Uses, { topic: "apps" }));
    await act(async () => { latest().open(); latest().emit(hello()); });
    await act(async () => { ack(latest()); });
    online = false;
    await act(async () => { window.dispatchEvent(new Event("offline")); });
    expect(status).toBe("offline");
    act(() => latest().drop(1006));
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000); });
    expect(FakeSocket.all).toHaveLength(1); // no reconnect loop while offline
    expect(status).toBe("offline");
    online = true;
    await act(async () => { window.dispatchEvent(new Event("online")); });
    expect(FakeSocket.all).toHaveLength(2);
    await act(async () => { latest().open(); latest().emit(hello()); });
    expect(latest().ops()[0]).toMatchObject({ op: "sub", topics: [{ name: "apps" }], resume: { epoch: "E", seq: 0 } });
    await act(async () => { ack(latest()); });
    expect(status).toBe("live");
  });
});
