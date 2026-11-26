// @vitest-environment jsdom
// 002 T061 (FR-022): one polite live region; short messages; focus never moves; at most one
// message per 2 s (a burst keeps the newest). Users: AppCard (enable/disable/delete/service of
// that app from another session), the apps page (app added) and the Header (live updates lost /
// back).
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import AppsPage from "@/app/apps/page";
import { AppShell } from "@/components/shell/AppShell";
import { SessionGate } from "@/components/shell/SessionGate";
import { FakeWebSocket, Providers, SESSION, goLive, installFakeWebSocket, installFetch } from "@/components/shell/testUtils";
import { resetAuthStateForTests } from "@/lib/api/client";
import { ANNOUNCE_INTERVAL_MS, LiveAnnouncer, announce } from "./LiveAnnouncer";

vi.mock("next/navigation", () => ({
  usePathname: () => "/apps/",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

beforeEach(() => {
  resetAuthStateForTests();
  installFakeWebSocket();
  window.localStorage.setItem("ad.locale", "id");
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("LiveAnnouncer", () => {
  it("is ONE polite, atomic, visually hidden region; announcing never moves the focus", () => {
    render(
      <>
        <button type="button">focused</button>
        <LiveAnnouncer />
      </>,
    );
    const region = screen.getByTestId("live-announcer");
    expect(region).toHaveAttribute("aria-live", "polite");
    expect(region).toHaveAttribute("aria-atomic", "true");
    expect(region).toHaveClass("sr-only");
    expect(region).not.toHaveAttribute("tabindex");
    const button = screen.getByRole("button", { name: "focused" });
    button.focus();
    act(() => announce("Aplikasi web dinonaktifkan di sesi lain."));
    expect(region).toHaveTextContent("Aplikasi web dinonaktifkan di sesi lain.");
    expect(document.activeElement).toBe(button);
  });

  it("throttles to one message per 2 s; a burst keeps only the newest", () => {
    vi.useFakeTimers();
    render(<LiveAnnouncer />);
    const region = screen.getByTestId("live-announcer");
    act(() => {
      announce("first");
      announce("second");
      announce("third");
    });
    expect(region).toHaveTextContent("first");
    act(() => vi.advanceTimersByTime(ANNOUNCE_INTERVAL_MS - 1));
    expect(region).toHaveTextContent("first");
    act(() => vi.advanceTimersByTime(1));
    expect(region).toHaveTextContent("third"); // "second" was superseded
    act(() => vi.advanceTimersByTime(ANNOUNCE_INTERVAL_MS * 3));
    expect(region).toHaveTextContent("third");
    act(() => announce("later"));
    expect(region).toHaveTextContent("later"); // quiet for > 2 s: immediate
  });
});

describe("announcements from pushed changes", () => {
  const APP = { id: "a1", name: "web-api", serviceName: "web-api.service", githubRepo: "acme/web-api", binaryPath: "/opt/b", artifactName: "x", enabled: true, createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z" };
  const routes = () => ({
    "GET /admin/api/v1/session": () => ({ status: 200, body: SESSION }),
    "GET /admin/api/v1/apps": () => ({ status: 200, body: { apps: [APP] } }),
  });
  const ev = (type: string, seq: number, extra: object) => ({ protocol: "3", id: `evt-${seq}`, seq, ts: new Date().toISOString(), type, ...extra });
  async function mountApps() {
    installFetch(routes());
    render(
      <Providers>
        <SessionGate>
          <AppShell>
            <AppsPage />
          </AppShell>
        </SessionGate>
      </Providers>,
    );
    await screen.findByTestId("app-card-a1");
    const ws = FakeWebSocket.instances[0];
    await act(async () => goLive(ws, { service: { statuses: [{ appId: "a1", state: "active" }], observedAt: "x" }, summary: { appsTotal: 1, appsEnabled: 1, running: 0, queued: 0 } }));
    return ws;
  }
  const region = () => screen.getByTestId("live-announcer");

  it("another session disabling the app is announced with its name; our own toggle is not", async () => {
    const ws = await mountApps();
    expect(screen.getAllByTestId("live-announcer")).toHaveLength(1); // one region for the whole page
    await act(async () => ws.emit(ev("app.upserted", 5, { topic: "apps", appId: "a1", origin: "c-0000test", payload: { app: { ...APP, enabled: false, updatedAt: "2026-11-02T00:00:00Z" } } })));
    expect(region().textContent).toBe(""); // own change: nothing announced
    await act(async () => ws.emit(ev("app.upserted", 6, { topic: "apps", appId: "a1", origin: "c-99999999", payload: { app: { ...APP, enabled: true, updatedAt: "2026-11-03T00:00:00Z" } } })));
    expect(region()).toHaveTextContent("Aplikasi web-api diaktifkan di sesi lain.");
  });

  it("an app added elsewhere, a service state change and a deletion are announced (throttled, newest kept)", async () => {
    const ws = await mountApps();
    await act(async () => ws.emit(ev("app.upserted", 7, { topic: "apps", appId: "a9", origin: "c-99999999", payload: { app: { ...APP, id: "a9", name: "baru" } } })));
    expect(region()).toHaveTextContent("Aplikasi baru ditambahkan di sesi lain.");
    await act(async () => { await new Promise((r) => setTimeout(r, ANNOUNCE_INTERVAL_MS + 50)); });
    await act(async () => ws.emit(ev("service.status", 8, { topic: "service", appId: "a1", payload: { state: "failed" } })));
    expect(region()).toHaveTextContent("Service web-api kini: Gagal.");
    await act(async () => ws.emit(ev("app.deleted", 9, { topic: "apps", appId: "a1", origin: "c-99999999", payload: {} })));
    await waitFor(() => expect(region()).toHaveTextContent("Aplikasi web-api dihapus di sesi lain."), { timeout: ANNOUNCE_INTERVAL_MS + 1_000 });
  });

  it("the header says when live updates stop for more than 5 s and when they are back", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const ws = await mountApps();
    await act(async () => ws.onclose?.({ code: 1006 }));
    await act(async () => { await vi.advanceTimersByTimeAsync(5_100); });
    expect(region()).toHaveTextContent("Koneksi langsung terputus; perubahan dari sesi lain belum tampil.");
    const next = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    expect(next).not.toBe(ws);
    await act(async () => { await vi.advanceTimersByTimeAsync(ANNOUNCE_INTERVAL_MS); });
    await act(async () => goLive(next));
    await act(async () => { await vi.advanceTimersByTimeAsync(10); });
    expect(region()).toHaveTextContent("Koneksi langsung tersambung kembali.");
    fireEvent.focus(document.body);
  });
});
