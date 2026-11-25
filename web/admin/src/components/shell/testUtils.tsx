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

export type Reply = { status: number; body?: unknown };
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

export class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  onopen: ((e: unknown) => void) | null = null;
  onmessage: ((e: { data: unknown }) => void) | null = null;
  onclose: ((e: { code: number }) => void) | null = null;
  onerror: ((e: unknown) => void) | null = null;
  close = vi.fn();
  constructor(public url: string) {
    FakeWebSocket.instances.push(this);
  }
  send(frame: object | string) {
    this.onmessage?.({ data: typeof frame === "string" ? frame : JSON.stringify(frame) });
  }
}

export function installFakeWebSocket() {
  FakeWebSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeWebSocket);
}

export const SESSION = { username: "budi", mustChangePassword: false, expiresAt: "2026-11-25T12:00:00Z", absoluteExpiresAt: "2026-12-02T00:00:00Z" };
