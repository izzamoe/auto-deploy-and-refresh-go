"use client";

// Owns the single WebSocket of the tab (protocol 3, specs/002 contracts/events-ws.v3.md) and
// feeds the EventStore. Pages say which topics they need with `useTopic`; the
// SubscriptionManager turns that into sub/unsub ops, resumes after a reconnect and resyncs
// only the topics the server cannot replay. A reconnect never refetches everything (FR-011).
// Consumers read the store through useSyncExternalStore hooks with selectors, so the provider
// itself never re-renders its children.
import { useQueryClient } from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { PropsWithChildren } from "react";
import { api, setAdminClientId, tabClientId } from "@/lib/api/client";
import { createAppliers } from "./appliers";
import { EventStore, FALLBACK_AFTER_MS } from "./store";
import type { AppNotification, ConnectionState, ConnectionStatus } from "./store";
import { HIDDEN_AFTER_MS, SubscriptionManager } from "./subscriptions";
import type { ClientOp, ResyncContext, TopicOpts } from "./subscriptions";
import { EVENTS_PATH, PROTOCOL, parseFrame, parseServiceState, parseSummaryState } from "./types";
import type { JobProgress, ServiceState, SummaryState, Topic, TopicFrame } from "./types";

export interface SocketLike {
  onopen: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: { code: number }) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  close: (code?: number) => void;
  /** Protocol 3: the client sends sub/unsub ops (events-ws.v3.md §4). */
  send: (data: string) => void;
}

export type SocketFactory = (url: string) => SocketLike;

const defaultFactory: SocketFactory = (url) => new WebSocket(url) as unknown as SocketLike;

export const BACKOFF_START_MS = 1_000;
export const BACKOFF_MAX_MS = 30_000;
/** Close 4429 (op abuse): wait at least this long before reconnecting (events-ws.v3.md §6). */
export const ABUSE_BACKOFF_MS = 30_000;
const PRUNE_EVERY_MS = 30_000;

/** Query keys whose data a topic keeps current (subscription.md §4). */
export const TOPIC_QUERY_KEYS: Partial<Record<Topic, readonly (readonly unknown[])[]>> = {
  jobs: [["history"], ["apps"]],
  apps: [["apps"], ["app-env"], ["app-args"]],
  stats: [["stats"]],
  settings: [["github"], ["telegram"]],
  account: [["tokens"]],
};

/** RV-8: a page left the topic; its cached data is stale now but is NOT refetched. */
export function leaveTopic(client: QueryClient, topic: Topic) {
  for (const queryKey of TOPIC_QUERY_KEYS[topic] ?? []) void client.invalidateQueries({ queryKey, refetchType: "none" });
}

/** Resync (subscription.md §4 "Saat resync"): ONE targeted refetch of the active queries of the topic. */
export function resyncTopic(client: QueryClient, store: EventStore, topic: Topic, ctx: ResyncContext): Promise<unknown> | undefined {
  switch (topic) {
    case "summary":
    case "service":
      return undefined; // the state comes with the subscribed ack (0 REST)
    case "jobs":
      return client.invalidateQueries({ queryKey: ["history"], refetchType: "active" }); // + snapshot (0 REST)
    case "apps":
      return client.invalidateQueries({ queryKey: ["apps"], refetchType: "active" });
    case "stats":
      return client.invalidateQueries({ queryKey: ["stats"], refetchType: "active" });
    case "settings":
      return Promise.all([client.invalidateQueries({ queryKey: ["github"], refetchType: "active" }), client.invalidateQueries({ queryKey: ["telegram"], refetchType: "active" })]);
    case "account":
      // K-1 (subscription.md §4): a server restart (epoch) does not change the profile; only tokens.
      if (ctx.reason === "epoch") return client.invalidateQueries({ queryKey: ["tokens"], refetchType: "active" });
      return Promise.all([client.invalidateQueries({ queryKey: ["tokens"], refetchType: "active" }), client.invalidateQueries({ queryKey: ["session"], refetchType: "active" })]);
    case "logs:system":
      // The page reloads its REST snapshot (1 REST) and continues after the new cursor.
      store.emitFrame({ kind: "logs.gap", topic: "logs:system", dropped: 0, reason: ctx.reason === "epoch" ? "epoch" : "resync" });
      return undefined;
  }
}

/** Topic events whose data lives in TanStack Query: what to do with one (default: lib/events/appliers, T051). */
export type FrameApplier = (frame: TopicFrame, ctx: { own: boolean }) => void;

interface EventsContext {
  store: EventStore;
  manager: SubscriptionManager;
}

// Used only when a component is rendered without the provider (isolated tests).
const fallback: EventsContext = { store: new EventStore(), manager: new SubscriptionManager({ send: () => false }) };
const Ctx = createContext<EventsContext | null>(null);

export function eventsUrl(loc: Pick<Location, "protocol" | "host"> = window.location): string {
  const scheme = loc.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${loc.host}${EVENTS_PATH}?protocol=${PROTOCOL}`;
}

interface ProviderProps {
  socketFactory?: SocketFactory;
  /** Applies topic events to cached queries; default `createAppliers` (T051). */
  applyFrame?: FrameApplier;
}

export function AdminEventProvider({ children, socketFactory = defaultFactory, applyFrame }: PropsWithChildren<ProviderProps>) {
  const queryClient = useQueryClient();
  const factoryRef = useRef(socketFactory);
  factoryRef.current = socketFactory;
  const applyRef = useRef(applyFrame);
  applyRef.current = applyFrame;
  const socketRef = useRef<{ ws: SocketLike; hello: boolean } | null>(null);

  const settledRef = useRef<() => void>(() => {});
  // Created before any child effect runs (children subscribe topics in their effects).
  const [ctx] = useState<EventsContext>(() => {
    const store = new EventStore();
    const manager = new SubscriptionManager({
      send: (op: ClientOp) => {
        const s = socketRef.current;
        if (!s || !s.hello) return false;
        s.ws.send(JSON.stringify(op));
        return true;
      },
      onTopics: (topics) => store.setTopics(topics),
      onAckState: (topic, state) => {
        if (topic === "summary") {
          const summary = parseSummaryState(state);
          if (summary) store.setSummary(summary);
        } else if (topic === "service") {
          const service = parseServiceState(state);
          if (service) store.replaceService(service);
        }
      },
      onLeave: (topic) => leaveTopic(queryClient, topic),
      onResync: (topic, rctx) => resyncTopic(queryClient, store, topic, rctx),
      onSettled: () => settledRef.current(),
    });
    return { store, manager };
  });

  useEffect(() => {
    const { store, manager } = ctx;
    let disposed = false;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let fallbackTimer: ReturnType<typeof setTimeout> | undefined;
    let hiddenTimer: ReturnType<typeof setTimeout> | undefined;
    let backoff = BACKOFF_START_MS;
    let connectedBefore = false;
    let helloSeq: number | undefined;
    const isOffline = () => typeof navigator !== "undefined" && navigator.onLine === false;

    const appliers = createAppliers(queryClient);
    const applyTopicFrame: FrameApplier = (frame, c) => (applyRef.current ?? appliers.apply)(frame, c);
    store.addOwnOrigin(tabClientId()); // mutations sent before the first hello carry the tab id

    // Q-4=A: the cached data is trusted while live, and for FALLBACK_AFTER_MS after leaving live.
    const setStatus = (status: ConnectionStatus) => {
      store.setConnectionStatus(status);
      if (store.getConnection().status === "live") {
        if (fallbackTimer) clearTimeout(fallbackTimer);
        fallbackTimer = undefined;
        store.setFresh(true);
        store.setFallback(false);
      } else if (!fallbackTimer && !store.isFallback()) {
        // Not live (yet, or any more): after FALLBACK_AFTER_MS the page stops trusting events.
        fallbackTimer = setTimeout(() => {
          fallbackTimer = undefined;
          if (store.getConnection().status === "live") return;
          store.setFresh(false);
          store.setFallback(true);
        }, FALLBACK_AFTER_MS);
      }
    };

    settledRef.current = () => {
      const s = socketRef.current;
      if (disposed || !s || !s.hello) return;
      if (isOffline()) return setStatus("offline");
      const settled = manager.debug().pending.length === 0 && !manager.isResyncing();
      const current = store.getConnection().status;
      setStatus(settled ? "live" : manager.isResyncing() ? "resyncing" : current === "connecting" || !connectedBefore ? "connecting" : "reconnecting");
    };

    const scheduleReconnect = (delay = backoff) => {
      if (disposed || retryTimer) return;
      if (isOffline()) return setStatus("offline"); // wait for the `online` event (subscription.md §2.5)
      retryTimer = setTimeout(() => {
        retryTimer = undefined;
        connect();
      }, delay);
      backoff = Math.min(backoff * 2, BACKOFF_MAX_MS);
    };

    const onFrame = (raw: unknown) => {
      const frame = parseFrame(raw);
      if (!frame) return;
      const now = Date.now();
      switch (frame.kind) {
        case "hello": {
          const s = socketRef.current;
          if (s) s.hello = true;
          helloSeq = frame.seq;
          store.apply(frame, now);
          if (frame.clientId) {
            store.setClientId(frame.clientId);
            setAdminClientId(frame.clientId); // X-Admin-Client of every later mutation (T051)
          }
          manager.attach(frame);
          settledRef.current();
          return;
        }
        case "subscribed":
          store.lastSeen = now;
          manager.onSubscribed(frame.ref, frame.topics);
          return;
        case "unsubscribed":
          store.lastSeen = now;
          manager.onUnsubscribed(frame.topics);
          return;
        case "resumed":
          store.lastSeen = now;
          manager.onResumed(frame.ref, frame.toSeq);
          return;
        case "resync":
          store.lastSeen = now;
          manager.onResync(frame.ref, frame.topics, frame.reason, helloSeq);
          return;
        case "error":
          store.lastSeen = now;
          manager.onError(frame.ref, frame.topic, frame.code);
          return;
        case "heartbeat":
        case "protocol_mismatch":
        case "progress":
          store.apply(frame, now);
          return;
        default: {
          if (!manager.accept(frame.topic, frame.seq)) return; // duplicate of a replay (RV3-8)
          if (frame.kind === "logs.lines") manager.noteLogsCursor(frame.cursor);
          store.apply(frame, now);
          applyTopicFrame(frame, { own: store.isOwnOrigin(frame.origin) });
          store.emitFrame(frame);
        }
      }
    };

    const connect = () => {
      if (disposed) return;
      if (isOffline()) return setStatus("offline");
      setStatus(connectedBefore && store.getConnection().status !== "connecting" ? "reconnecting" : "connecting");
      const ws = factoryRef.current(eventsUrl());
      const entry = { ws, hello: false };
      socketRef.current = entry;

      ws.onopen = () => {
        if (disposed) return ws.close();
        backoff = BACKOFF_START_MS;
        connectedBefore = true; // the hello that follows resumes (no refetch, FR-011)
      };
      ws.onmessage = (ev) => {
        if (disposed || socketRef.current !== entry) return;
        onFrame(ev.data);
      };
      ws.onerror = () => {
        /* the close event follows and drives the reconnect */
      };
      ws.onclose = (ev) => {
        if (disposed || socketRef.current !== entry) return;
        socketRef.current = null;
        manager.detach();
        switch (ev.code) {
          case 4401: // session expired/revoked: ask the API; a 401 there redirects to login
            setStatus("unauthorized");
            api
              .getSession()
              .then(() => {
                // RV3-14: unauthorized -> connecting -> live, resuming every topic.
                if (disposed) return;
                setStatus("connecting");
                backoff = BACKOFF_START_MS;
                scheduleReconnect();
              })
              .catch(() => {});
            return;
          case 4409: // protocol mismatch: banner, no reconnect
            store.apply({ kind: "protocol_mismatch", protocol: "" }, Date.now());
            return;
          case 1008: // origin rejected
            setStatus("forbidden");
            return;
          case 4429: // op abuse: back off for at least 30 s
            setStatus("reconnecting");
            scheduleReconnect(Math.max(backoff, ABUSE_BACKOFF_MS));
            return;
          default:
            setStatus(isOffline() ? "offline" : "reconnecting");
            scheduleReconnect();
        }
      };
    };

    const onOffline = () => {
      if (store.getConnection().status === "stale") return;
      setStatus("offline");
    };
    const onOnline = () => {
      if (disposed) return;
      if (socketRef.current) {
        settledRef.current(); // the socket survived: back to its real state
        return;
      }
      if (retryTimer) clearTimeout(retryTimer);
      retryTimer = undefined;
      backoff = BACKOFF_START_MS;
      const st = store.getConnection().status;
      if (st === "stale" || st === "forbidden" || st === "unauthorized") return;
      connect();
    };
    const onVisibility = () => {
      if (document.visibilityState === "hidden") {
        if (!hiddenTimer) hiddenTimer = setTimeout(() => manager.setHidden(true), HIDDEN_AFTER_MS);
      } else {
        if (hiddenTimer) clearTimeout(hiddenTimer);
        hiddenTimer = undefined;
        manager.setHidden(false);
      }
    };
    window.addEventListener("offline", onOffline);
    window.addEventListener("online", onOnline);
    document.addEventListener("visibilitychange", onVisibility);

    connect();
    const pruneTimer = setInterval(() => store.prune(Date.now()), PRUNE_EVERY_MS);

    return () => {
      disposed = true;
      appliers.dispose();
      settledRef.current = () => {};
      window.removeEventListener("offline", onOffline);
      window.removeEventListener("online", onOnline);
      document.removeEventListener("visibilitychange", onVisibility);
      clearInterval(pruneTimer);
      if (retryTimer) clearTimeout(retryTimer);
      if (fallbackTimer) clearTimeout(fallbackTimer);
      if (hiddenTimer) clearTimeout(hiddenTimer);
      const s = socketRef.current;
      socketRef.current = null;
      if (s) {
        s.ws.onopen = s.ws.onmessage = s.ws.onclose = s.ws.onerror = null;
        s.ws.close();
      }
      manager.detach();
    };
  }, [ctx, queryClient]);

  return <Ctx.Provider value={ctx}>{children}</Ctx.Provider>;
}

function useEvents(): EventsContext {
  return useContext(Ctx) ?? fallback;
}

export function useEventStore(): EventStore {
  return useEvents().store;
}

export function useSubscriptionManager(): SubscriptionManager {
  return useEvents().manager;
}

/**
 * The page needs `topic` while mounted (subscription.md §2). Returns true while the topic's
 * cached data is kept current by events (confirmed by the server and the socket is live, or
 * left live less than 5 s ago): queries may then use `liveQueryOptions` (T052).
 */
export function useTopic(topic: Topic, opts?: TopicOpts, enabled = true): boolean {
  const { store, manager } = useEvents();
  const key = JSON.stringify(opts ?? {});
  useEffect(() => (enabled ? manager.acquire(topic, opts ?? {}) : undefined), [manager, topic, key, enabled]); // eslint-disable-line react-hooks/exhaustive-deps
  const subscribe = useCallback(
    (l: () => void) => {
      const a = store.subscribeFresh(l);
      const b = store.subscribeTopics(l);
      return () => {
        a();
        b();
      };
    },
    [store],
  );
  const get = useCallback(() => store.isFresh() && store.getTopics().has(topic), [store, topic]);
  return useSyncExternalStore(subscribe, get, get);
}

/** Whether the server confirmed `topic` once (and it was not left since). */
export function useTopicConfirmed(topic: Topic): boolean {
  const store = useEventStore();
  const get = useCallback(() => store.getTopics().has(topic), [store, topic]);
  return useSyncExternalStore(store.subscribeTopics, get, get);
}

/** Events keep the cache current (live, or not live for less than 5 s). */
export function useEventsFresh(): boolean {
  const store = useEventStore();
  return useSyncExternalStore(store.subscribeFresh, store.isFresh, store.isFresh);
}

/** The socket has not been live for more than 5 s (from page load, or since it dropped): Q-4 fallback. */
export function useEventsFallback(): boolean {
  const store = useEventStore();
  return useSyncExternalStore(store.subscribeFresh, store.isFallback, store.isFallback);
}

export function useConnection(): ConnectionState {
  const store = useEventStore();
  return useSyncExternalStore(store.subscribeConnection, store.getConnection, store.getConnection);
}

/** Reading live progress needs topic `jobs` with progress frames (subscription.md §1). */
const JOBS_WITH_PROGRESS: TopicOpts = { progress: true };

/** Live progress of one job; re-renders only when THAT job changes. Subscribes topic `jobs`. */
export function useJobProgress(jobId: string | undefined): JobProgress | undefined {
  const store = useEventStore();
  useTopic("jobs", JOBS_WITH_PROGRESS);
  const subscribe = useCallback((l: () => void) => (jobId ? store.subscribeJob(jobId, l) : () => {}), [store, jobId]);
  const get = useCallback(() => (jobId ? store.getJob(jobId) : undefined), [store, jobId]);
  return useSyncExternalStore(subscribe, get, get);
}

/** All live jobs (dashboard "running now"). Subscribes topic `jobs`. */
export function useProgressList(): readonly JobProgress[] {
  const store = useEventStore();
  useTopic("jobs", JOBS_WITH_PROGRESS);
  return useSyncExternalStore(store.subscribeProgressList, store.getProgressList, store.getProgressList);
}

export function useNotifications(): readonly AppNotification[] {
  const store = useEventStore();
  return useSyncExternalStore(store.subscribeNotifications, store.getNotifications, store.getNotifications);
}

/** State of topic `summary` (Header), undefined until the first ack. */
export function useSummary(): SummaryState | undefined {
  const store = useEventStore();
  return useSyncExternalStore(store.subscribeSummary, store.getSummary, store.getSummary);
}

/** State of the app's service from topic `service` (undefined = not known yet). */
export function useServiceState(appId: string): ServiceState | undefined {
  const store = useEventStore();
  const get = useCallback(() => store.getService(appId), [store, appId]);
  return useSyncExternalStore(store.subscribeService, get, get);
}

/** Calls `listener` for every accepted topic event while mounted. */
export function useTopicFrames(listener: (frame: TopicFrame) => void) {
  const store = useEventStore();
  const ref = useRef(listener);
  ref.current = listener;
  useEffect(() => store.subscribeFrames((f) => ref.current(f)), [store]);
}
