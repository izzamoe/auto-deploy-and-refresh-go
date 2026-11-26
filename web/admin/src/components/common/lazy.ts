"use client";

// On-demand modules (002 T070, FR-027): UI that only appears after an interaction (dialogs,
// menus) is loaded with `import()` on intent (pointerenter/focus) and shown on click, so it
// never weighs on the initial JS of a route.
//
// RV-22: a tab that stayed open while a new binary was installed asks for chunk hashes that
// no longer exist (404 -> ChunkLoadError). A failed load never throws into React and never
// leaves the UI waiting: it resolves to null, the caller closes what it was opening, and the
// shell shows the existing "this admin page is out of date, reload" banner (`eventsStale`).
import { useEffect, useState, useSyncExternalStore } from "react";

let chunkFailed = false;
const listeners = new Set<() => void>();

/** A dynamic import failed (old tab after an update, or the network): ask for a reload. */
export function reportChunkError(err?: unknown) {
  if (typeof console !== "undefined") console.warn("admin: a lazily loaded part could not be loaded", err);
  if (chunkFailed) return;
  chunkFailed = true;
  listeners.forEach((l) => l());
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => void listeners.delete(l);
}
const getFailed = () => chunkFailed;
const getServer = () => false;

/** True once any lazily loaded part failed to load (the page is out of date). */
export function useChunkFailed(): boolean {
  return useSyncExternalStore(subscribe, getFailed, getServer);
}

/** Tests only: forget a reported failure. */
export function resetChunkErrorForTests() {
  chunkFailed = false;
  listeners.forEach((l) => l());
}

export interface LazyModule<M> {
  /** The module when it is loaded already, else null. */
  current(): M | null;
  /** Loads once (shared promise); resolves null when loading failed (reported, retried next time). */
  load(): Promise<M | null>;
}

export function lazyModule<M>(importer: () => Promise<M>): LazyModule<M> {
  let mod: M | null = null;
  let pending: Promise<M | null> | null = null;
  return {
    current: () => mod,
    load() {
      if (mod) return Promise.resolve(mod);
      pending ??= importer().then(
        (m) => (mod = m),
        (err: unknown) => {
          pending = null;
          reportChunkError(err);
          return null;
        },
      );
      return pending;
    },
  };
}

/**
 * The module once `want` was true (it stays loaded afterwards, so closing animations and focus
 * return still run). `onFail` is called when loading failed, e.g. to close the dialog again.
 */
export function useLazyModule<M>(lazy: LazyModule<M>, want: boolean, onFail?: () => void): M | null {
  const [mod, setMod] = useState<M | null>(() => lazy.current());
  useEffect(() => {
    if (!want || mod) return;
    let active = true;
    void lazy.load().then((m) => {
      if (!active) return;
      if (m) setMod(() => m);
      else onFail?.();
    });
    return () => {
      active = false;
    };
  }, [want, mod, lazy]); // eslint-disable-line react-hooks/exhaustive-deps
  return mod;
}

/**
 * For `next/dynamic` loaders: `dynamic(() => import("./X").then((m) => m.X).catch(renderNothing))`.
 * A failed chunk is reported and the part renders nothing instead of crashing the page.
 */
export function renderNothing(err: unknown): () => null {
  reportChunkError(err);
  return () => null;
}
