// @vitest-environment jsdom
// T017 (RED, FR-011, FR-014, SC-003): reconnecting the event socket must not refetch the
// data of the page. Before 002 the provider calls invalidateAll on every reconnect, so
// every active query (3 here; N+3 on /apps/ with N apps) is fetched again although the
// server could replay what was missed (protocol 3: resume{epoch,seq}).
//
// The fake socket already has the protocol-3 shape (RV2-3): `send` records what the client
// sends and `emit` pushes a server frame. This test only drives open/close, so it neither
// breaks when SocketLike gains `send` (T050) nor passes by accident on protocol_mismatch.
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, cleanup, render } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetAuthStateForTests } from "@/lib/api/client";
import { AdminEventProvider } from "./provider";
import type { SocketLike } from "./provider";

class FakeV3Socket implements SocketLike {
  static all: FakeV3Socket[] = [];
  onopen: SocketLike["onopen"] = null;
  onmessage: SocketLike["onmessage"] = null;
  onclose: SocketLike["onclose"] = null;
  onerror: SocketLike["onerror"] = null;
  close = vi.fn();
  /** What the client sends to the server (protocol 3 ops). */
  send = vi.fn();
  constructor(public url: string) {
    FakeV3Socket.all.push(this);
  }
  open() {
    this.onopen?.({});
  }
  /** A frame pushed by the server. */
  emit(frame: string) {
    this.onmessage?.({ data: frame });
  }
  drop(code: number) {
    this.onclose?.({ code });
  }
}

const latest = () => FakeV3Socket.all[FakeV3Socket.all.length - 1];

beforeEach(() => {
  FakeV3Socket.all = [];
  resetAuthStateForTests();
  vi.useFakeTimers();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("AdminEventProvider reconnect (T017)", () => {
  it("a reconnect after a dropped socket (1006) refetches no active query", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const fns = [vi.fn(async () => ({ n: 1 })), vi.fn(async () => ({ n: 2 })), vi.fn(async () => ({ n: 3 }))];
    function Q({ i }: { i: number }) {
      useQuery({ queryKey: ["t017", i], queryFn: fns[i] });
      return null;
    }
    render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(
          AdminEventProvider,
          { socketFactory: (u: string) => new FakeV3Socket(u) },
          createElement(Q, { i: 0 }),
          createElement(Q, { i: 1 }),
          createElement(Q, { i: 2 }),
        ),
      ),
    );
    // The three queries are active and loaded once (the initial page load).
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });
    for (const fn of fns) expect(fn).toHaveBeenCalledTimes(1);

    act(() => latest().open());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });
    const beforeDrop = fns.reduce((n, fn) => n + fn.mock.calls.length, 0);

    act(() => latest().drop(1006)); // Wi-Fi blip / edge restart
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000); // backoff 1 s -> second socket
    });
    expect(FakeV3Socket.all).toHaveLength(2);
    act(() => latest().open());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });

    const refetched = fns.reduce((n, fn) => n + fn.mock.calls.length, 0) - beforeDrop;
    expect(refetched).toBe(0);
  });
});
