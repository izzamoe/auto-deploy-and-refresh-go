// Event WebSocket protocol "3" (specs/002 contracts/events-ws.v3.md, subscription.md), written
// by hand from the contract. `parseFrame` validates one text frame and returns a typed frame,
// or null for anything the contract does not describe (malformed frames are ignored).
import type { Schemas } from "@/lib/api/client";

export const PROTOCOL = "3";
export const EVENTS_PATH = "/admin/events/v1/ws";

export type JobStatus = Schemas["JobStatus"];
export type App = Schemas["App"];
export const TERMINAL_STATUSES: readonly JobStatus[] = ["succeeded", "failed", "canceled"];
const JOB_STATUSES: readonly string[] = ["pending", "in_progress", "cancel_requested", "succeeded", "failed", "canceled"];

export function isTerminalStatus(status: string): boolean {
  return (TERMINAL_STATUSES as readonly string[]).includes(status);
}

/** Topics of protocol 3 (subscription.md §1). */
export const TOPICS = ["summary", "jobs", "apps", "service", "stats", "settings", "account", "logs:system"] as const;
export type Topic = (typeof TOPICS)[number];
export const isTopic = (v: unknown): v is Topic => typeof v === "string" && (TOPICS as readonly string[]).includes(v);

/** systemd states of topic `service` (events-ws.v3.md §2.2). */
export const SERVICE_STATES = ["active", "inactive", "failed", "activating", "deactivating", "reloading", "unknown"] as const;
export type ServiceState = (typeof SERVICE_STATES)[number];
const toServiceState = (v: unknown): ServiceState => ((SERVICE_STATES as readonly unknown[]).includes(v) ? (v as ServiceState) : "unknown");

/** Live progress of one job (snapshot entry or compact `p` frame). */
export interface JobProgress {
  appId: string;
  jobId: string;
  tag?: string;
  /** JobStatus: what the UI shows. `phase` is only the tracker stage (B52). */
  status: JobStatus;
  phase: string;
  percent?: number;
  downloadedBytes: number;
  totalBytes?: number;
  speedBps: number;
  message?: string;
  /** Server time of the frame (ms since epoch), for "newer than the DB row" (B52). */
  ts: number;
}

export interface SummaryState {
  appsTotal: number;
  appsEnabled: number;
  running: number;
  queued: number;
}

export interface LogLine {
  ts: string;
  priority: number;
  message: string;
}

/** Envelope fields of a topic event: `seq` only for state events (resume), `origin` = clientId of the actor tab. */
export interface EventMeta {
  seq?: number;
  topic?: Topic;
  origin?: string;
}

// ---- control frames ------------------------------------------------------------------
export interface HelloFrame {
  kind: "hello";
  serverVersion?: string;
  sessionExpiresAt?: string;
  heartbeatSeconds?: number;
  epoch?: string;
  seq?: number;
  clientId?: string;
}
export interface SubscribedFrame {
  kind: "subscribed";
  ref?: string;
  topics: { name: Topic; state?: unknown }[];
}
export interface UnsubscribedFrame {
  kind: "unsubscribed";
  ref?: string;
  topics: Topic[];
}
export interface ResumedFrame {
  kind: "resumed";
  ref?: string;
  fromSeq: number;
  toSeq: number;
  replayed: number;
}
export interface ResyncFrame {
  kind: "resync";
  ref?: string;
  topics: Topic[];
  reason: string;
}
export interface ErrorFrame {
  kind: "error";
  ref?: string;
  code: string;
  topic?: string;
}
export interface HeartbeatFrame {
  kind: "heartbeat";
}
/** The server speaks another protocol version: stop processing and ask for a reload. */
export interface ProtocolMismatchFrame {
  kind: "protocol_mismatch";
  protocol: string;
}

// ---- topic events --------------------------------------------------------------------
export interface SnapshotFrame extends EventMeta {
  kind: "snapshot";
  jobs: JobProgress[];
}
export interface ProgressFrame {
  kind: "progress";
  job: JobProgress;
}
export interface JobStatusFrame extends EventMeta {
  kind: "job_status";
  appId?: string;
  jobId: string;
  status: JobStatus;
  previousStatus?: string;
  tag?: string;
  rolledBack?: boolean;
  errorCode?: string;
  ts: number;
}
export interface CancelRequestedFrame extends EventMeta {
  kind: "cancel_requested";
  appId?: string;
  jobId: string;
  status?: JobStatus;
  message?: string;
  ts: number;
}
export interface NoticeFrame extends EventMeta {
  kind: "notice";
  level: string;
  code?: string;
  message?: string;
  ts: number;
}
export interface SummaryFrame extends EventMeta {
  kind: "summary";
  state: SummaryState;
}
export interface AppUpsertedFrame extends EventMeta {
  kind: "app.upserted";
  appId: string;
  app: App;
}
export interface AppDeletedFrame extends EventMeta {
  kind: "app.deleted";
  appId: string;
}
export interface AppConfigChangedFrame extends EventMeta {
  kind: "app.config_changed";
  appId: string;
  part: "env" | "args" | "unit";
  updatedAt?: string;
}
export interface ServiceStatusFrame extends EventMeta {
  kind: "service.status";
  appId: string;
  state: ServiceState;
}
export interface StatsChangedFrame extends EventMeta {
  kind: "stats.changed";
  reasons: string[];
}
export interface SettingsChangedFrame extends EventMeta {
  kind: "settings.changed";
  section: "github" | "telegram";
}
export interface AccountChangedFrame extends EventMeta {
  kind: "account.changed";
  section: "profile" | "tokens" | "sessions";
}
export interface LogsLinesFrame extends EventMeta {
  kind: "logs.lines";
  lines: LogLine[];
  cursor?: string;
}
export interface LogsGapFrame extends EventMeta {
  kind: "logs.gap";
  dropped: number;
  reason: string;
}

export type ControlFrame = HelloFrame | SubscribedFrame | UnsubscribedFrame | ResumedFrame | ResyncFrame | ErrorFrame | HeartbeatFrame | ProtocolMismatchFrame;
export type TopicFrame =
  | SnapshotFrame
  | JobStatusFrame
  | CancelRequestedFrame
  | NoticeFrame
  | SummaryFrame
  | AppUpsertedFrame
  | AppDeletedFrame
  | AppConfigChangedFrame
  | ServiceStatusFrame
  | StatsChangedFrame
  | SettingsChangedFrame
  | AccountChangedFrame
  | LogsLinesFrame
  | LogsGapFrame;
export type Frame = ControlFrame | ProgressFrame | TopicFrame;

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const str = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);
const num = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) ? v : undefined);
const isStatus = (v: unknown): v is JobStatus => typeof v === "string" && JOB_STATUSES.includes(v);
const topicList = (v: unknown): Topic[] => (Array.isArray(v) ? v.filter(isTopic) : []);

function tsOf(v: unknown, fallback: number): number {
  const s = str(v);
  const t = s ? Date.parse(s) : NaN;
  return Number.isNaN(t) ? fallback : t;
}

function toProgress(o: Obj, fallbackTs: number): JobProgress | null {
  const appId = str(o.appId);
  const jobId = str(o.jobId);
  if (!appId || !jobId || !isStatus(o.status)) return null; // `status` is mandatory (B52)
  const total = num(o.totalBytes);
  const percent = num(o.percent);
  return {
    appId,
    jobId,
    tag: str(o.tag),
    status: o.status,
    phase: str(o.phase) ?? "",
    percent: percent !== undefined && percent >= 0 ? percent : undefined,
    downloadedBytes: num(o.downloadedBytes) ?? 0,
    totalBytes: total !== undefined && total > 0 ? total : undefined,
    speedBps: num(o.speedBps) ?? 0,
    message: str(o.message),
    ts: tsOf(o.updatedAt, fallbackTs),
  };
}

function toSummary(v: unknown): SummaryState | null {
  if (!isObj(v)) return null;
  const appsTotal = num(v.appsTotal);
  const appsEnabled = num(v.appsEnabled);
  const running = num(v.running);
  const queued = num(v.queued);
  if (appsTotal === undefined || appsEnabled === undefined || running === undefined || queued === undefined) return null;
  return { appsTotal, appsEnabled, running, queued };
}

/** State of topic `summary` in a `subscribed` ack (events-ws.v3.md §3). */
export function parseSummaryState(v: unknown): SummaryState | null {
  return toSummary(v);
}

/** State of topic `service` in a `subscribed` ack: {statuses:[{appId,state}], observedAt}. */
export function parseServiceState(v: unknown): Map<string, ServiceState> | null {
  if (!isObj(v) || !Array.isArray(v.statuses)) return null;
  const out = new Map<string, ServiceState>();
  for (const s of v.statuses) {
    if (!isObj(s)) continue;
    const appId = str(s.appId);
    if (appId) out.set(appId, toServiceState(s.state));
  }
  return out;
}

function toApp(v: unknown): App | null {
  if (!isObj(v)) return null;
  if (!str(v.id) || typeof v.name !== "string" || typeof v.enabled !== "boolean") return null;
  const { liveProgress: _drop, ...rest } = v; // progress only arrives on topic jobs
  return rest as unknown as App;
}

/** Parses one frame; `now` is the fallback timestamp for frames without `ts`. */
export function parseFrame(raw: unknown, now: number = Date.now()): Frame | null {
  let data: unknown = raw;
  if (typeof raw === "string") {
    try {
      data = JSON.parse(raw);
    } catch {
      return null;
    }
  }
  if (!isObj(data)) return null;

  // Compact progress frame (§2.3): {"v":"3","t":"p",...}
  if (data.t === "p") {
    if (data.v !== PROTOCOL) return data.v === undefined ? null : { kind: "protocol_mismatch", protocol: String(data.v) };
    const appId = str(data.a);
    const jobId = str(data.j);
    if (!appId || !jobId || !isStatus(data.st)) return null;
    const tb = num(data.tb);
    const pct = num(data.pct);
    return {
      kind: "progress",
      job: {
        appId,
        jobId,
        status: data.st,
        phase: str(data.ph) ?? "",
        percent: pct !== undefined && pct >= 0 ? pct : undefined,
        downloadedBytes: num(data.db) ?? 0,
        totalBytes: tb !== undefined && tb > 0 ? tb : undefined,
        speedBps: num(data.bps) ?? 0,
        message: str(data.msg),
        ts: tsOf(data.ts, now),
      },
    };
  }

  const type = str(data.type);
  if (!type) return null;
  // The first frame tells whether the page and the server speak the same protocol.
  if (data.protocol !== PROTOCOL) {
    return { kind: "protocol_mismatch", protocol: String(data.protocol ?? "") };
  }
  const payload = isObj(data.payload) ? data.payload : {};
  const ts = tsOf(data.ts, now);
  const appId = str(data.appId);
  const jobId = str(data.jobId);
  const seq = num(data.seq);
  const meta: EventMeta = {};
  if (seq !== undefined && seq >= 1) meta.seq = seq;
  if (isTopic(data.topic)) meta.topic = data.topic;
  const origin = str(data.origin);
  if (origin) meta.origin = origin;

  switch (type) {
    case "hello":
      return {
        kind: "hello",
        serverVersion: str(payload.serverVersion),
        sessionExpiresAt: str(payload.sessionExpiresAt),
        heartbeatSeconds: num(payload.heartbeatSeconds),
        epoch: str(payload.epoch),
        seq: num(payload.seq),
        clientId: str(payload.clientId),
      };
    case "subscribed": {
      const list = Array.isArray(payload.topics) ? payload.topics : [];
      const topics = list.flatMap((t) => (isObj(t) && isTopic(t.name) ? [{ name: t.name, state: t.state }] : []));
      return { kind: "subscribed", ref: str(payload.ref), topics };
    }
    case "unsubscribed":
      return { kind: "unsubscribed", ref: str(payload.ref), topics: topicList(payload.topics) };
    case "resumed":
      return { kind: "resumed", ref: str(payload.ref), fromSeq: num(payload.fromSeq) ?? 0, toSeq: num(payload.toSeq) ?? 0, replayed: num(payload.replayed) ?? 0 };
    case "resync":
      return { kind: "resync", ref: str(payload.ref), topics: topicList(payload.topics), reason: str(payload.reason) ?? "gap" };
    case "error": {
      const code = str(payload.code);
      if (!code) return null;
      return { kind: "error", ref: str(payload.ref), code, topic: str(payload.topic) };
    }
    case "heartbeat":
      return { kind: "heartbeat" };
    case "snapshot": {
      const list = Array.isArray(payload.jobs) ? payload.jobs : [];
      const jobs = list.flatMap((j) => (isObj(j) ? [toProgress(j, ts)] : [])).filter((j): j is JobProgress => j !== null);
      return { ...meta, kind: "snapshot", jobs };
    }
    case "job_status": {
      const status = payload.status;
      if (!jobId || !isStatus(status)) return null;
      return {
        ...meta,
        kind: "job_status",
        appId,
        jobId,
        status,
        previousStatus: str(payload.previousStatus),
        tag: str(payload.tag),
        rolledBack: typeof payload.rolledBack === "boolean" ? payload.rolledBack : undefined,
        errorCode: str(payload.errorCode),
        ts,
      };
    }
    case "cancel_requested": {
      if (!jobId) return null; // v3: top-level only (the payload copy is gone)
      return { ...meta, kind: "cancel_requested", appId, jobId, status: isStatus(payload.status) ? payload.status : undefined, message: str(payload.message), ts };
    }
    case "notice":
      return { ...meta, kind: "notice", level: str(payload.level) ?? "info", code: str(payload.code), message: str(payload.message), ts };
    case "summary": {
      const state = toSummary(payload);
      return state ? { ...meta, kind: "summary", state } : null;
    }
    case "app.upserted": {
      const app = toApp(payload.app);
      if (!app) return null;
      return { ...meta, kind: "app.upserted", appId: appId ?? app.id, app };
    }
    case "app.deleted":
      return appId ? { ...meta, kind: "app.deleted", appId } : null;
    case "app.config_changed": {
      const part = payload.part;
      if (!appId || (part !== "env" && part !== "args" && part !== "unit")) return null;
      return { ...meta, kind: "app.config_changed", appId, part, updatedAt: str(payload.updatedAt) };
    }
    case "service.status":
      return appId ? { ...meta, kind: "service.status", appId, state: toServiceState(payload.state) } : null;
    case "stats.changed":
      return { ...meta, kind: "stats.changed", reasons: Array.isArray(payload.reasons) ? payload.reasons.filter((r): r is string => typeof r === "string") : [] };
    case "settings.changed": {
      const section = payload.section;
      return section === "github" || section === "telegram" ? { ...meta, kind: "settings.changed", section } : null;
    }
    case "account.changed": {
      const section = payload.section;
      return section === "profile" || section === "tokens" || section === "sessions" ? { ...meta, kind: "account.changed", section } : null;
    }
    case "logs.lines": {
      const list = Array.isArray(payload.lines) ? payload.lines : [];
      const lines = list.flatMap((l) => (isObj(l) && typeof l.message === "string" ? [{ ts: str(l.ts) ?? "", priority: num(l.priority) ?? 6, message: l.message }] : []));
      return { ...meta, kind: "logs.lines", lines, cursor: str(payload.cursor) };
    }
    case "logs.gap":
      return { ...meta, kind: "logs.gap", dropped: num(payload.dropped) ?? 0, reason: str(payload.reason) ?? "rate" };
    default:
      return null;
  }
}
