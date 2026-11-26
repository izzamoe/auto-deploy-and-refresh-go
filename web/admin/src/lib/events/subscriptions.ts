// SubscriptionManager (data-model.md §SubscriptionManager, subscription.md §2): which topics the
// tab needs, what the server confirmed, and the resume position of every topic.
//
//   - refcount per topic: 0->1 schedules `sub`, 1->0 schedules `unsub` after UNSUB_DELAY_MS
//     (a navigation between two pages using the same topic does not churn);
//   - ops are batched per tick (one `sub` frame lists several topics);
//   - resume (RV-8/RV3-8): `topicSeq[topic]` = every state event of that topic up to this seq
//     was applied. Events are accepted when `seq > topicSeq[topic]` (per topic, not a global
//     lastSeq, because a resume replays events older than the newest seen). A topic that comes
//     back (reconnect, or a page opened again) is subscribed with `resume{epoch, seq}`;
//   - epoch (RV-9): a hello with another epoch replaces the epoch, sets lastSeq = hello.seq and
//     clears topicSeq; the topics are then resynced;
//   - visibility: hidden > 10 s -> `jobs {progress:false}` and `logs:system` dropped; visible ->
//     `jobs {progress:true}` (snapshot) and `logs:system {after: cursor}` again.
import type { Topic } from "./types";

export const UNSUB_DELAY_MS = 1_000;
export const HIDDEN_AFTER_MS = 10_000;

export interface TopicOpts {
  /** jobs only: receive the compact progress frames (default true). */
  progress?: boolean;
  /** logs:system only: journal cursor to continue after. */
  after?: string;
  /** stats only: IANA zone of the stats clock. */
  tz?: string;
}

export interface SubTopic {
  name: Topic;
  progress?: boolean;
  after?: string;
  tz?: string;
}

export type ClientOp =
  | { op: "sub"; ref: string; topics: SubTopic[]; resume?: { epoch: string; seq: number } }
  | { op: "unsub"; ref: string; topics: Topic[] };

export interface ResyncContext {
  reason: string;
  /** true when the resync answers a `sub` of this tab: the ack already carried the state/snapshot. */
  viaSub: boolean;
}

export interface ManagerHooks {
  /** Sends one op on the socket; false when there is no usable socket. */
  send(op: ClientOp): boolean;
  /** The topics whose data events keep current changed (confirmed once, not left; kept across a reconnect). */
  onTopics?(topics: ReadonlySet<Topic>): void;
  /** A topic was left (refcount 1->0 and unsub sent): its cached data is no longer kept current. */
  onLeave?(topic: Topic): void;
  /** State carried by a `subscribed` ack (summary, service). */
  onAckState?(topic: Topic, state: unknown): void;
  /** The topic must be recovered without replay (resync). */
  onResync?(topic: Topic, ctx: ResyncContext): void | Promise<unknown>;
  /** Every requested topic is confirmed and no resync is running (or the opposite). */
  onSettled?(settled: boolean): void;
}

interface Entry {
  count: number;
  opts: TopicOpts;
}

interface Pending {
  topics: Set<Topic>;
  resume: boolean;
  acked: Set<Topic>;
}

export class SubscriptionManager {
  epoch: string | undefined;
  lastSeq = 0;
  /** RV3-8: per-topic dedup / resume position. */
  readonly topicSeq = new Map<Topic, number>();

  private refs = new Map<Topic, Entry>();
  private leftOpts = new Map<Topic, TopicOpts>();
  private unsubTimers = new Map<Topic, ReturnType<typeof setTimeout>>();
  private acked = new Set<Topic>();
  /** Topics confirmed at least once and not left: their cached data is kept current (UI view). */
  private kept: ReadonlySet<Topic> = new Set();
  private pending = new Map<string, Pending>();
  private toSub = new Map<Topic, boolean>(); // topic -> force (re-send even when acked)
  private toUnsub = new Set<Topic>();
  private flushQueued = false;
  private attached = false;
  private nextRef = 1;
  private hidden = false;
  private logsCursor: string | undefined;
  private refused = new Set<Topic>();
  private resyncing = 0;
  private epochResync = new Set<Topic>();
  private settled = false;

  constructor(private hooks: ManagerHooks) {}

  // ---- page side ----------------------------------------------------------------------

  /** A component needs `topic`; returns the release function. */
  acquire(topic: Topic, opts: TopicOpts = {}): () => void {
    const entry = this.refs.get(topic);
    if (entry) {
      entry.count++;
      if (!sameOpts(topic, entry.opts, opts)) {
        if (topic === "logs:system" && entry.opts.after !== opts.after) this.logsCursor = undefined;
        entry.opts = { ...opts };
        this.queueSub(topic, true);
      }
    } else {
      if (topic === "logs:system" && opts.after !== undefined) this.logsCursor = undefined;
      this.refs.set(topic, { count: 1, opts: { ...opts } });
      const timer = this.unsubTimers.get(topic);
      if (timer !== undefined) {
        clearTimeout(timer); // still subscribed on the server: nothing to send...
        this.unsubTimers.delete(topic);
        const before = this.leftOpts.get(topic);
        if (before && !sameOpts(topic, before, opts)) this.queueSub(topic, true); // ...unless the options changed
      } else {
        this.queueSub(topic, false);
      }
    }
    let released = false;
    return () => {
      if (released) return;
      released = true;
      this.release(topic);
    };
  }

  private release(topic: Topic) {
    const entry = this.refs.get(topic);
    if (!entry) return;
    entry.count--;
    if (entry.count > 0) return;
    this.refs.delete(topic);
    this.leftOpts.set(topic, entry.opts);
    this.toSub.delete(topic);
    this.unsubTimers.set(
      topic,
      setTimeout(() => {
        this.unsubTimers.delete(topic);
        if (this.refs.has(topic)) return;
        this.leave(topic);
      }, UNSUB_DELAY_MS),
    );
    this.updateSettled();
  }

  private leave(topic: Topic) {
    this.epochResync.delete(topic);
    if (this.acked.has(topic)) this.markComplete(topic);
    if (this.attached && (this.acked.has(topic) || this.isPending(topic))) {
      this.toUnsub.add(topic);
      this.queueFlush();
    }
    this.setAcked(topic, false);
    this.keep(topic, false);
    this.hooks.onLeave?.(topic);
  }

  /** Topics the page currently needs (refcount > 0). */
  wanted(): Topic[] {
    return [...this.refs.keys()];
  }

  isAcked(topic: Topic): boolean {
    return this.acked.has(topic);
  }

  keptTopics(): ReadonlySet<Topic> {
    return this.kept;
  }

  // ---- socket side --------------------------------------------------------------------

  /** A hello arrived on a new socket: settle the epoch, then subscribe every needed topic. */
  attach(hello: { epoch?: string; seq?: number }) {
    this.attached = true;
    this.pending.clear();
    this.epochResync.clear();
    const seq = hello.seq ?? 0;
    if (hello.epoch && this.epoch !== undefined && hello.epoch !== this.epoch) {
      // RV-9: the server restarted. Old positions mean nothing: resync every topic in use.
      this.epoch = hello.epoch;
      this.lastSeq = seq;
      this.topicSeq.clear();
      for (const t of this.refs.keys()) this.epochResync.add(t);
    } else {
      if (hello.epoch) this.epoch = hello.epoch;
      this.lastSeq = Math.max(this.lastSeq, seq);
    }
    for (const t of this.refs.keys()) this.toSub.set(t, true);
    this.flush();
  }

  /** The socket closed: what was confirmed is complete up to lastSeq (resume point). */
  detach() {
    for (const t of this.acked) this.markComplete(t);
    this.attached = false;
    this.pending.clear();
    this.toSub.clear();
    this.toUnsub.clear();
    this.resyncing = 0;
    this.acked = new Set();
    // Topics waiting for their delayed unsub are gone with the socket.
    for (const [t, timer] of this.unsubTimers) {
      clearTimeout(timer);
      this.keep(t, false);
      this.hooks.onLeave?.(t);
    }
    this.unsubTimers.clear();
    this.updateSettled();
  }

  /** Hidden for more than HIDDEN_AFTER_MS (true) or visible again (false). */
  setHidden(hidden: boolean) {
    if (this.hidden === hidden) return;
    this.hidden = hidden;
    if (!this.attached) return;
    if (this.refs.has("jobs")) this.queueSub("jobs", true);
    if (this.refs.has("logs:system")) {
      if (hidden) {
        if (this.acked.has("logs:system") || this.isPending("logs:system")) this.toUnsub.add("logs:system");
        this.setAcked("logs:system", false);
        this.queueFlush();
      } else {
        this.queueSub("logs:system", true);
      }
    }
  }

  isHidden(): boolean {
    return this.hidden;
  }

  /** The newest journal cursor seen (logs.lines), used for `sub logs:system {after}`. */
  noteLogsCursor(cursor: string | undefined) {
    if (cursor) this.logsCursor = cursor;
  }

  /** Per-topic dedup (RV3-8): true when the event must be applied. */
  accept(topic: Topic | undefined, seq: number | undefined): boolean {
    if (seq === undefined || topic === undefined) return true; // stream frames and control frames
    const last = this.topicSeq.get(topic) ?? 0;
    if (seq <= last) return false;
    this.topicSeq.set(topic, seq);
    if (seq > this.lastSeq) this.lastSeq = seq;
    return true;
  }

  onSubscribed(ref: string | undefined, topics: { name: Topic; state?: unknown }[]) {
    const p = ref ? this.pending.get(ref) : undefined;
    for (const { name, state } of topics) {
      if (!this.refs.has(name) && !this.unsubTimers.has(name)) continue; // left meanwhile; the unsub follows
      if (state !== undefined && state !== null) this.hooks.onAckState?.(name, state);
      if (!p || !p.resume) this.markComplete(name);
      this.setAcked(name, true);
      this.keep(name, true);
      if (p) p.acked.add(name);
    }
    if (p && !p.resume) this.pending.delete(ref!);
    if (!p) this.resolveFreshPending();
    for (const { name } of topics) {
      if (this.epochResync.delete(name)) this.runResync(name, { reason: "epoch", viaSub: true });
    }
    this.updateSettled();
  }

  onUnsubscribed(topics: Topic[]) {
    for (const t of topics) if (!this.refs.has(t)) this.setAcked(t, false);
    this.updateSettled();
  }

  onResumed(ref: string | undefined, toSeq: number) {
    const refs = ref && this.pending.has(ref) ? [ref] : [...this.pending.keys()].filter((r) => this.pending.get(r)!.resume);
    for (const r of refs) {
      const p = this.pending.get(r)!;
      for (const t of p.topics) {
        if ((this.topicSeq.get(t) ?? 0) < toSeq) this.topicSeq.set(t, toSeq);
      }
      this.pending.delete(r);
    }
    if (toSeq > this.lastSeq) this.lastSeq = toSeq;
    this.updateSettled();
  }

  onResync(ref: string | undefined, topics: Topic[], reason: string, helloSeq?: number) {
    const viaSub = ref !== undefined && this.pending.has(ref);
    if (reason === "epoch") {
      this.topicSeq.clear();
      if (helloSeq !== undefined) this.lastSeq = helloSeq;
    }
    if (ref) this.pending.delete(ref);
    for (const t of topics) {
      if (!this.refs.has(t)) continue;
      this.markComplete(t);
      if (!viaSub && (t === "summary" || t === "service" || t === "jobs")) {
        // Overflow: the state comes back with a fresh ack (summary, service) or snapshot (jobs).
        this.queueSub(t, true);
      }
      this.runResync(t, { reason, viaSub });
    }
    this.updateSettled();
  }

  onError(ref: string | undefined, topic: string | undefined, code: string) {
    if (topic && (code === "unknown_topic" || code === "forbidden_topic")) {
      this.refused.add(topic as Topic);
      this.keep(topic as Topic, false);
    }
    const p = ref ? this.pending.get(ref) : undefined;
    if (!p) return;
    if (topic) p.topics.delete(topic as Topic);
    // A refused op (rate limit, bad op) or a sub whose topics were all refused gets no ack.
    if (!topic || [...p.topics].every((t) => p.acked.has(t) || this.refused.has(t))) this.pending.delete(ref!);
    this.updateSettled();
  }

  // ---- internals ----------------------------------------------------------------------

  private runResync(topic: Topic, ctx: ResyncContext) {
    const r = this.hooks.onResync?.(topic, ctx);
    if (r && typeof (r as Promise<unknown>).then === "function") {
      this.resyncing++;
      this.updateSettled();
      void (r as Promise<unknown>).catch(() => {}).finally(() => {
        this.resyncing = Math.max(0, this.resyncing - 1);
        this.updateSettled();
      });
    }
  }

  isResyncing(): boolean {
    return this.resyncing > 0;
  }

  private markComplete(topic: Topic) {
    const known = this.topicSeq.get(topic);
    if (known === undefined || known < this.lastSeq) this.topicSeq.set(topic, this.lastSeq);
  }

  private isPending(topic: Topic): boolean {
    for (const p of this.pending.values()) if (p.topics.has(topic)) return true;
    return false;
  }

  private resolveFreshPending() {
    for (const [r, p] of this.pending) {
      if (!p.resume && [...p.topics].every((t) => this.acked.has(t) || !this.refs.has(t))) this.pending.delete(r);
    }
  }

  private setAcked(topic: Topic, on: boolean) {
    if (on) this.acked.add(topic);
    else this.acked.delete(topic);
  }

  private keep(topic: Topic, on: boolean) {
    if (this.kept.has(topic) === on) return;
    const next = new Set(this.kept);
    if (on) next.add(topic);
    else next.delete(topic);
    this.kept = next;
    this.hooks.onTopics?.(next);
  }

  private queueSub(topic: Topic, force: boolean) {
    if (this.refused.has(topic)) return;
    this.toUnsub.delete(topic);
    this.toSub.set(topic, force || this.toSub.get(topic) === true);
    this.queueFlush();
  }

  private queueFlush() {
    if (this.flushQueued) return;
    this.flushQueued = true;
    queueMicrotask(() => {
      this.flushQueued = false;
      this.flush();
    });
  }

  private optsOf(topic: Topic): SubTopic {
    const opts = this.refs.get(topic)?.opts ?? {};
    const st: SubTopic = { name: topic };
    if (topic === "jobs") st.progress = this.hidden ? false : opts.progress !== false;
    if (topic === "logs:system") {
      const after = this.logsCursor ?? opts.after;
      if (after) st.after = after;
    }
    if (topic === "stats" && opts.tz) st.tz = opts.tz;
    return st;
  }

  private flush() {
    if (!this.attached) return; // attach() sends everything once the hello arrived
    if (this.toUnsub.size > 0) {
      const topics = [...this.toUnsub].filter((t) => !this.refs.has(t) || (t === "logs:system" && this.hidden));
      this.toUnsub.clear();
      if (topics.length > 0) this.hooks.send({ op: "unsub", ref: this.ref(), topics });
    }
    const fresh: Topic[] = [];
    const resumable: Topic[] = [];
    for (const [t, force] of this.toSub) {
      if (!this.refs.has(t) || this.refused.has(t)) continue;
      if (t === "logs:system" && this.hidden) continue;
      if (this.acked.has(t) && !force) continue;
      if (this.acked.has(t) || this.isPending(t)) {
        fresh.push(t); // option change or recovery of a confirmed topic: no replay wanted
        continue;
      }
      // logs:system is a stream without seq: it continues after its journal cursor instead.
      if (t !== "logs:system" && this.epoch !== undefined && this.topicSeq.has(t)) resumable.push(t);
      else fresh.push(t);
    }
    this.toSub.clear();
    if (resumable.length > 0) {
      const seq = Math.min(...resumable.map((t) => this.topicSeq.get(t)!));
      this.sendSub(resumable, { epoch: this.epoch!, seq });
    }
    if (fresh.length > 0) this.sendSub(fresh);
    this.updateSettled();
  }

  private sendSub(topics: Topic[], resume?: { epoch: string; seq: number }) {
    const ref = this.ref();
    const op: ClientOp = resume ? { op: "sub", ref, topics: topics.map((t) => this.optsOf(t)), resume } : { op: "sub", ref, topics: topics.map((t) => this.optsOf(t)) };
    if (!this.hooks.send(op)) return;
    this.pending.set(ref, { topics: new Set(topics), resume: resume !== undefined, acked: new Set() });
  }

  private ref(): string {
    return `r${this.nextRef++}`;
  }

  private updateSettled() {
    const settled = this.attached && this.pending.size === 0 && this.resyncing === 0 && this.epochResync.size === 0;
    if (settled === this.settled) return;
    this.settled = settled;
    this.hooks.onSettled?.(settled);
  }

  /** Test/diagnostic view. */
  debug() {
    return { acked: [...this.acked], pending: [...this.pending.keys()], refs: Object.fromEntries([...this.refs].map(([t, e]) => [t, e.count])) };
  }
}

function sameOpts(topic: Topic, a: TopicOpts, b: TopicOpts): boolean {
  switch (topic) {
    case "jobs":
      return (a.progress !== false) === (b.progress !== false);
    case "logs:system":
      return a.after === b.after;
    case "stats":
      return (a.tz ?? "") === (b.tz ?? "");
    default:
      return true;
  }
}
