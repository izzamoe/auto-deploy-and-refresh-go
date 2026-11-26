// @vitest-environment jsdom
// 002 T052 (FR-014, Q-4=A): event-backed query defaults. While the topic is confirmed and the
// socket live, a focus or a network reconnect refetches nothing; when the socket has not been
// live for more than 5 s, a focus refetches (fallback) but nothing ever polls.
import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, cleanup, render } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetAuthStateForTests } from "@/lib/api/client";
import { AdminEventProvider, useTopic } from "@/lib/events/provider";
import type { SocketLike } from "@/lib/events/provider";
import { FALLBACK_AFTER_MS } from "@/lib/events/store";
import { FALLBACK_STALE_MS, createQueryClient, liveQueryOptions } from "./query";

class FakeSocket implements SocketLike {
  static all: FakeSocket[] = [];
  onopen: SocketLike["onopen"] = null;
  onmessage: SocketLike["onmessage"] = null;
  onclose: SocketLike["onclose"] = null;
  onerror: SocketLike["onerror"] = null;
  close = vi.fn();
  send = vi.fn();
  constructor(public url: string) {
    FakeSocket.all.push(this);
  }
  emit(frame: object) { this.onmessage?.({ data: JSON.stringify(frame) }); }
  drop(code: number) { this.onclose?.({ code }); }
  ack() {
    for (const c of this.send.mock.calls) {
      const op = JSON.parse(String(c[0]));
      if (op.op !== "sub") continue;
      this.emit({ protocol: "3", id: "x", type: "subscribed", ts: "t", payload: { ref: op.ref, topics: op.topics.map((t: { name: string }) => ({ name: t.name })) } });
      if (op.resume) this.emit({ protocol: "3", id: "y", type: "resumed", ts: "t", payload: { ref: op.ref, fromSeq: op.resume.seq, toSeq: op.resume.seq, replayed: 0 } });
    }
  }
}
const latest = () => FakeSocket.all[FakeSocket.all.length - 1];
const hello = { protocol: "3", id: "h", type: "hello", ts: "t", payload: { serverVersion: "v", heartbeatSeconds: 30, epoch: "E", seq: 0, clientId: "c-00000001" } };
const focus = () => act(async () => { window.dispatchEvent(new Event("visibilitychange")); await vi.advanceTimersByTimeAsync(10); });

beforeEach(() => {
  FakeSocket.all = [];
  resetAuthStateForTests();
  vi.useFakeTimers();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("query defaults (Q-4=A)", () => {
  it("no polling and no refetch on a network reconnect by default; focus refetch stays as the fallback", () => {
    const q = createQueryClient().getDefaultOptions().queries!;
    expect(q.refetchInterval).toBeUndefined();
    expect(q.refetchOnReconnect).toBe(false);
    expect(q.refetchOnWindowFocus).toBe(true);
    expect(q.staleTime).toBe(FALLBACK_STALE_MS);
  });

  it("liveQueryOptions: Infinity and no focus/reconnect refetch while fresh, the defaults otherwise", () => {
    expect(liveQueryOptions(true)).toEqual({ staleTime: Infinity, refetchOnWindowFocus: false, refetchOnReconnect: false });
    expect(liveQueryOptions(false)).toEqual({});
  });
});

describe("event-backed query in the provider", () => {
  function mount() {
    const client = createQueryClient();
    client.setDefaultOptions({ queries: { ...client.getDefaultOptions().queries, retry: false } });
    const fn = vi.fn(async () => ({ apps: [] }));
    function Page() {
      const fresh = useTopic("apps");
      useQuery({ queryKey: ["apps"], queryFn: fn, ...liveQueryOptions(fresh) });
      return null;
    }
    render(createElement(QueryClientProvider, { client }, createElement(AdminEventProvider, { socketFactory: (u: string) => new FakeSocket(u) }, createElement(Page))));
    return fn;
  }

  it("live: focus and `online` refetch nothing, and nothing polls in 10 minutes", async () => {
    const fn = mount();
    await act(async () => { latest().emit(hello); await vi.advanceTimersByTimeAsync(0); latest().ack(); await vi.advanceTimersByTimeAsync(10); });
    expect(fn).toHaveBeenCalledTimes(1); // the initial load
    await act(async () => { await vi.advanceTimersByTimeAsync(FALLBACK_STALE_MS + 1_000); });
    await focus();
    await act(async () => { window.dispatchEvent(new Event("online")); await vi.advanceTimersByTimeAsync(10); });
    await act(async () => { await vi.advanceTimersByTimeAsync(600_000); });
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("not live for less than 5 s: still trusted; more than 5 s: a focus refetches once (fallback), still no polling", async () => {
    const fn = mount();
    await act(async () => { latest().emit(hello); await vi.advanceTimersByTimeAsync(0); latest().ack(); await vi.advanceTimersByTimeAsync(10); });
    await act(async () => { await vi.advanceTimersByTimeAsync(FALLBACK_STALE_MS + 1_000); });
    // the socket drops and does not come back (every reconnect fails)
    const failForever = () => { const s = latest(); s.drop(1006); };
    act(failForever);
    await act(async () => { await vi.advanceTimersByTimeAsync(FALLBACK_AFTER_MS - 500); });
    await focus();
    expect(fn).toHaveBeenCalledTimes(1); // < 5 s: no refetch
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    await focus();
    expect(fn).toHaveBeenCalledTimes(2); // > 5 s: focus refetches (Q-4=A)
    await act(async () => { await vi.advanceTimersByTimeAsync(300_000); });
    expect(fn).toHaveBeenCalledTimes(2); // and nothing polls
  });
});
