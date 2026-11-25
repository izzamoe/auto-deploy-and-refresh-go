// Event WebSocket protocol "2" (contracts/events-ws.md), written by hand from the contract.
// `parseFrame` validates one text frame and returns a typed frame, or null for anything the
// contract does not describe (malformed frames are ignored, events-ws.md §5.2).
import type { Schemas } from "@/lib/api/client";

export const PROTOCOL = "2";
export const EVENTS_PATH = "/admin/events/v1/ws";

export type JobStatus = Schemas["JobStatus"];
export const TERMINAL_STATUSES: readonly JobStatus[] = ["succeeded", "failed", "canceled"];
const JOB_STATUSES: readonly string[] = ["pending", "in_progress", "cancel_requested", "succeeded", "failed", "canceled"];

export function isTerminalStatus(status: string): boolean {
  return (TERMINAL_STATUSES as readonly string[]).includes(status);
}

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

export interface HelloFrame {
  kind: "hello";
  serverVersion?: string;
  sessionExpiresAt?: string;
  heartbeatSeconds?: number;
}
export interface SnapshotFrame {
  kind: "snapshot";
  jobs: JobProgress[];
}
export interface ProgressFrame {
  kind: "progress";
  job: JobProgress;
}
export interface JobStatusFrame {
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
export interface CancelRequestedFrame {
  kind: "cancel_requested";
  appId?: string;
  jobId: string;
  message?: string;
  ts: number;
}
export interface NoticeFrame {
  kind: "notice";
  level: string;
  code?: string;
  message?: string;
  ts: number;
}
export interface HeartbeatFrame {
  kind: "heartbeat";
}
/** The server speaks another protocol version: stop processing and ask for a reload. */
export interface ProtocolMismatchFrame {
  kind: "protocol_mismatch";
  protocol: string;
}

export type Frame =
  | HelloFrame
  | SnapshotFrame
  | ProgressFrame
  | JobStatusFrame
  | CancelRequestedFrame
  | NoticeFrame
  | HeartbeatFrame
  | ProtocolMismatchFrame;

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const str = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);
const num = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) ? v : undefined);
const isStatus = (v: unknown): v is JobStatus => typeof v === "string" && JOB_STATUSES.includes(v);

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

  // Compact progress frame (§3.3): {"v":"2","t":"p",...}
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
  // The first event tells whether the page and the server speak the same protocol.
  if (data.protocol !== PROTOCOL) {
    return { kind: "protocol_mismatch", protocol: String(data.protocol ?? "") };
  }
  const payload = isObj(data.payload) ? data.payload : {};
  const ts = tsOf(data.ts, now);
  const appId = str(data.appId);
  const jobId = str(data.jobId);

  switch (type) {
    case "hello":
      return {
        kind: "hello",
        serverVersion: str(payload.serverVersion),
        sessionExpiresAt: str(payload.sessionExpiresAt),
        heartbeatSeconds: num(payload.heartbeatSeconds),
      };
    case "snapshot": {
      const list = Array.isArray(payload.jobs) ? payload.jobs : [];
      const jobs = list.flatMap((j) => (isObj(j) ? [toProgress(j, ts)] : [])).filter((j): j is JobProgress => j !== null);
      return { kind: "snapshot", jobs };
    }
    case "job_status": {
      const status = payload.status;
      if (!jobId || !isStatus(status)) return null;
      return {
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
      // jobId is top-level; payload.jobId is the deprecated copy kept for one release.
      const id = jobId ?? str(payload.jobId);
      if (!id) return null;
      return { kind: "cancel_requested", appId, jobId: id, message: str(payload.message), ts };
    }
    case "notice":
      return { kind: "notice", level: str(payload.level) ?? "info", code: str(payload.code), message: str(payload.message), ts };
    case "heartbeat":
      return { kind: "heartbeat" };
    default:
      return null;
  }
}
