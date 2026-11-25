"use client";

// Owns the single WebSocket of the tab (events-ws.md §5.1) and feeds the EventStore.
// Consumers read the store through useSyncExternalStore hooks with selectors, so the
// provider itself never re-renders its children: a heartbeat changes nothing visible and a
// progress frame re-renders only the components that read that job.
import { useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { PropsWithChildren } from "react";
import { api } from "@/lib/api/client";
import { invalidateAfterJob, invalidateAll } from "@/lib/query";
import { EventStore } from "./store";
import type { AppNotification, ConnectionState } from "./store";
import { EVENTS_PATH, PROTOCOL, parseFrame } from "./types";
import type { JobProgress } from "./types";

export interface SocketLike {
  onopen: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: { code: number }) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  close: (code?: number) => void;
}

export type SocketFactory = (url: string) => SocketLike;

const defaultFactory: SocketFactory = (url) => new WebSocket(url) as unknown as SocketLike;

export const BACKOFF_START_MS = 1_000;
export const BACKOFF_MAX_MS = 30_000;
const PRUNE_EVERY_MS = 30_000;

// Used only when a component is rendered without the provider (isolated tests).
const fallbackStore = new EventStore();
const StoreContext = createContext<EventStore | null>(null);

export function eventsUrl(loc: Pick<Location, "protocol" | "host"> = window.location): string {
  const scheme = loc.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${loc.host}${EVENTS_PATH}?protocol=${PROTOCOL}`;
}

export function AdminEventProvider({ children, socketFactory = defaultFactory }: PropsWithChildren<{ socketFactory?: SocketFactory }>) {
  const queryClient = useQueryClient();
  const [store] = useState(() => new EventStore());
  const factoryRef = useRef(socketFactory);
  factoryRef.current = socketFactory;

  useEffect(() => {
    let disposed = false;
    let socket: SocketLike | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let backoff = BACKOFF_START_MS;
    let connectedBefore = false;

    store.onTerminal = (frame) => invalidateAfterJob(queryClient, frame.appId);

    const scheduleReconnect = () => {
      if (disposed) return;
      retryTimer = setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, BACKOFF_MAX_MS);
    };

    const connect = () => {
      if (disposed) return;
      store.setConnectionStatus("connecting");
      const ws = factoryRef.current(eventsUrl());
      socket = ws;

      ws.onopen = () => {
        if (disposed) return ws.close();
        backoff = BACKOFF_START_MS;
        if (connectedBefore) invalidateAll(queryClient); // events may have been missed
        connectedBefore = true;
      };
      ws.onmessage = (ev) => {
        if (disposed) return;
        const frame = parseFrame(ev.data);
        if (frame) store.apply(frame, Date.now());
      };
      ws.onerror = () => {
        /* the close event follows and drives the reconnect */
      };
      ws.onclose = (ev) => {
        if (disposed || socket !== ws) return;
        socket = null;
        switch (ev.code) {
          case 4401: // session expired/revoked: ask the API; a 401 there redirects to login
            store.setConnectionStatus("unauthorized");
            api
              .getSession()
              .then(() => scheduleReconnect()) // session is still valid: try again
              .catch(() => {});
            return;
          case 4409: // protocol mismatch: banner, no reconnect
            store.apply({ kind: "protocol_mismatch", protocol: "" }, Date.now());
            return;
          case 1008: // origin rejected
            store.setConnectionStatus("forbidden");
            return;
          default:
            store.setConnectionStatus("closed");
            scheduleReconnect();
        }
      };
    };

    connect();
    const pruneTimer = setInterval(() => store.prune(Date.now()), PRUNE_EVERY_MS);

    return () => {
      disposed = true;
      store.onTerminal = undefined;
      clearInterval(pruneTimer);
      if (retryTimer) clearTimeout(retryTimer);
      if (socket) {
        socket.onopen = socket.onmessage = socket.onclose = socket.onerror = null;
        socket.close();
        socket = null;
      }
    };
  }, [store, queryClient]);

  return <StoreContext.Provider value={store}>{children}</StoreContext.Provider>;
}

export function useEventStore(): EventStore {
  return useContext(StoreContext) ?? fallbackStore;
}

export function useConnection(): ConnectionState {
  const store = useEventStore();
  return useSyncExternalStore(store.subscribeConnection, store.getConnection, store.getConnection);
}

/** Live progress of one job; re-renders only when THAT job changes. */
export function useJobProgress(jobId: string | undefined): JobProgress | undefined {
  const store = useEventStore();
  const subscribe = useCallback((l: () => void) => (jobId ? store.subscribeJob(jobId, l) : () => {}), [store, jobId]);
  const get = useCallback(() => (jobId ? store.getJob(jobId) : undefined), [store, jobId]);
  return useSyncExternalStore(subscribe, get, get);
}

/** All live jobs (dashboard "running now"). */
export function useProgressList(): readonly JobProgress[] {
  const store = useEventStore();
  return useSyncExternalStore(store.subscribeProgressList, store.getProgressList, store.getProgressList);
}

export function useNotifications(): readonly AppNotification[] {
  const store = useEventStore();
  return useSyncExternalStore(store.subscribeNotifications, store.getNotifications, store.getNotifications);
}
