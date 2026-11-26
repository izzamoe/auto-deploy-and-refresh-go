// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SessionGate } from "@/components/shell/SessionGate";
import { FakeWebSocket, Providers, SESSION, errorBody, goLive, installFakeWebSocket, installFetch } from "@/components/shell/testUtils";
import type { Handler } from "@/components/shell/testUtils";
import { resetAuthStateForTests } from "@/lib/api/client";
import HistoryPage from "./page";

const nav = vi.hoisted(() => ({ params: "" }));
vi.mock("next/navigation", () => ({
  usePathname: () => "/history",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(nav.params),
}));

const U = "/admin/api/v1";
const job = (n: number, over: Record<string, unknown> = {}) => ({
  id: `j${n}`, appId: "a1", appName: "web-api", appEnabled: true, tag: `v1.0.${n}`, status: "succeeded", trigger: "webhook", errorMsg: "", downloadBytes: 1, downloadDurationMs: 1, downloadSpeedBps: 1,
  createdAt: "2026-11-24T10:00:00Z", updatedAt: "2026-11-24T10:01:00Z", ...over,
});
const pageOf = (jobs: unknown[], total = jobs.length, page = 1, extra: object = {}) => ({ history: jobs, total, page, pageSize: 20, ...extra });

function routes(extra: Record<string, Handler> = {}): Record<string, Handler> {
  return {
    [`GET ${U}/session`]: () => ({ status: 200, body: SESSION }),
    [`GET ${U}/stats`]: () => ({ status: 200, body: { generatedAt: "", range: "7d", tz: "UTC", apps: { total: 1, enabled: 1 }, deploys24h: { total: 0, succeeded: 0, failed: 0, canceled: 0 }, active: { running: 0, queued: 0 }, successRate: { succeeded: 0, total: 0, rate: null }, daily: [], rollbacks: { count: 0 }, recent: [] } }),
    [`GET ${U}/history`]: () => ({ status: 200, body: pageOf([job(1), job(2, { status: "failed", errorMsg: "health check failed", rolledBack: true }), job(3, { status: "in_progress" })]) }),
    ...extra,
  };
}

function mount() {
  return render(
    <Providers>
      <SessionGate>
        <HistoryPage />
      </SessionGate>
    </Providers>,
  );
}
const historyUrls = (api: ReturnType<typeof installFetch>) => api.mock.mock.calls.map((c) => String(c[0])).filter((u) => u.startsWith(`${U}/history?`));

beforeEach(() => {
  resetAuthStateForTests();
  installFakeWebSocket();
  nav.params = "";
  window.localStorage.setItem("ad.locale", "id");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("History page (T090, FR-077, B56, B52)", () => {
  it("renders a semantic table with localized status, trigger, rollback marker and actions by state", async () => {
    installFetch(routes());
    mount();
    const table = await screen.findByTestId("history-table");
    expect(within(table).getByRole("columnheader", { name: "Status" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "Aplikasi" })).toBeInTheDocument(); // unfiltered: app column
    const failed = screen.getByTestId("history-row-j2");
    expect(within(failed).getByText("Gagal")).toBeInTheDocument();
    expect(within(failed).getByText("Webhook")).toBeInTheDocument();
    expect(within(failed).getByText("Dikembalikan")).toBeInTheDocument();
    expect(within(failed).getByText("health check failed")).toBeInTheDocument();
    expect(within(failed).getByRole("button", { name: "Ulangi deploy v1.0.2" })).toBeInTheDocument();
    expect(within(failed).queryByRole("button", { name: /Batalkan/ })).toBeNull();
    const running = screen.getByTestId("history-row-j3");
    expect(within(running).getByRole("button", { name: "Batalkan deploy v1.0.3" })).toBeInTheDocument();
    expect(within(running).queryByRole("button", { name: /Ulangi/ })).toBeNull();
  });

  it("paginates (20 per page) and shows the page/total text", async () => {
    const api = installFetch(
      routes({
        [`GET ${U}/history`]: (init) => {
          const p = Number(new URL(init.url, "http://x").searchParams.get("page") ?? "1");
          return { status: 200, body: pageOf([job(p * 10)], 45, p) };
        },
      }),
    );
    mount();
    expect(await screen.findByText("Halaman 1 dari 3 · 45 deploy")).toBeInTheDocument();
    expect(screen.getByTestId("history-prev")).toBeDisabled();
    fireEvent.click(screen.getByTestId("history-next"));
    expect(await screen.findByText("Halaman 2 dari 3 · 45 deploy")).toBeInTheDocument();
    expect(historyUrls(api).some((u) => u.includes("page=2") && u.includes("pageSize=20"))).toBe(true);
    fireEvent.click(screen.getByTestId("history-next"));
    await screen.findByText("Halaman 3 dari 3 · 45 deploy");
    expect(screen.getByTestId("history-next")).toBeDisabled();
  });

  it("B56: changing the app filter aborts the old request, resets to page 1 and ignores its late response", async () => {
    let release: (r: { status: number; body: unknown }) => void = () => {};
    const slow = new Promise<{ status: number; body: unknown }>((r) => { release = r; });
    let signalOfFirst: AbortSignal | undefined;
    installFetch(
      routes({
        [`GET ${U}/history`]: (init) => {
          const u = new URL(init.url, "http://x");
          if (u.searchParams.get("appId") === "a1") { signalOfFirst = init.signal ?? undefined; return slow; }
          return { status: 200, body: pageOf([job(7, { appId: "a2", appName: "worker", tag: "v2.0.0" })], 1, 1, { app: { id: "a2", name: "worker" } }) };
        },
      }),
    );
    nav.params = "appId=a1";
    const view = mount();
    await waitFor(() => expect(signalOfFirst).toBeDefined());

    nav.params = "appId=a2";
    view.rerender(
      <Providers>
        <SessionGate>
          <HistoryPage />
        </SessionGate>
      </Providers>,
    );
    expect(await screen.findByText("v2.0.0")).toBeInTheDocument();
    await waitFor(() => expect(signalOfFirst?.aborted).toBe(true)); // old request cancelled
    release({ status: 200, body: pageOf([job(9, { tag: "v9.9.9" })]) }); // arrives late
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText("v9.9.9")).toBeNull();
    expect(screen.getByText("Menampilkan riwayat worker")).toBeInTheDocument();
  });

  it("filtered history drops the app column and offers 'show all'", async () => {
    nav.params = "appId=a1";
    installFetch(routes({ [`GET ${U}/history`]: () => ({ status: 200, body: pageOf([job(1)], 1, 1, { app: { id: "a1", name: "web-api" } }) }) }));
    mount();
    const table = await screen.findByTestId("history-table");
    expect(within(table).queryByRole("columnheader", { name: "Aplikasi" })).toBeNull();
    expect(screen.getByRole("link", { name: "Tampilkan semua aplikasi" })).toBeInTheDocument();
  });

  it("retry posts once, hides the button for that job and refreshes the history", async () => {
    const api = installFetch(routes({ [`POST ${U}/history/j2/retry`]: () => ({ status: 202, body: { status: "queued", jobId: "j99" } }) }));
    mount();
    const row = await screen.findByTestId("history-row-j2");
    const before = api.count("GET", `${U}/history`);
    fireEvent.click(within(row).getByRole("button", { name: "Ulangi deploy v1.0.2" }));
    expect(await screen.findByText("Deploy ulang diantrekan.")).toBeInTheDocument();
    expect(api.count("POST", `${U}/history/j2/retry`)).toBe(1);
    await waitFor(() => expect(api.count("GET", `${U}/history`)).toBeGreaterThan(before));
    expect(within(screen.getByTestId("history-row-j2")).queryByRole("button", { name: /Ulangi/ })).toBeNull();
  });

  it("cancel posts to the job and reports an error localized when it fails", async () => {
    installFetch(routes({ [`POST ${U}/jobs/j3/cancel`]: () => ({ status: 409, body: errorBody("duplicate") }) }));
    mount();
    const row = await screen.findByTestId("history-row-j3");
    fireEvent.click(within(row).getByRole("button", { name: "Batalkan deploy v1.0.3" }));
    expect(await screen.findByText("Data dengan nilai yang sama sudah ada.")).toBeInTheDocument();
  });

  it("job logs open in a dialog (lazy) and say when truncated", async () => {
    installFetch(routes({ [`GET ${U}/jobs/j2/logs`]: () => ({ status: 200, body: { log: "boom\n", truncated: true } }) }));
    mount();
    const row = await screen.findByTestId("history-row-j2");
    fireEvent.click(within(row).getByRole("button", { name: "Lihat log deploy v1.0.2" }));
    expect(await screen.findByText("boom")).toBeInTheDocument();
    expect(await screen.findByTestId("logs-truncated")).toBeInTheDocument();
    expect(screen.getByText("Log deploy v1.0.2")).toBeInTheDocument();
  });

  it("empty history and failures are explained; unknown app (404) is distinguished", async () => {
    installFetch(routes({ [`GET ${U}/history`]: () => ({ status: 200, body: pageOf([], 0) }) }));
    mount();
    expect(await screen.findByText("Belum ada riwayat deploy")).toBeInTheDocument();
    cleanup();
    nav.params = "appId=zz";
    installFetch(routes({ [`GET ${U}/history`]: () => ({ status: 404, body: errorBody("not_found") }) }));
    mount();
    expect(await screen.findByText("Aplikasi untuk riwayat ini tidak ditemukan.")).toBeInTheDocument();
  });
});

describe("history on topic jobs (002 T058)", () => {
  it("refetches only after job events (several -> ONE request), never on focus or by a timer", async () => {
    const api = installFetch(routes());
    mount();
    await screen.findByTestId("history-table");
    await act(async () => goLive(FakeWebSocket.instances[0]));
    expect(FakeWebSocket.instances[0].subscribedTopics()).toEqual(expect.arrayContaining(["jobs", "apps"]));
    const n = () => historyUrls(api).length;
    expect(n()).toBe(1);
    await act(async () => { window.dispatchEvent(new Event("visibilitychange")); await new Promise((r) => setTimeout(r, 50)); });
    expect(n()).toBe(1);
    await act(async () => {
      for (let i = 0; i < 3; i++) {
        FakeWebSocket.instances[0].emit({ protocol: "3", id: `evt-${10 + i}`, seq: 10 + i, topic: "jobs", type: "job_status", ts: new Date().toISOString(), appId: "a1", jobId: "j9", payload: { status: i === 0 ? "pending" : i === 1 ? "in_progress" : "succeeded" } });
      }
    });
    await waitFor(() => expect(n()).toBe(2));
    await act(async () => { await new Promise((r) => setTimeout(r, 500)); });
    expect(n()).toBe(2);
  });
});
