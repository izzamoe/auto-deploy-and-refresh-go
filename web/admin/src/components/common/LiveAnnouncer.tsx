"use client";

// One polite live region for changes pushed by other sessions (002 FR-022): "App X was disabled
// in another session". Short text only, never moves focus, at most one message per
// ANNOUNCE_INTERVAL_MS (a burst keeps only the newest pending message, so a screen reader is
// never flooded). Rendered once, in the Header; anything calls `announce(text)`.
import { useEffect, useState } from "react";

export const ANNOUNCE_INTERVAL_MS = 2_000;

type Listener = (message: string) => void;
const listeners = new Set<Listener>();

/** Queues a polite announcement (no-op when no announcer is mounted). */
export function announce(message: string) {
  if (!message) return;
  for (const l of [...listeners]) l(message);
}

export function LiveAnnouncer() {
  const [text, setText] = useState("");

  useEffect(() => {
    let last = Number.NEGATIVE_INFINITY;
    let pending: string | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const show = (message: string) => {
      last = Date.now();
      setText(message);
    };
    const flush = () => {
      timer = undefined;
      if (pending === null) return;
      const message = pending;
      pending = null;
      show(message);
    };
    const listener: Listener = (message) => {
      const wait = last + ANNOUNCE_INTERVAL_MS - Date.now();
      if (wait <= 0 && !timer) return show(message);
      pending = message; // newest wins
      if (!timer) timer = setTimeout(flush, Math.max(0, wait));
    };
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
      if (timer) clearTimeout(timer);
    };
  }, []);

  return (
    <div aria-live="polite" aria-atomic="true" className="sr-only" data-testid="live-announcer">
      {text}
    </div>
  );
}
