// Test helpers for the shell (not part of the app): providers, a routed fetch mock and a
// fake WebSocket so nothing touches the network.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ThemeProvider } from "next-themes";
import type { ReactNode } from "react";
import { vi } from "vitest";
import { I18nProvider } from "@/i18n";

export function Providers({ children, client }: { children: ReactNode; client?: QueryClient }) {
  const qc = client ?? new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 0 } } });
  return (
    <ThemeProvider attribute="class" defaultTheme="light" enableSystem={false}>
      <I18nProvider>
        <QueryClientProvider client={qc}>{children}</QueryClientProvider>
      </I18nProvider>
    </ThemeProvider>
  );
}

export type Reply = { status: number; body?: unknown; headers?: Record<string, string> };
export type Handler = (init: RequestInit & { url: string }) => Reply | Promise<Reply>;

export interface FetchCall {
  method: string;
  path: string;
  body?: unknown;
}

/** Routes "METHOD /admin/api/v1/…" (query string stripped) to handlers; unknown -> 404 JSON. */
export function installFetch(routes: Record<string, Handler>) {
  const calls: FetchCall[] = [];
  const mock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const path = url.split("?")[0];
    const method = (init.method ?? "GET").toUpperCase();
    calls.push({ method, path, body: typeof init.body === "string" ? JSON.parse(init.body) : undefined });
    const handler = routes[`${method} ${path}`];
    const reply: Reply = handler
      ? await handler({ ...init, url })
      : { status: 404, body: { status: "error", code: "not_found", error: "not found", errors: [] } };
    return {
      ok: reply.status >= 200 && reply.status < 300,
      status: reply.status,
      headers: new Headers(reply.headers),
      json: async () => {
        if (reply.body === undefined) throw new SyntaxError("no body");
        return reply.body;
      },
    } as Response;
  });
  vi.stubGlobal("fetch", mock);
  return { calls, mock, count: (method: string, path: string) => calls.filter((c) => c.method === method && c.path === path).length };
}

export const errorBody = (code: string) => ({ status: "error", code, error: "english server text", errors: [] });

export const STATS = {
  generatedAt: "2026-11-25T00:00:00Z",
  range: "7d",
  tz: "UTC",
  apps: { total: 5, enabled: 3 },
  deploys24h: { total: 4, succeeded: 3, failed: 1, canceled: 0 },
  active: { running: 2, queued: 1 },
  successRate: { succeeded: 3, total: 4, rate: 0.75, previousRate: null },
  daily: [],
  rollbacks: { count: 0 },
  recent: [],
};

// Protocol 3 (002 T050, RV-2): `send` records the ops of the page, `emit` pushes a server frame.
export class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  onopen: ((e: unknown) => void) | null = null;
  onmessage: ((e: { data: unknown }) => void) | null = null;
  onclose: ((e: { code: number }) => void) | null = null;
  onerror: ((e: unknown) => void) | null = null;
  close = vi.fn();
  send = vi.fn();
  constructor(public url: string) {
    FakeWebSocket.instances.push(this);
  }
  emit(frame: object | string) {
    this.onmessage?.({ data: typeof frame === "string" ? frame : JSON.stringify(frame) });
  }
  /** The sub/unsub ops sent by the page, parsed. */
  ops(): { op: string; ref: string; topics: (string | { name: string })[]; resume?: { epoch: string; seq: number } }[] {
    return this.send.mock.calls.map((c) => JSON.parse(String(c[0])));
  }
  /** Topic names of every sub op sent so far. */
  subscribedTopics(): string[] {
    return this.ops().flatMap((o) => (o.op === "sub" ? o.topics.map((t) => (typeof t === "string" ? t : t.name)) : []));
  }
}

/** A protocol-3 server frame. */
export const v3 = (type: string, extra: Record<string, unknown> = {}) => ({ protocol: "3", id: `evt-${type}`, type, ts: new Date().toISOString(), ...extra });

export const hello = (extra: Record<string, unknown> = {}) =>
  v3("hello", { payload: { serverVersion: "v1", heartbeatSeconds: 30, epoch: "e-test", seq: 0, clientId: "c-0000test", limits: { maxTopics: 16, maxOpsPer10s: 20, maxFrameBytes: 4096 }, ...extra } });

/**
 * Answers every sub op of the page not answered yet with `subscribed` (carrying `states[topic]`),
 * plus `resumed` when the op asked to resume. Call inside `act`.
 */
export function ackSubs(ws: FakeWebSocket, states: Record<string, unknown> = {}) {
  const answered = ((ws as unknown as { answered?: Set<string> }).answered ??= new Set<string>());
  for (const op of ws.ops()) {
    if (op.op !== "sub" || answered.has(op.ref)) continue;
    answered.add(op.ref);
    const topics = op.topics.map((t) => (typeof t === "string" ? t : t.name));
    ws.emit(v3("subscribed", { payload: { ref: op.ref, topics: topics.map((name) => (states[name] === undefined ? { name } : { name, state: states[name] })) } }));
    if (op.resume) ws.emit(v3("resumed", { payload: { ref: op.ref, fromSeq: op.resume.seq, toSeq: op.resume.seq, replayed: 0 } }));
  }
}

/** hello + ack of every topic the page asked for: the socket is `live`. Call inside `await act(async () => ...)`. */
export async function goLive(ws: FakeWebSocket, states: Record<string, unknown> = {}) {
  ws.emit(hello());
  await Promise.resolve(); // the sub op is sent from a microtask
  ackSubs(ws, states);
}

export function installFakeWebSocket() {
  FakeWebSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeWebSocket);
}

export const SESSION = { username: "budi", mustChangePassword: false, expiresAt: "2026-11-25T12:00:00Z", absoluteExpiresAt: "2026-12-02T00:00:00Z" };
