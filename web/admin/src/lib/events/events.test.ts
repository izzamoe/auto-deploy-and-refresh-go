// @vitest-environment jsdom
// T084: event protocol "2" (contracts/events-ws.md): parsing, store semantics, provider
// behaviour. Corrected B51 / B52 / B11 / B12 behaviour (the T078 repro asserted the bugs).
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render } from "@testing-library/react";
import { createElement, useRef } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetAuthStateForTests } from "@/lib/api/client";
import {
  AdminEventProvider,
  BACKOFF_MAX_MS,
  eventsUrl,
  useConnection,
  useJobProgress,
  useNotifications,
  useProgressList,
} from "./provider";
import type { SocketLike } from "./provider";
import { EventStore, MAX_NOTIFICATIONS, PROGRESS_TTL_MS, effectiveStatus } from "./store";
import { parseFrame } from "./types";
import type { Frame, JobProgress } from "./types";

// ---- the JSON examples of contracts/events-ws.md (copied: specs/ is not in git) -------------
const EX = {
  hello: '{"protocol":"2","id":"evt-1","type":"hello","ts":"2026-11-24T10:00:00Z","payload":{"serverVersion":"v1.9.0","sessionExpiresAt":"2026-11-24T22:00:00Z","heartbeatSeconds":30}}',
  snapshot:
    '{"protocol":"2","id":"evt-2","type":"snapshot","ts":"2026-11-24T10:00:00Z","payload":{"jobs":[{"appId":"a1","jobId":"j1","tag":"v1.2.3","status":"in_progress","phase":"downloading","percent":42,"downloadedBytes":1048576,"totalBytes":2490368,"speedBps":524288,"updatedAt":"2026-11-24T10:00:00Z"}]}}',
  progress: '{"v":"2","t":"p","i":"evt-9","ts":"2026-11-24T10:00:01Z","a":"a1","j":"j1","ph":"downloading","st":"in_progress","pct":42,"db":1048576,"tb":2490368,"bps":524288,"msg":""}',
  jobStatus:
    '{"protocol":"2","id":"evt-10","type":"job_status","ts":"2026-11-24T10:00:05Z","appId":"a1","jobId":"j1","payload":{"status":"failed","previousStatus":"in_progress","tag":"v1.2.3","rolledBack":true,"errorCode":"health_check_failed","finishedAt":"2026-11-24T10:00:05Z"}}',
  cancel:
    '{"protocol":"2","id":"evt-11","type":"cancel_requested","ts":"2026-11-24T10:00:02Z","appId":"a1","jobId":"j1","payload":{"status":"cancel_requested","previousStatus":"in_progress","outcome":"signaled","message":"x"}}',
  notice: '{"protocol":"2","id":"evt-12","type":"notice","ts":"2026-11-24T10:00:00Z","payload":{"level":"warning","code":"telegram_reload_failed","message":"m"}}',
  heartbeat: '{"protocol":"2","id":"evt-13","type":"heartbeat","ts":"2026-11-24T10:00:00Z"}',
};

const job = (over: Partial<JobProgress> = {}): JobProgress => ({
  appId: "a1",
  jobId: "j1",
  status: "in_progress",
  phase: "downloading",
  downloadedBytes: 0,
  speedBps: 0,
  ts: 1000,
  ...over,
});

describe("parseFrame: the contract examples", () => {
  it("parses every documented event", () => {
    expect(parseFrame(EX.hello)).toEqual({ kind: "hello", serverVersion: "v1.9.0", sessionExpiresAt: "2026-11-24T22:00:00Z", heartbeatSeconds: 30 });
    expect(parseFrame(EX.heartbeat)).toEqual({ kind: "heartbeat" });
    expect(parseFrame(EX.notice)).toMatchObject({ kind: "notice", level: "warning", code: "telegram_reload_failed", message: "m" });
    expect(parseFrame(EX.cancel)).toMatchObject({ kind: "cancel_requested", appId: "a1", jobId: "j1" });
    expect(parseFrame(EX.jobStatus)).toMatchObject({ kind: "job_status", appId: "a1", jobId: "j1", status: "failed", previousStatus: "in_progress", rolledBack: true, errorCode: "health_check_failed", tag: "v1.2.3" });
  });

  it("snapshot jobs carry the JobStatus in `status`, distinct from the tracker `phase` (B52)", () => {
    const f = parseFrame(EX.snapshot) as Extract<Frame, { kind: "snapshot" }>;
    expect(f.jobs).toHaveLength(1);
    expect(f.jobs[0]).toMatchObject({ appId: "a1", jobId: "j1", status: "in_progress", phase: "downloading", percent: 42, totalBytes: 2490368, downloadedBytes: 1048576, speedBps: 524288, tag: "v1.2.3" });
  });

  it("a snapshot job without `status` is rejected, never defaulted to the phase (B52)", () => {
    const f = parseFrame('{"protocol":"2","type":"snapshot","payload":{"jobs":[{"appId":"a1","jobId":"j1","phase":"downloading"}]}}') as Extract<Frame, { kind: "snapshot" }>;
    expect(f.jobs).toEqual([]);
  });

  it("parses the compact progress frame (job and app are top-level keys a/j)", () => {
    const f = parseFrame(EX.progress) as Extract<Frame, { kind: "progress" }>;
    expect(f.job).toMatchObject({ appId: "a1", jobId: "j1", phase: "downloading", status: "in_progress", percent: 42, downloadedBytes: 1048576, totalBytes: 2490368, speedBps: 524288 });
    expect(f.job.message).toBeUndefined(); // "" -> absent
  });

  it("reads jobId from the top level (B11) and tolerates the deprecated payload copy", () => {
    const legacy = parseFrame('{"protocol":"2","type":"cancel_requested","payload":{"jobId":"jX"}}');
    expect(legacy).toMatchObject({ kind: "cancel_requested", jobId: "jX" });
  });

  it("ignores malformed frames (events-ws.md §5.2)", () => {
    for (const bad of ["not json", "null", "[]", "42", '{"type":42}', '{"protocol":"2","type":"nope"}', '{"protocol":"2","type":"job_status","payload":{"status":"failed"}}', '{"v":"2","t":"p","a":"a1"}', '{"v":"2","t":"p","a":"a1","j":"j1","st":"bogus"}', "{}"]) {
      expect(parseFrame(bad), bad).toBeNull();
    }
  });

  it("another protocol version stops processing (banner, no event handled)", () => {
    expect(parseFrame('{"protocol":"v1","type":"hello"}')).toEqual({ kind: "protocol_mismatch", protocol: "v1" });
    expect(parseFrame('{"type":"snapshot","payload":[]}')).toEqual({ kind: "protocol_mismatch", protocol: "" });
    expect(parseFrame('{"v":"3","t":"p","a":"a","j":"j","st":"pending"}')).toEqual({ kind: "protocol_mismatch", protocol: "3" });
  });
});

describe("EventStore", () => {
  it("snapshot replaces the live set; the status is the job status (B52)", () => {
    const s = new EventStore();
    s.apply({ kind: "progress", job: job({ jobId: "old" }) }, 1);
    s.apply(parseFrame(EX.snapshot) as Frame, 2);
    expect(s.getJob("old")).toBeUndefined();
    expect(s.getJob("j1")).toMatchObject({ status: "in_progress", phase: "downloading" });
    expect(s.getProgressList().map((j) => j.jobId)).toEqual(["j1"]);
  });

  it("cancel_requested marks the job (jobId is top-level, B11)", () => {
    const s = new EventStore();
    s.apply({ kind: "progress", job: job() }, 1);
    s.apply(parseFrame(EX.cancel) as Frame, 2);
    expect(s.getJob("j1")?.status).toBe("cancel_requested");
  });

  it("a terminal job_status removes the progress, notifies once and calls onTerminal (B57, FR-078)", () => {
    const s = new EventStore();
    const terminal = vi.fn();
    s.onTerminal = terminal;
    s.apply({ kind: "progress", job: job() }, 1);
    s.apply(parseFrame(EX.jobStatus) as Frame, 2);
    expect(s.getJob("j1")).toBeUndefined();
    expect(s.getProgressList()).toEqual([]);
    expect(terminal).toHaveBeenCalledTimes(1);
    expect(terminal.mock.calls[0][0]).toMatchObject({ appId: "a1", jobId: "j1", status: "failed" });
    expect(s.getNotifications()).toHaveLength(1);
    expect(s.getNotifications()[0]).toMatchObject({ kind: "job_failed", level: "error", appId: "a1", tag: "v1.2.3" });
  });

  it("a non-terminal job_status updates the live status only", () => {
    const s = new EventStore();
    s.apply({ kind: "progress", job: job({ status: "pending" }) }, 1);
    s.apply({ kind: "job_status", jobId: "j1", appId: "a1", status: "in_progress", ts: 5 }, 2);
    expect(s.getJob("j1")?.status).toBe("in_progress");
    expect(s.getNotifications()).toEqual([]);
  });

  it("drops out-of-order progress frames", () => {
    const s = new EventStore();
    s.apply({ kind: "progress", job: job({ ts: 100, percent: 50 }) }, 1);
    s.apply({ kind: "progress", job: job({ ts: 50, percent: 10 }) }, 2);
    expect(s.getJob("j1")?.percent).toBe(50);
  });

  it("prunes entries silent for 120 s (a lost terminal event)", () => {
    const s = new EventStore();
    s.apply({ kind: "progress", job: job() }, 1_000);
    s.prune(1_000 + PROGRESS_TTL_MS);
    expect(s.getJob("j1")).toBeDefined();
    s.prune(1_000 + PROGRESS_TTL_MS + 1);
    expect(s.getJob("j1")).toBeUndefined();
  });

  it("keeps at most MAX_NOTIFICATIONS, newest first, and has no event history at all (B51)", () => {
    const s = new EventStore();
    for (let i = 0; i < MAX_NOTIFICATIONS + 25; i++) s.apply({ kind: "notice", level: "info", code: `c${i}`, ts: i }, i);
    const n = s.getNotifications();
    expect(n).toHaveLength(MAX_NOTIFICATIONS);
    expect(n[0].code).toBe(`c${MAX_NOTIFICATIONS + 24}`);
    expect(Object.keys(s)).not.toContain("events");
    expect((s as unknown as Record<string, unknown>).events).toBeUndefined();
  });

  it("a protocol mismatch freezes the store and raises one warning", () => {
    const s = new EventStore();
    s.apply({ kind: "progress", job: job() }, 1);
    s.apply({ kind: "protocol_mismatch", protocol: "v1" }, 2);
    s.apply({ kind: "progress", job: job({ jobId: "j2" }) }, 3);
    expect(s.getConnection().status).toBe("stale");
    expect(s.getProgressList()).toEqual([]);
    expect(s.getNotifications().map((n) => n.kind)).toEqual(["events_stale"]);
  });

  it("heartbeats only move lastSeen: nobody is notified and references stay identical", () => {
    const s = new EventStore();
    const conn = s.getConnection();
    const list = s.getProgressList();
    const notes = s.getNotifications();
    const listener = vi.fn();
    s.subscribeConnection(listener);
    s.subscribeProgressList(listener);
    s.subscribeNotifications(listener);
    for (let i = 0; i < 100; i++) s.apply({ kind: "heartbeat" }, 1000 + i);
    expect(listener).not.toHaveBeenCalled();
    expect(s.getConnection()).toBe(conn);
    expect(s.getProgressList()).toBe(list);
    expect(s.getNotifications()).toBe(notes);
    expect(s.lastSeen).toBe(1099);
  });
});

describe("effectiveStatus (B52, events-ws.md §5.3)", () => {
  it("the database status wins unless a live event is newer than the row", () => {
    const row = "2026-11-24T10:00:00Z";
    const t = Date.parse(row);
    expect(effectiveStatus("canceled", row, job({ status: "in_progress", ts: t - 1 }))).toBe("canceled");
    expect(effectiveStatus("canceled", row, job({ status: "in_progress", ts: t }))).toBe("canceled");
    expect(effectiveStatus("pending", row, job({ status: "in_progress", ts: t + 1 }))).toBe("in_progress");
    expect(effectiveStatus("pending", row, undefined)).toBe("pending");
    expect(effectiveStatus(undefined, undefined, job())).toBe("in_progress");
  });
});

// ---- provider -------------------------------------------------------------------------

class FakeSocket implements SocketLike {
  static all: FakeSocket[] = [];
  onopen: SocketLike["onopen"] = null;
  onmessage: SocketLike["onmessage"] = null;
  onclose: SocketLike["onclose"] = null;
  onerror: SocketLike["onerror"] = null;
  close = vi.fn();
  constructor(public url: string) {
    FakeSocket.all.push(this);
  }
  open() { this.onopen?.({}); }
  send(frame: string) { this.onmessage?.({ data: frame }); }
  drop(code: number) { this.onclose?.({ code }); }
}

function makeClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidate = vi.spyOn(client, "invalidateQueries");
  return { client, invalidate };
}

function Wrapper({ client, children }: { client: QueryClient; children?: ReactNode }) {
  return createElement(QueryClientProvider, { client }, createElement(AdminEventProvider, { socketFactory: (u) => new FakeSocket(u) }, children));
}

beforeEach(() => {
  FakeSocket.all = [];
  resetAuthStateForTests();
  vi.useFakeTimers();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("AdminEventProvider", () => {
  const mount = (ui: ReactNode = null) => {
    const { client, invalidate } = makeClient();
    const view = render(createElement(Wrapper, { client }, ui));
    return { ...view, client, invalidate, socket: () => FakeSocket.all[FakeSocket.all.length - 1] };
  };

  it("connects once to /admin/events/v1/ws?protocol=2 (ws and wss)", () => {
    const { socket } = mount();
    expect(FakeSocket.all).toHaveLength(1);
    expect(socket().url).toBe(`ws://${window.location.host}/admin/events/v1/ws?protocol=2`);
    expect(eventsUrl({ protocol: "http:", host: "localhost" })).toBe("ws://localhost/admin/events/v1/ws?protocol=2");
    expect(eventsUrl({ protocol: "https:", host: "example.test" })).toBe("wss://example.test/admin/events/v1/ws?protocol=2");
  });

  it("heartbeats do not re-render consumers; a progress frame re-renders only its own job (B51)", async () => {
    const renders = { conn: 0, j1: 0, j2: 0, list: 0, notes: 0 };
    function Probe() {
      const c = useRef(0);
      c.current++;
      useConnection();
      renders.conn = c.current;
      return null;
    }
    function J1() { useJobProgress("j1"); renders.j1++; return null; }
    function J2() { useJobProgress("j2"); renders.j2++; return null; }
    function List() { useProgressList(); renders.list++; return null; }
    function Notes() { useNotifications(); renders.notes++; return null; }
    const { socket } = mount(createElement("div", null, createElement(Probe), createElement(J1), createElement(J2), createElement(List), createElement(Notes)));
    act(() => { socket().open(); socket().send(EX.hello); });
    const before = { ...renders };

    for (let i = 0; i < 100; i++) act(() => socket().send(EX.heartbeat));
    expect(renders).toEqual(before); // 100 heartbeats: zero renders

    act(() => socket().send(EX.progress)); // job j1 only
    expect(renders.j1).toBe(before.j1 + 1);
    expect(renders.j2).toBe(before.j2); // another job's component is untouched
    expect(renders.conn).toBe(before.conn);
    expect(renders.notes).toBe(before.notes);
  });

  it("applies hello and snapshot to the consumers", () => {
    let conn: ReturnType<typeof useConnection> | undefined;
    let live: JobProgress | undefined;
    function Probe() { conn = useConnection(); live = useJobProgress("j1"); return null; }
    const { socket } = mount(createElement(Probe));
    expect(conn?.status).toBe("connecting");
    act(() => { socket().open(); socket().send(EX.hello); socket().send(EX.snapshot); });
    expect(conn).toMatchObject({ status: "open", serverVersion: "v1.9.0" });
    expect(live).toMatchObject({ status: "in_progress", phase: "downloading", percent: 42 });
  });

  it("a terminal job_status invalidates apps, history, stats, the service status and the releases of that app (FR-078)", () => {
    const { socket, invalidate } = mount();
    act(() => { socket().open(); socket().send(EX.hello); socket().send(EX.snapshot); });
    invalidate.mockClear();
    act(() => socket().send(EX.jobStatus));
    const keys = invalidate.mock.calls.map((c) => JSON.stringify((c[0] as { queryKey: unknown }).queryKey));
    expect(keys).toEqual(expect.arrayContaining(['["apps"]', '["history"]', '["stats"]', '["service-status","a1"]', '["releases","a1"]']));
  });

  it("ignores malformed frames without failing", () => {
    const { socket } = mount();
    act(() => { socket().open(); socket().send("garbage"); socket().send("{}"); socket().send(EX.hello); });
    // still alive and processing
    expect(FakeSocket.all).toHaveLength(1);
  });

  it("reconnects with backoff 1 s, 2 s, 4 s ... capped at 30 s, and resets after an open", async () => {
    const { socket } = mount();
    expect(FakeSocket.all).toHaveLength(1);
    act(() => socket().drop(1006));
    await act(async () => { vi.advanceTimersByTime(999); });
    expect(FakeSocket.all).toHaveLength(1);
    await act(async () => { vi.advanceTimersByTime(1); });
    expect(FakeSocket.all).toHaveLength(2);
    act(() => socket().drop(1006)); // second failure: 2 s
    await act(async () => { vi.advanceTimersByTime(1999); });
    expect(FakeSocket.all).toHaveLength(2);
    await act(async () => { vi.advanceTimersByTime(1); });
    expect(FakeSocket.all).toHaveLength(3);
    for (let i = 0; i < 8; i++) {
      act(() => socket().drop(1006));
      await act(async () => { vi.advanceTimersByTime(BACKOFF_MAX_MS); });
    }
    const n = FakeSocket.all.length;
    act(() => socket().drop(1006));
    await act(async () => { vi.advanceTimersByTime(BACKOFF_MAX_MS - 1); });
    expect(FakeSocket.all).toHaveLength(n); // never longer than the cap...
    await act(async () => { vi.advanceTimersByTime(1); });
    expect(FakeSocket.all).toHaveLength(n + 1); // ...and exactly the cap
    act(() => { socket().open(); socket().drop(1006); }); // open resets the backoff
    await act(async () => { vi.advanceTimersByTime(1000); });
    expect(FakeSocket.all).toHaveLength(n + 2);
  });

  it("refetches everything after a RECONNECT (events may have been missed), not on the first connect", async () => {
    const { socket, invalidate } = mount();
    act(() => socket().open());
    expect(invalidate).not.toHaveBeenCalled();
    act(() => socket().drop(1006));
    await act(async () => { vi.advanceTimersByTime(1000); });
    act(() => socket().open());
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate.mock.calls[0]).toEqual([]);
  });

  it("4401 (session ended): no reconnect loop, ONE session probe, ONE redirect to login (B12, B50)", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, pathname: "/admin/apps/", search: "", hash: "", assign });
    const fetchMock = vi.fn(async (_url: string) => ({ ok: false, status: 401, json: async () => ({ status: "error", code: "session_expired", error: "x", errors: [] }) }));
    vi.stubGlobal("fetch", fetchMock);
    let status = "";
    function Probe() { status = useConnection().status; return null; }
    const { socket } = mount(createElement(Probe));
    await act(async () => { socket().drop(4401); });
    expect(status).toBe("unauthorized");
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0][0])).toBe("/admin/api/v1/session");
    expect(assign).toHaveBeenCalledTimes(1);
    await act(async () => { vi.advanceTimersByTime(60_000); });
    expect(FakeSocket.all).toHaveLength(1); // never reconnected
  });

  it("4401 with a still-valid session reconnects", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ username: "a", mustChangePassword: false }) })));
    const { socket } = mount();
    await act(async () => { socket().drop(4401); });
    await act(async () => { vi.advanceTimersByTime(1000); });
    expect(FakeSocket.all).toHaveLength(2);
  });

  it("4409 (protocol) shows the banner state and never reconnects", async () => {
    let status = "";
    let notes: readonly { kind: string }[] = [];
    function Probe() { status = useConnection().status; notes = useNotifications(); return null; }
    const { socket } = mount(createElement(Probe));
    act(() => socket().drop(4409));
    expect(status).toBe("stale");
    expect(notes.map((n) => n.kind)).toEqual(["events_stale"]);
    await act(async () => { vi.advanceTimersByTime(60_000); });
    expect(FakeSocket.all).toHaveLength(1);
  });

  it("1008 (origin) is terminal too", async () => {
    let status = "";
    function Probe() { status = useConnection().status; return null; }
    const { socket } = mount(createElement(Probe));
    act(() => socket().drop(1008));
    expect(status).toBe("forbidden");
    await act(async () => { vi.advanceTimersByTime(60_000); });
    expect(FakeSocket.all).toHaveLength(1);
  });

  it("unmount closes the socket and stops reconnecting", async () => {
    const { socket, unmount } = mount();
    const first = socket();
    unmount();
    expect(first.close).toHaveBeenCalled();
    await act(async () => { vi.advanceTimersByTime(60_000); });
    expect(FakeSocket.all).toHaveLength(1);
  });

  it("there is no test hook on window in any mode (B60)", () => {
    mount();
    expect((window as unknown as Record<string, unknown>).AdminUITest).toBeUndefined();
  });
});
