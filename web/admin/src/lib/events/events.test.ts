// @vitest-environment jsdom
// T084 -> 002 T050: event protocol "3" (specs/002 contracts/events-ws.v3.md, subscription.md):
// parsing, store semantics, provider behaviour (topics, resume, resync, epoch, 4401).
// Corrected B51 / B52 / B11 / B12 behaviour (the T078 repro asserted the bugs). Migrated from
// protocol "2" under RV-2 (T050): fixtures are v3, the fake socket pushes with `emit` and
// records the client's ops in `send`.
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
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
  useTopic,
  useTopicFrames,
} from "./provider";
import type { SocketLike } from "./provider";
import { EventStore, MAX_NOTIFICATIONS, PROGRESS_TTL_MS, effectiveStatus } from "./store";
import { parseFrame } from "./types";
import type { Frame, JobProgress } from "./types";

// ---- the JSON examples of contracts/events-ws.v3.md (copied: specs/ is not in git) ----------
const EX = {
  hello:
    '{"protocol":"3","id":"evt-1","type":"hello","ts":"2026-11-24T10:00:00Z","payload":{"serverVersion":"v1.9.0","sessionExpiresAt":"2026-11-24T22:00:00Z","heartbeatSeconds":30,"epoch":"9c0f","seq":41,"clientId":"c-7f3a9b21","limits":{"maxTopics":16,"maxOpsPer10s":20,"maxFrameBytes":4096}}}',
  snapshot:
    '{"protocol":"3","id":"evt-2","topic":"jobs","type":"snapshot","ts":"2026-11-24T10:00:00Z","payload":{"jobs":[{"appId":"a1","jobId":"j1","tag":"v1.2.3","status":"in_progress","phase":"downloading","percent":42,"downloadedBytes":1048576,"totalBytes":2490368,"speedBps":524288,"updatedAt":"2026-11-24T10:00:00Z"}]}}',
  progress: '{"v":"3","t":"p","i":"evt-9","ts":"2026-11-24T10:00:01Z","a":"a1","j":"j1","ph":"downloading","st":"in_progress","pct":42,"db":1048576,"tb":2490368,"bps":524288,"msg":""}',
  jobStatus:
    '{"protocol":"3","id":"evt-10","seq":10,"topic":"jobs","type":"job_status","ts":"2026-11-24T10:00:05Z","appId":"a1","jobId":"j1","payload":{"status":"failed","previousStatus":"in_progress","tag":"v1.2.3","rolledBack":true,"errorCode":"health_check_failed","finishedAt":"2026-11-24T10:00:05Z"}}',
  cancel:
    '{"protocol":"3","id":"evt-11","seq":11,"topic":"jobs","type":"cancel_requested","ts":"2026-11-24T10:00:02Z","appId":"a1","jobId":"j1","payload":{"status":"cancel_requested","previousStatus":"in_progress","outcome":"signaled","message":"x"}}',
  notice: '{"protocol":"3","id":"evt-12","seq":12,"topic":"summary","type":"notice","ts":"2026-11-24T10:00:00Z","payload":{"level":"warning","code":"telegram_reload_failed","message":"m"}}',
  heartbeat: '{"protocol":"3","id":"evt-13","type":"heartbeat","ts":"2026-11-24T10:00:00Z"}',
  // events-ws.v3.md §1 envelope example (+ an App body)
  appUpserted:
    '{"protocol":"3","id":"evt-42","seq":42,"topic":"apps","type":"app.upserted","ts":"2026-11-25T10:00:00Z","appId":"a1","origin":"c-7f3a9b21","payload":{"app":{"id":"a1","name":"web","binaryPath":"/opt/web/bin","serviceName":"web.service","githubRepo":"acme/web","artifactName":"web","enabled":false,"createdAt":"2026-11-01T00:00:00Z","updatedAt":"2026-11-25T10:00:00Z","liveProgress":{"x":1}}}}',
  appDeleted: '{"protocol":"3","id":"evt-43","seq":43,"topic":"apps","type":"app.deleted","ts":"2026-11-25T10:00:00Z","appId":"a2","payload":{}}',
  configChanged: '{"protocol":"3","id":"evt-44","seq":44,"topic":"apps","type":"app.config_changed","ts":"2026-11-25T10:00:00Z","appId":"a1","payload":{"part":"env","updatedAt":"2026-11-25T10:00:00Z"}}',
  summary: '{"protocol":"3","id":"evt-45","seq":45,"topic":"summary","type":"summary","ts":"2026-11-25T10:00:00Z","payload":{"appsTotal":5,"appsEnabled":3,"running":1,"queued":2}}',
  service: '{"protocol":"3","id":"evt-46","seq":46,"topic":"service","type":"service.status","ts":"2026-11-25T10:00:00Z","appId":"a1","payload":{"state":"failed"}}',
  stats: '{"protocol":"3","id":"evt-47","seq":47,"topic":"stats","type":"stats.changed","ts":"2026-11-25T10:00:00Z","payload":{"reasons":["job","window"]}}',
  settings: '{"protocol":"3","id":"evt-48","seq":48,"topic":"settings","type":"settings.changed","ts":"2026-11-25T10:00:00Z","payload":{"section":"telegram"}}',
  account: '{"protocol":"3","id":"evt-49","seq":49,"topic":"account","type":"account.changed","ts":"2026-11-25T10:00:00Z","payload":{"section":"tokens"}}',
  logsLines: '{"protocol":"3","id":"evt-50","topic":"logs:system","type":"logs.lines","ts":"2026-11-25T10:00:00Z","payload":{"lines":[{"ts":"2026-11-25T10:00:00Z","priority":6,"message":"started"}],"cursor":"s=1;i=2"}}',
  logsGap: '{"protocol":"3","id":"evt-51","topic":"logs:system","type":"logs.gap","ts":"2026-11-25T10:00:00Z","payload":{"dropped":12,"reason":"rate"}}',
  subscribed:
    '{"protocol":"3","id":"evt-52","type":"subscribed","ts":"2026-11-25T10:00:00Z","payload":{"ref":"r1","topics":[{"name":"summary","state":{"appsTotal":5,"appsEnabled":3,"running":1,"queued":2}},{"name":"service","state":{"statuses":[{"appId":"a1","state":"active"}],"observedAt":"2026-11-25T10:00:00Z"}},{"name":"apps"}]}}',
  unsubscribed: '{"protocol":"3","id":"evt-53","type":"unsubscribed","ts":"2026-11-25T10:00:00Z","payload":{"ref":"r2","topics":["jobs"]}}',
  resumed: '{"protocol":"3","id":"evt-54","type":"resumed","ts":"2026-11-25T10:00:00Z","payload":{"ref":"r3","fromSeq":41,"toSeq":45,"replayed":4}}',
  resync: '{"protocol":"3","id":"evt-55","type":"resync","ts":"2026-11-25T10:00:00Z","payload":{"ref":"r3","topics":["apps"],"reason":"gap"}}',
  error: '{"protocol":"3","id":"evt-56","type":"error","ts":"2026-11-25T10:00:00Z","payload":{"ref":"r4","code":"forbidden_topic","topic":"logs:system"}}',
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
    expect(parseFrame(EX.hello)).toEqual({ kind: "hello", serverVersion: "v1.9.0", sessionExpiresAt: "2026-11-24T22:00:00Z", heartbeatSeconds: 30, epoch: "9c0f", seq: 41, clientId: "c-7f3a9b21" });
    expect(parseFrame(EX.heartbeat)).toEqual({ kind: "heartbeat" });
    expect(parseFrame(EX.notice)).toMatchObject({ kind: "notice", level: "warning", code: "telegram_reload_failed", message: "m" });
    expect(parseFrame(EX.cancel)).toMatchObject({ kind: "cancel_requested", appId: "a1", jobId: "j1" });
    expect(parseFrame(EX.jobStatus)).toMatchObject({ kind: "job_status", appId: "a1", jobId: "j1", status: "failed", previousStatus: "in_progress", rolledBack: true, errorCode: "health_check_failed", tag: "v1.2.3", seq: 10, topic: "jobs" });
  });

  it("parses every protocol-3 topic event and control frame of the contract (events-ws.v3.md §1-§2)", () => {
    const up = parseFrame(EX.appUpserted) as Extract<Frame, { kind: "app.upserted" }>;
    expect(up).toMatchObject({ kind: "app.upserted", appId: "a1", seq: 42, topic: "apps", origin: "c-7f3a9b21" });
    expect(up.app).toMatchObject({ id: "a1", name: "web", enabled: false });
    expect(up.app).not.toHaveProperty("liveProgress"); // progress only on topic jobs
    expect(parseFrame(EX.appDeleted)).toEqual({ kind: "app.deleted", appId: "a2", seq: 43, topic: "apps" });
    expect(parseFrame(EX.configChanged)).toEqual({ kind: "app.config_changed", appId: "a1", part: "env", updatedAt: "2026-11-25T10:00:00Z", seq: 44, topic: "apps" });
    expect(parseFrame(EX.summary)).toEqual({ kind: "summary", state: { appsTotal: 5, appsEnabled: 3, running: 1, queued: 2 }, seq: 45, topic: "summary" });
    expect(parseFrame(EX.service)).toEqual({ kind: "service.status", appId: "a1", state: "failed", seq: 46, topic: "service" });
    expect(parseFrame(EX.stats)).toEqual({ kind: "stats.changed", reasons: ["job", "window"], seq: 47, topic: "stats" });
    expect(parseFrame(EX.settings)).toEqual({ kind: "settings.changed", section: "telegram", seq: 48, topic: "settings" });
    expect(parseFrame(EX.account)).toEqual({ kind: "account.changed", section: "tokens", seq: 49, topic: "account" });
    expect(parseFrame(EX.logsLines)).toEqual({ kind: "logs.lines", topic: "logs:system", lines: [{ ts: "2026-11-25T10:00:00Z", priority: 6, message: "started" }], cursor: "s=1;i=2" });
    expect(parseFrame(EX.logsGap)).toEqual({ kind: "logs.gap", topic: "logs:system", dropped: 12, reason: "rate" });
    expect(parseFrame(EX.subscribed)).toMatchObject({ kind: "subscribed", ref: "r1", topics: [{ name: "summary" }, { name: "service" }, { name: "apps" }] });
    expect(parseFrame(EX.unsubscribed)).toEqual({ kind: "unsubscribed", ref: "r2", topics: ["jobs"] });
    expect(parseFrame(EX.resumed)).toEqual({ kind: "resumed", ref: "r3", fromSeq: 41, toSeq: 45, replayed: 4 });
    expect(parseFrame(EX.resync)).toEqual({ kind: "resync", ref: "r3", topics: ["apps"], reason: "gap" });
    expect(parseFrame(EX.error)).toEqual({ kind: "error", ref: "r4", code: "forbidden_topic", topic: "logs:system" });
    // an unknown service state is "unknown", never a guess
    expect(parseFrame('{"protocol":"3","type":"service.status","appId":"a1","seq":1,"topic":"service","payload":{"state":"weird"}}')).toMatchObject({ state: "unknown" });
  });

  it("snapshot jobs carry the JobStatus in `status`, distinct from the tracker `phase` (B52)", () => {
    const f = parseFrame(EX.snapshot) as Extract<Frame, { kind: "snapshot" }>;
    expect(f.jobs).toHaveLength(1);
    expect(f.jobs[0]).toMatchObject({ appId: "a1", jobId: "j1", status: "in_progress", phase: "downloading", percent: 42, totalBytes: 2490368, downloadedBytes: 1048576, speedBps: 524288, tag: "v1.2.3" });
  });

  it("a snapshot job without `status` is rejected, never defaulted to the phase (B52)", () => {
    const f = parseFrame('{"protocol":"3","topic":"jobs","type":"snapshot","payload":{"jobs":[{"appId":"a1","jobId":"j1","phase":"downloading"}]}}') as Extract<Frame, { kind: "snapshot" }>;
    expect(f.jobs).toEqual([]);
  });

  it("parses the compact progress frame (job and app are top-level keys a/j)", () => {
    const f = parseFrame(EX.progress) as Extract<Frame, { kind: "progress" }>;
    expect(f.job).toMatchObject({ appId: "a1", jobId: "j1", phase: "downloading", status: "in_progress", percent: 42, downloadedBytes: 1048576, totalBytes: 2490368, speedBps: 524288 });
    expect(f.job.message).toBeUndefined(); // "" -> absent
  });

  it("reads jobId from the top level (B11); protocol 3 dropped the payload copy, so a payload-only jobId is rejected", () => {
    expect(parseFrame('{"protocol":"3","seq":3,"topic":"jobs","type":"cancel_requested","jobId":"jX","payload":{"status":"canceled"}}')).toMatchObject({ kind: "cancel_requested", jobId: "jX", status: "canceled" });
    expect(parseFrame('{"protocol":"3","type":"cancel_requested","payload":{"jobId":"jX"}}')).toBeNull();
  });

  it("ignores malformed frames (events-ws.md §5.2)", () => {
    for (const bad of [
      "not json", "null", "[]", "42", '{"type":42}', '{"protocol":"3","type":"nope"}', '{"protocol":"3","type":"job_status","payload":{"status":"failed"}}', '{"v":"3","t":"p","a":"a1"}', '{"v":"3","t":"p","a":"a1","j":"j1","st":"bogus"}', "{}",
      '{"protocol":"3","type":"app.upserted","payload":{"app":{"name":"x"}}}', '{"protocol":"3","type":"app.deleted","payload":{}}', '{"protocol":"3","type":"app.config_changed","appId":"a1","payload":{"part":"secret"}}',
      '{"protocol":"3","type":"summary","payload":{"appsTotal":1}}', '{"protocol":"3","type":"settings.changed","payload":{"section":"vault"}}', '{"protocol":"3","type":"error","payload":{}}',
    ]) {
      expect(parseFrame(bad), bad).toBeNull();
    }
  });

  it("another protocol version stops processing (banner, no event handled)", () => {
    expect(parseFrame('{"protocol":"v1","type":"hello"}')).toEqual({ kind: "protocol_mismatch", protocol: "v1" });
    expect(parseFrame('{"protocol":"2","type":"hello","payload":{}}')).toEqual({ kind: "protocol_mismatch", protocol: "2" });
    expect(parseFrame('{"type":"snapshot","payload":[]}')).toEqual({ kind: "protocol_mismatch", protocol: "" });
    expect(parseFrame('{"v":"2","t":"p","a":"a","j":"j","st":"pending"}')).toEqual({ kind: "protocol_mismatch", protocol: "2" });
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

  it("cancel_requested with status canceled (queued job canceled at once, RV-14) removes the live entry", () => {
    const s = new EventStore();
    s.apply({ kind: "progress", job: job({ status: "pending" }) }, 1);
    s.apply({ kind: "cancel_requested", jobId: "j1", appId: "a1", status: "canceled", ts: 5 }, 2);
    expect(s.getJob("j1")).toBeUndefined();
  });

  it("summary and service.status update their stores; an equal summary keeps the same reference", () => {
    const s = new EventStore();
    const l = vi.fn();
    s.subscribeSummary(l);
    s.apply(parseFrame(EX.summary) as Frame, 1);
    const first = s.getSummary();
    expect(first).toEqual({ appsTotal: 5, appsEnabled: 3, running: 1, queued: 2 });
    s.apply(parseFrame(EX.summary) as Frame, 2);
    expect(s.getSummary()).toBe(first);
    expect(l).toHaveBeenCalledTimes(1);
    s.apply(parseFrame(EX.service) as Frame, 3);
    expect(s.getService("a1")).toBe("failed");
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
  /** Ops the client sends (protocol 3). */
  send = vi.fn();
  constructor(public url: string) {
    FakeSocket.all.push(this);
  }
  open() { this.onopen?.({}); }
  /** A frame pushed by the server. */
  emit(frame: string | object) { this.onmessage?.({ data: typeof frame === "string" ? frame : JSON.stringify(frame) }); }
  drop(code: number) { this.onclose?.({ code }); }
  subs(): SubOp[] {
    return this.send.mock.calls.map((c) => JSON.parse(String(c[0])) as SubOp).filter((o) => o.op === "sub");
  }
}

interface SubOp {
  op: string;
  ref: string;
  topics: { name: string; progress?: boolean; after?: string }[];
  resume?: { epoch: string; seq: number };
}

const v3 = (type: string, extra: Record<string, unknown> = {}) => ({ protocol: "3", id: `evt-${type}`, type, ts: "2026-11-25T10:00:00Z", ...extra });
const hello = (epoch: string, seq: number, clientId = "c-00000001") =>
  v3("hello", { payload: { serverVersion: "v2", heartbeatSeconds: 30, epoch, seq, clientId, limits: { maxTopics: 16, maxOpsPer10s: 20, maxFrameBytes: 4096 } } });
const appEvent = (seq: number, id: string) =>
  v3("app.upserted", { seq, topic: "apps", appId: id, payload: { app: { id, name: id, enabled: true, binaryPath: "/b", serviceName: `${id}.service`, githubRepo: "o/r", artifactName: "x", createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-25T10:00:00Z" } } });
/** Answers every sub op not answered yet: subscribed, plus resumed{toSeq} when the op carried resume. */
function ackAll(s: FakeSocket, answered: Set<string>, toSeq?: number) {
  for (const op of s.subs()) {
    if (answered.has(op.ref)) continue;
    answered.add(op.ref);
    s.emit(v3("subscribed", { payload: { ref: op.ref, topics: op.topics.map((t) => ({ name: t.name })) } }));
    if (op.resume) s.emit(v3("resumed", { payload: { ref: op.ref, fromSeq: op.resume.seq, toSeq: toSeq ?? op.resume.seq, replayed: 0 } }));
  }
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

  it("connects once to /admin/events/v1/ws?protocol=3 (ws and wss)", () => {
    const { socket } = mount();
    expect(FakeSocket.all).toHaveLength(1);
    expect(socket().url).toBe(`ws://${window.location.host}/admin/events/v1/ws?protocol=3`);
    expect(eventsUrl({ protocol: "http:", host: "localhost" })).toBe("ws://localhost/admin/events/v1/ws?protocol=3");
    expect(eventsUrl({ protocol: "https:", host: "example.test" })).toBe("wss://example.test/admin/events/v1/ws?protocol=3");
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
    act(() => { socket().open(); socket().emit(EX.hello); });
    const before = { ...renders };

    for (let i = 0; i < 100; i++) act(() => socket().emit(EX.heartbeat));
    expect(renders).toEqual(before); // 100 heartbeats: zero renders

    act(() => socket().emit(EX.progress)); // job j1 only
    expect(renders.j1).toBe(before.j1 + 1);
    expect(renders.j2).toBe(before.j2); // another job's component is untouched
    expect(renders.conn).toBe(before.conn);
    expect(renders.notes).toBe(before.notes);
  });

  it("applies hello and snapshot to the consumers", async () => {
    let conn: ReturnType<typeof useConnection> | undefined;
    let live: JobProgress | undefined;
    function Probe() { conn = useConnection(); live = useJobProgress("j1"); return null; }
    const { socket } = mount(createElement(Probe));
    expect(conn?.status).toBe("connecting");
    await act(async () => { socket().open(); socket().emit(EX.hello); });
    expect(conn).toMatchObject({ status: "connecting", serverVersion: "v1.9.0" }); // reading progress asked for topic jobs...
    expect(socket().subs()).toEqual([{ op: "sub", ref: expect.any(String), topics: [{ name: "jobs", progress: true }] }]);
    await act(async () => { ackAll(socket(), new Set()); socket().emit(EX.snapshot); });
    expect(conn).toMatchObject({ status: "live", serverVersion: "v1.9.0" }); // ...live once confirmed
    expect(live).toMatchObject({ status: "in_progress", phase: "downloading", percent: 42 });
  });

  it("a terminal job_status invalidates apps, history, stats, the service status and the releases of that app (FR-078)", () => {
    const { socket, invalidate } = mount();
    act(() => { socket().open(); socket().emit(EX.hello); socket().emit(EX.snapshot); });
    invalidate.mockClear();
    act(() => socket().emit(EX.jobStatus));
    const keys = invalidate.mock.calls.map((c) => JSON.stringify((c[0] as { queryKey: unknown }).queryKey));
    expect(keys).toEqual(expect.arrayContaining(['["apps"]', '["history"]', '["stats"]', '["service-status","a1"]', '["releases","a1"]']));
  });

  it("ignores malformed frames without failing", () => {
    const { socket } = mount();
    act(() => { socket().open(); socket().emit("garbage"); socket().emit("{}"); socket().emit(EX.hello); });
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

  it("a RECONNECT issues no REST refetch when resume succeeds and only the topics of the open page resync otherwise", async () => {
    const appsFn = vi.fn(async () => ({ apps: [] }));
    const statsFn = vi.fn(async () => ({ n: 1 }));
    // The open page uses topic apps; a stats query of another page sits in the cache, inactive.
    function Page() {
      const fresh = useTopic("apps");
      useQuery({ queryKey: ["apps"], queryFn: appsFn, staleTime: fresh ? Infinity : 0 });
      return null;
    }
    const { socket, client, invalidate } = mount(createElement(Page));
    await act(async () => { await client.prefetchQuery({ queryKey: ["stats", { range: "7d" }], queryFn: statsFn }); });
    const answered = new Set<string>();
    await act(async () => { socket().open(); socket().emit(hello("E1", 5)); });
    expect(socket().subs()).toEqual([{ op: "sub", ref: expect.any(String), topics: [{ name: "apps" }] }]); // first connect: no resume
    await act(async () => { ackAll(socket(), answered); socket().emit(appEvent(6, "a1")); });
    await act(async () => { await vi.advanceTimersByTimeAsync(10); });
    const calls = () => appsFn.mock.calls.length + statsFn.mock.calls.length;
    const before = calls();
    invalidate.mockClear();

    // 1) dropped (1006), reconnected, the server replays: zero REST, zero invalidation.
    act(() => socket().drop(1006));
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(FakeSocket.all).toHaveLength(2);
    await act(async () => { socket().open(); socket().emit(hello("E1", 9)); });
    expect(socket().subs()).toEqual([{ op: "sub", ref: expect.any(String), topics: [{ name: "apps" }], resume: { epoch: "E1", seq: 6 } }]);
    await act(async () => { ackAll(socket(), new Set(), 9); });
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });
    expect(calls()).toBe(before);
    expect(invalidate).not.toHaveBeenCalled();

    // 2) dropped again; this time the ring no longer has seq 9: resync{gap} of topic apps only.
    act(() => socket().drop(1006));
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    await act(async () => { socket().open(); socket().emit(hello("E1", 2000)); });
    const op = socket().subs()[0];
    expect(op.resume).toEqual({ epoch: "E1", seq: 9 });
    await act(async () => {
      socket().emit(v3("subscribed", { payload: { ref: op.ref, topics: [{ name: "apps" }] } }));
      socket().emit(v3("resync", { payload: { ref: op.ref, topics: ["apps"], reason: "gap" } }));
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });
    expect(calls() - before).toBe(1); // ONE targeted refetch...
    expect(appsFn.mock.calls.length).toBe(2); // ...of the open page's topic
    expect(statsFn).toHaveBeenCalledTimes(1); // the other page's data was not touched
    expect(invalidate.mock.calls.every((c) => c.length === 1 && (c[0] as { queryKey?: unknown }).queryKey !== undefined)).toBe(true); // never "invalidate everything"
  });

  it("epoch change resets lastSeq (RV-9): seq restarts at 1 after a server restart and new events are applied", async () => {
    const accepted: string[] = [];
    function Page() {
      useTopic("apps");
      useTopicFrames((f) => accepted.push(`${f.seq}:${f.topic}`));
      return null;
    }
    const { socket } = mount(createElement(Page));
    await act(async () => { socket().open(); socket().emit(hello("OLD", 500)); });
    await act(async () => { ackAll(socket(), new Set()); socket().emit(appEvent(501, "a1")); });
    expect(accepted).toEqual(["501:apps"]);
    act(() => socket().drop(1006));
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    await act(async () => { socket().open(); socket().emit(hello("NEW", 0)); });
    expect(socket().subs()[0].resume).toBeUndefined(); // the old position means nothing in the new epoch
    await act(async () => { ackAll(socket(), new Set()); });
    // seq 1 of the new epoch is far below the old lastSeq (501): it must be applied, not dropped.
    await act(async () => { socket().emit(appEvent(1, "a9")); socket().emit(appEvent(2, "a9")); });
    expect(accepted).toEqual(["501:apps", "1:apps", "2:apps"]);
  });

  it("deduplicates per topic (RV3-8): a replay of an older seq of another topic is applied, a repeat of the same topic is dropped", async () => {
    const accepted: string[] = [];
    function Page() {
      useTopic("apps");
      useTopic("stats");
      useTopicFrames((f) => accepted.push(`${f.seq}:${f.topic}`));
      return null;
    }
    const { socket } = mount(createElement(Page));
    await act(async () => { socket().open(); socket().emit(hello("E", 10)); });
    await act(async () => { ackAll(socket(), new Set()); });
    await act(async () => {
      socket().emit(appEvent(20, "a1"));
      socket().emit(v3("stats.changed", { seq: 15, topic: "stats", payload: { reasons: ["job"] } })); // older than 20, other topic
      socket().emit(appEvent(20, "a1")); // duplicate
      socket().emit(appEvent(19, "a1")); // older than the last apps event
    });
    expect(accepted).toEqual(["20:apps", "15:stats"]);
  });

  it("4401 then session ok reconnects with resume (RV3-14: unauthorized -> connecting -> live)", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ username: "a", mustChangePassword: false }) })));
    const statuses: string[] = [];
    function Page() {
      useTopic("apps");
      const s = useConnection().status;
      if (statuses[statuses.length - 1] !== s) statuses.push(s);
      return null;
    }
    const { socket } = mount(createElement(Page));
    await act(async () => { socket().open(); socket().emit(hello("E", 3)); });
    await act(async () => { ackAll(socket(), new Set()); socket().emit(appEvent(4, "a1")); });
    expect(statuses[statuses.length - 1]).toBe("live");
    await act(async () => { socket().drop(4401); });
    expect(statuses).toContain("unauthorized");
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(FakeSocket.all).toHaveLength(2);
    await act(async () => { socket().open(); socket().emit(hello("E", 7)); });
    expect(socket().subs()).toEqual([{ op: "sub", ref: expect.any(String), topics: [{ name: "apps" }], resume: { epoch: "E", seq: 4 } }]);
    expect(statuses[statuses.length - 1]).toBe("connecting"); // waiting for the resume
    await act(async () => { ackAll(socket(), new Set(), 7); });
    const tail = statuses.slice(statuses.indexOf("unauthorized"));
    expect(tail).toEqual(["unauthorized", "connecting", "live"]);
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
