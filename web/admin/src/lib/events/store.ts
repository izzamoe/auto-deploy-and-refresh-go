// Event store (B51, B52, FR-072/073): a tiny external store consumed with
// useSyncExternalStore. There is NO list of past events. Only what the UI shows is kept:
//   - the connection state (rare changes),
//   - live progress per job (bounded: removed on a terminal job_status, replaced by each
//     snapshot, pruned after 120 s of silence),
//   - at most MAX_NOTIFICATIONS notifications of this session.
// Heartbeats only touch `lastSeen`, a plain field: nobody is notified and nothing re-renders.
import { isTerminalStatus } from "./types";
import type { Frame, JobProgress, JobStatus, JobStatusFrame, ServiceState, SummaryState, Topic, TopicFrame } from "./types";

/**
 * Socket state (data-model.md §ConnectionState). `live` = hello received and every topic the
 * page asked for is confirmed (subscribed / resumed); `resyncing` = a resync is being applied.
 */
export type ConnectionStatus = "connecting" | "live" | "reconnecting" | "resyncing" | "offline" | "unauthorized" | "forbidden" | "stale";

/** The four states the header shows (FR-024). */
export type ConnectionIndicator = "live" | "reconnecting" | "resyncing" | "offline";

export function indicatorOf(status: ConnectionStatus): ConnectionIndicator {
  switch (status) {
    case "live":
      return "live";
    case "resyncing":
      return "resyncing";
    case "offline":
    case "forbidden":
    case "stale":
      return "offline";
    default:
      return "reconnecting";
  }
}

export interface ConnectionState {
  status: ConnectionStatus;
  serverVersion?: string;
  sessionExpiresAt?: string;
}

/** Grace before the queries fall back to TanStack defaults when the socket is not live (Q-4=A). */
export const FALLBACK_AFTER_MS = 5_000;

export type NotificationKind = "job_succeeded" | "job_failed" | "job_canceled" | "notice" | "password_change_required" | "events_stale";

export interface AppNotification {
  id: number;
  kind: NotificationKind;
  level: "success" | "error" | "warning" | "info";
  ts: number;
  appId?: string;
  jobId?: string;
  tag?: string;
  /** Server notice code / text (already safe; the UI translates by code when it can). */
  code?: string;
  message?: string;
}

export const MAX_NOTIFICATIONS = 50;
export const PROGRESS_TTL_MS = 120_000;

type Listener = () => void;
type Tracked = JobProgress & { receivedAt: number };

export class EventStore {
  /** Last frame time of any kind (heartbeats included). Not reactive on purpose. */
  lastSeen = 0;
  /** Called (outside of any render) when a job reaches a terminal status. */
  onTerminal?: (frame: JobStatusFrame) => void;

  private connection: ConnectionState = { status: "connecting" };
  private progress = new Map<string, Tracked>();
  private progressList: readonly JobProgress[] = [];
  private notifications: readonly AppNotification[] = [];
  private nextId = 1;

  private connectionListeners = new Set<Listener>();
  private listListeners = new Set<Listener>();
  private notificationListeners = new Set<Listener>();
  private jobListeners = new Map<string, Set<Listener>>();

  // ---- protocol 3: topic state (data-model.md §Stores topik) ------------------------------
  /** Events keep the cached data current: live, or not live for less than FALLBACK_AFTER_MS. */
  private fresh = false;
  /** Topics the server confirmed on the current socket. */
  private topics: ReadonlySet<Topic> = new Set();
  private summary: SummaryState | undefined;
  private service = new Map<string, ServiceState>();
  private freshListeners = new Set<Listener>();
  private topicListeners = new Set<Listener>();
  private summaryListeners = new Set<Listener>();
  private serviceListeners = new Set<Listener>();
  private frameListeners = new Set<(frame: TopicFrame) => void>();
  /** clientIds this tab used (hello.clientId of each socket + the tab id): events with such an origin are our own. */
  private ownOrigins: string[] = [];

  // ---- reads (stable references while nothing changed) --------------------------------
  getConnection = (): ConnectionState => this.connection;
  getJob = (jobId: string): JobProgress | undefined => this.progress.get(jobId);
  getProgressList = (): readonly JobProgress[] => this.progressList;
  getNotifications = (): readonly AppNotification[] => this.notifications;
  isFresh = (): boolean => this.fresh;
  getTopics = (): ReadonlySet<Topic> => this.topics;
  getSummary = (): SummaryState | undefined => this.summary;
  getService = (appId: string): ServiceState | undefined => this.service.get(appId);
  hasService = (appId: string): boolean => this.service.has(appId);

  // ---- subscriptions -------------------------------------------------------------------
  subscribeConnection = (l: Listener) => this.add(this.connectionListeners, l);
  subscribeProgressList = (l: Listener) => this.add(this.listListeners, l);
  subscribeNotifications = (l: Listener) => this.add(this.notificationListeners, l);
  subscribeFresh = (l: Listener) => this.add(this.freshListeners, l);
  subscribeTopics = (l: Listener) => this.add(this.topicListeners, l);
  subscribeSummary = (l: Listener) => this.add(this.summaryListeners, l);
  subscribeService = (l: Listener) => this.add(this.serviceListeners, l);
  /** Every accepted topic event (after dedup), for components reacting to one kind (logs, edit conflicts, announcements). */
  subscribeFrames = (l: (frame: TopicFrame) => void) => {
    this.frameListeners.add(l);
    return () => void this.frameListeners.delete(l);
  };

  emitFrame(frame: TopicFrame) {
    for (const l of [...this.frameListeners]) l(frame);
  }

  /** clientId of the current socket (hello), sent as X-Admin-Client on mutations (T051). */
  private clientId: string | undefined;
  getClientId = (): string | undefined => this.clientId;
  setClientId(id: string) {
    this.clientId = id;
    this.addOwnOrigin(id);
  }

  addOwnOrigin(id: string | undefined) {
    if (!id || this.ownOrigins.includes(id)) return;
    this.ownOrigins = [id, ...this.ownOrigins].slice(0, 8);
  }

  isOwnOrigin(origin: string | undefined): boolean {
    return origin !== undefined && this.ownOrigins.includes(origin);
  }
  subscribeJob = (jobId: string, l: Listener) => {
    let set = this.jobListeners.get(jobId);
    if (!set) this.jobListeners.set(jobId, (set = new Set()));
    set.add(l);
    return () => {
      set.delete(l);
      if (set.size === 0) this.jobListeners.delete(jobId);
    };
  };

  private add(set: Set<Listener>, l: Listener) {
    set.add(l);
    return () => void set.delete(l);
  }

  // ---- writes --------------------------------------------------------------------------
  setConnectionStatus(status: ConnectionStatus) {
    if (this.connection.status === status) return;
    if (this.connection.status === "stale") return; // a protocol mismatch is final
    this.connection = { ...this.connection, status };
    this.connectionListeners.forEach((l) => l());
  }

  setFresh(fresh: boolean) {
    if (this.fresh === fresh) return;
    this.fresh = fresh;
    this.freshListeners.forEach((l) => l());
  }

  setTopics(topics: Iterable<Topic>) {
    const next = new Set(topics);
    if (next.size === this.topics.size && [...next].every((t) => this.topics.has(t))) return;
    this.topics = next;
    this.topicListeners.forEach((l) => l());
  }

  setSummary(summary: SummaryState | undefined) {
    const prev = this.summary;
    if (prev === summary) return;
    if (prev && summary && prev.appsTotal === summary.appsTotal && prev.appsEnabled === summary.appsEnabled && prev.running === summary.running && prev.queued === summary.queued) return;
    this.summary = summary;
    this.summaryListeners.forEach((l) => l());
  }

  /** The service states of a `subscribed` ack replace the whole map. */
  replaceService(states: Map<string, ServiceState>) {
    this.service = new Map(states);
    this.serviceListeners.forEach((l) => l());
  }

  setService(appId: string, state: ServiceState) {
    if (this.service.get(appId) === state) return;
    this.service = new Map(this.service).set(appId, state);
    this.serviceListeners.forEach((l) => l());
  }

  deleteService(appId: string) {
    if (!this.service.has(appId)) return;
    const next = new Map(this.service);
    next.delete(appId);
    this.service = next;
    this.serviceListeners.forEach((l) => l());
  }

  setHello(hello: { serverVersion?: string; sessionExpiresAt?: string }) {
    if (this.connection.status === "stale") return;
    this.connection = { ...this.connection, serverVersion: hello.serverVersion, sessionExpiresAt: hello.sessionExpiresAt };
    this.connectionListeners.forEach((l) => l());
  }

  dismissNotification(id: number) {
    this.notifications = this.notifications.filter((n) => n.id !== id);
    this.notificationListeners.forEach((l) => l());
  }

  clearNotifications() {
    if (this.notifications.length === 0) return;
    this.notifications = [];
    this.notificationListeners.forEach((l) => l());
  }

  pushNotification(n: Omit<AppNotification, "id">) {
    // Newest first, bounded.
    this.notifications = [{ ...n, id: this.nextId++ }, ...this.notifications].slice(0, MAX_NOTIFICATIONS);
    this.notificationListeners.forEach((l) => l());
  }

  /** Drops progress entries that were silent for PROGRESS_TTL_MS (a lost terminal event). */
  prune(now: number, ttl = PROGRESS_TTL_MS) {
    const dropped: string[] = [];
    for (const [jobId, t] of this.progress) if (now - t.receivedAt > ttl) dropped.push(jobId);
    if (dropped.length === 0) return;
    dropped.forEach((id) => this.progress.delete(id));
    this.publishProgress(dropped);
  }

  /** Applies one parsed frame. `now` is the receive time. */
  apply(frame: Frame, now: number) {
    if (this.connection.status === "stale") return; // protocol mismatch: ignore everything
    this.lastSeen = now;

    switch (frame.kind) {
      case "heartbeat":
        return; // lastSeen only (B51)

      case "hello":
        this.setHello(frame);
        return;

      case "protocol_mismatch": {
        const changed = [...this.progress.keys()];
        this.progress.clear();
        this.connection = { ...this.connection, status: "stale" };
        this.connectionListeners.forEach((l) => l());
        this.setFresh(false);
        this.pushNotification({ kind: "events_stale", level: "warning", ts: now });
        this.publishProgress(changed);
        return;
      }

      case "snapshot": {
        const changed = new Set(this.progress.keys());
        this.progress.clear();
        for (const job of frame.jobs) {
          this.progress.set(job.jobId, { ...job, receivedAt: now });
          changed.add(job.jobId);
        }
        this.publishProgress([...changed]);
        return;
      }

      case "progress": {
        const prev = this.progress.get(frame.job.jobId);
        if (prev && prev.ts > frame.job.ts) return; // out of order
        this.progress.set(frame.job.jobId, { ...frame.job, receivedAt: now });
        this.publishProgress([frame.job.jobId]);
        return;
      }

      case "cancel_requested": {
        const prev = this.progress.get(frame.jobId);
        if (!prev) return;
        const status = frame.status && isTerminalStatus(frame.status) ? frame.status : "cancel_requested";
        if (isTerminalStatus(status)) {
          this.progress.delete(frame.jobId); // a queued job canceled at once: nothing runs any more
          this.publishProgress([frame.jobId]);
          return;
        }
        this.progress.set(frame.jobId, { ...prev, status, ts: Math.max(prev.ts, frame.ts), receivedAt: now });
        this.publishProgress([frame.jobId]);
        return;
      }

      case "job_status": {
        if (isTerminalStatus(frame.status)) {
          const had = this.progress.delete(frame.jobId);
          if (had) this.publishProgress([frame.jobId]);
          this.pushNotification({
            kind: frame.status === "succeeded" ? "job_succeeded" : frame.status === "failed" ? "job_failed" : "job_canceled",
            level: frame.status === "succeeded" ? "success" : frame.status === "failed" ? "error" : "info",
            ts: frame.ts,
            appId: frame.appId,
            jobId: frame.jobId,
            tag: frame.tag,
            code: frame.errorCode,
          });
          this.onTerminal?.(frame);
          return;
        }
        const prev = this.progress.get(frame.jobId);
        if (prev) {
          this.progress.set(frame.jobId, { ...prev, status: frame.status, ts: Math.max(prev.ts, frame.ts), receivedAt: now });
          this.publishProgress([frame.jobId]);
        }
        return;
      }

      case "summary":
        this.setSummary(frame.state);
        return;

      case "service.status":
        this.setService(frame.appId, frame.state);
        return;

      case "notice":
        this.pushNotification({
          kind: "notice",
          level: frame.level === "error" ? "error" : frame.level === "warning" ? "warning" : "info",
          ts: frame.ts,
          code: frame.code,
          message: frame.message,
        });
        return;

      default:
        return; // control frames and query-backed topics are handled by the provider/appliers
    }
  }

  private publishProgress(jobIds: string[]) {
    this.progressList = [...this.progress.values()].map(({ receivedAt: _r, ...job }) => job);
    for (const id of jobIds) this.jobListeners.get(id)?.forEach((l) => l());
    this.listListeners.forEach((l) => l());
  }
}

/**
 * B52 / events-ws.md §5.3: the status to display. The database row wins unless a live
 * event is NEWER than the row (a stale live entry must never override a later DB status).
 */
export function effectiveStatus(dbStatus: JobStatus | undefined, dbUpdatedAt: string | undefined, live: JobProgress | undefined): JobStatus | undefined {
  if (!live) return dbStatus;
  if (!dbStatus) return live.status;
  const dbTime = dbUpdatedAt ? Date.parse(dbUpdatedAt) : NaN;
  if (!Number.isNaN(dbTime) && live.ts <= dbTime) return dbStatus;
  return live.status;
}
