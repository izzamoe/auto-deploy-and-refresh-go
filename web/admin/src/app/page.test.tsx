// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SessionGate } from "@/components/shell/SessionGate";
import { Providers, SESSION, STATS, installFakeWebSocket, installFetch } from "@/components/shell/testUtils";
import { resetAuthStateForTests } from "@/lib/api/client";
import DashboardPage from "./page";

const DAILY = [
  { date: "2026-11-23", succeeded: 3, failed: 1, canceled: 0 },
  { date: "2026-11-24", succeeded: 5, failed: 0, canceled: 1 },
  { date: "2026-11-25", succeeded: 0, failed: 2, canceled: 0 },
];

const FULL = {
  ...STATS,
  deploys24h: { total: 9, succeeded: 7, failed: 2, canceled: 0, hourly: Array.from({ length: 24 }, (_, i) => (i % 5 === 0 ? 2 : 0)) },
  daily: DAILY,
  rollbacks: { count: 1 },
  successRate: { succeeded: 7, total: 9, rate: 0.7778, previousRate: 0.6 },
  recent: [
    { id: "j1", appId: "a1", appName: "web-api", appEnabled: true, tag: "v1.2.0", status: "succeeded", trigger: "webhook", downloadBytes: 1, downloadDurationMs: 1, downloadSpeedBps: 1, createdAt: new Date(Date.now() - 5 * 60_000).toISOString(), updatedAt: "2026-11-25T00:00:00Z" },
    { id: "j2", appId: "a2", appName: "worker", appEnabled: true, tag: "v0.9.1", status: "failed", trigger: "manual", downloadBytes: 1, downloadDurationMs: 1, downloadSpeedBps: 1, createdAt: new Date(Date.now() - 3 * 3600_000).toISOString(), updatedAt: "2026-11-25T00:00:00Z" },
  ],
};

function routes(stats: unknown = FULL) {
  return {
    "GET /admin/api/v1/session": () => ({ status: 200, body: SESSION }),
    "GET /admin/api/v1/stats": () => ({ status: 200, body: stats }),
    "GET /admin/api/v1/tokens": () => ({ status: 200, body: { tokens: [{ id: "t1", name: "ci", prefix: "adt_ab", createdAt: "2026-11-01T00:00:00Z" }, { id: "t2", name: "old", prefix: "adt_cd", createdAt: "2026-10-01T00:00:00Z", revokedAt: "2026-10-02T00:00:00Z" }] } }),
    "GET /admin/api/v1/github": () => ({ status: 200, body: { config: { token: "ghp_****", hasToken: true, source: "db" } } }),
  };
}

function mount() {
  return render(
    <Providers>
      <SessionGate>
        <DashboardPage />
      </SessionGate>
    </Providers>,
  );
}

beforeEach(() => {
  resetAuthStateForTests();
  installFakeWebSocket();
  window.localStorage.setItem("ad.locale", "id");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Dashboard (T087, FR-100, FR-026, FR-108)", () => {
  it("renders auto-deploy content from /stats", async () => {
    installFetch(routes());
    mount();
    expect(await screen.findByTestId("dashboard")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, hidden: true })).toHaveTextContent("Dasbor");
    expect(screen.getByTestId("stat-deploys-value")).toHaveTextContent("9");
    expect(screen.getByTestId("stat-apps-value")).toHaveTextContent("3/5");
    expect(screen.getByTestId("stat-failed-value")).toHaveTextContent("2");
    expect(screen.getByTestId("active-deploy-value")).toHaveTextContent("2");
    expect(screen.getByText("1 antre")).toBeInTheDocument();
    expect(screen.getByTestId("mini-bars").querySelectorAll("rect")).toHaveLength(24);
    expect(screen.getByTestId("gauge-value")).toHaveTextContent("78%");
    // recent deploys
    const recent = screen.getByTestId("recent-deploys");
    expect(within(recent).getByText("web-api")).toBeInTheDocument();
    expect(within(recent).getByText("v1.2.0")).toBeInTheDocument();
    expect(within(recent).getByText("Berhasil")).toBeInTheDocument();
    expect(within(recent).getByText("Gagal")).toBeInTheDocument();
    expect(within(recent).getByRole("link", { name: /^web-api.*Lihat riwayat$/ })).toHaveAttribute("href", expect.stringMatching(/^\/history\/?\?appId=a1$/));
    // account + add-app
    expect(screen.getByTestId("account-username")).toHaveTextContent("budi");
    expect(screen.getByRole("link", { name: "Tambah app" })).toHaveAttribute("href", expect.stringMatching(/^\/apps\/new\/?$/));
  });

  it("the chart has a hidden table with every value and a legend (not colour alone)", async () => {
    installFetch(routes());
    mount();
    const table = await screen.findByTestId("chart-table");
    expect(table).toHaveClass("sr-only");
    expect(within(table).getByRole("columnheader", { name: "Berhasil" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "Dibatalkan" })).toBeInTheDocument();
    expect(within(table).getAllByRole("row")).toHaveLength(1 + DAILY.length);
    expect(within(table).getByRole("row", { name: /3\s*1\s*0/ })).toBeInTheDocument();
    expect(screen.getByTestId("chart-legend")).toHaveTextContent("Berhasil");
    expect(screen.getByTestId("chart-legend")).toHaveTextContent("Gagal");
    expect(screen.getByRole("img", { name: "Grafik area: deploy berhasil dan gagal per hari" })).toBeInTheDocument();
  });

  it("rate=null shows 'Belum ada data' ONCE (gauge only), no delta pill, no chart line", async () => {
    installFetch(routes({ ...FULL, successRate: { succeeded: 0, total: 0, rate: null, previousRate: null }, deploys24h: { total: 0, succeeded: 0, failed: 0, canceled: 0 }, daily: [], recent: [] }));
    mount();
    await screen.findByTestId("dashboard");
    expect(screen.getByTestId("gauge-value")).toHaveTextContent("Belum ada data");
    expect(screen.queryByTestId("tile-success-rate")).toBeNull();
    expect(screen.getAllByText("Belum ada data")).toHaveLength(1); // not repeated in the chart panel
    expect(screen.getByTestId("chart-empty")).toHaveTextContent("Belum ada deploy pada rentang ini.");
    expect(screen.queryByRole("img", { name: /Grafik area/ })).toBeNull(); // no flat line at 0
    expect(screen.queryByTestId("chart-table")).toBeNull();
    expect(screen.queryByTestId("delta-pill")).toBeNull();
    expect(screen.queryByTestId("gauge-arc")).toBeNull();
    expect(screen.getByText("Belum ada deploy.")).toBeInTheDocument();
    expect(screen.queryByTestId("mini-bars")).toBeNull(); // no series is invented
  });

  it("the failures badge is always red (also with 0 failures) and an empty 'Latest deploys' card does not stretch to the other column", async () => {
    installFetch(routes({ ...FULL, deploys24h: { total: 0, succeeded: 0, failed: 0, canceled: 0 }, recent: [] }));
    mount();
    await screen.findByTestId("dashboard");
    expect(screen.getByTestId("stat-failed-icon")).toHaveClass("bg-destructive");
    expect(screen.getByTestId("stat-deploys-icon")).toHaveClass("bg-primary");
    expect(screen.getByTestId("recent-deploys").parentElement?.className).not.toContain("flex-1");
    cleanup();
    installFetch(routes());
    mount();
    expect((await screen.findByTestId("recent-deploys")).parentElement?.className).toContain("flex-1"); // with data it still balances the columns
  });

  it("days present but all zero: only the empty state, never a red line flat at 0", async () => {
    const zeroDays = DAILY.map((d) => ({ ...d, succeeded: 0, failed: 0, canceled: 0 }));
    installFetch(routes({ ...FULL, daily: zeroDays }));
    mount();
    await screen.findByTestId("dashboard");
    expect(screen.getByTestId("chart-empty")).toBeInTheDocument();
    expect(screen.queryByTestId("chart-legend")).toBeNull();
    expect(screen.queryByRole("img", { name: /Grafik area/ })).toBeNull();
  });

  it("a day with data draws the chart and no empty state", async () => {
    installFetch(routes());
    mount();
    await screen.findByTestId("dashboard");
    expect(screen.queryByTestId("chart-empty")).toBeNull();
    expect(screen.getByRole("img", { name: /Grafik area/ })).toBeInTheDocument();
  });

  it("the success rate is shown once: in the gauge card, with the delta pill, not in the chart panel", async () => {
    installFetch(routes());
    mount();
    await screen.findByTestId("dashboard");
    expect(screen.queryByTestId("tile-success-rate")).toBeNull();
    expect(within(screen.getByTestId("success-gauge")).getByTestId("delta-pill")).toBeInTheDocument();
    expect(within(screen.getByTestId("activity")).queryByTestId("delta-pill")).toBeNull();
  });

  it("delta pill says how the rate moved against the previous period", async () => {
    installFetch(routes());
    mount();
    const pill = await screen.findByTestId("delta-pill");
    expect(pill).toHaveTextContent("+18"); // 0.7778 - 0.6 = 17.78 -> 18 points
    expect(pill).toHaveAttribute("aria-label", "naik 18 poin dibanding periode sebelumnya");
  });

  it("the 30d selector refetches /stats?range=30d and is keyboard-operable (aria-pressed)", async () => {
    const api = installFetch(routes());
    mount();
    const btn30 = await screen.findByRole("button", { name: "30 hari" });
    expect(btn30).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("button", { name: "7 hari" })).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(btn30);
    await waitFor(() => expect(api.mock.mock.calls.some((c) => String(c[0]).includes("/stats?range=30d"))).toBe(true));
    await waitFor(() => expect(screen.getByRole("button", { name: "30 hari" })).toHaveAttribute("aria-pressed", "true"));
  });

  it("security card uses real signals (active tokens only, GitHub token) and is honest when a check fails", async () => {
    const r = routes();
    installFetch({ ...r, "GET /admin/api/v1/github": () => ({ status: 500, body: { status: "error", code: "internal_error", error: "x", errors: [] } }) });
    mount();
    const card = await screen.findByTestId("security-card");
    await waitFor(() => expect(within(card).getByText("1 token API aktif")).toBeInTheDocument()); // the revoked one is not counted
    expect(within(card).getByText("Kata sandi bawaan sudah diganti")).toBeInTheDocument();
    await waitFor(() => expect(card.querySelectorAll('[data-state="unknown"]').length).toBe(1)); // GitHub: could not be checked
    expect(within(card).getByRole("link", { name: "Buka akun" })).toHaveAttribute("href", expect.stringMatching(/^\/account\/?$/));
  });

  it("a failed /stats shows a localized error with retry", async () => {
    let n = 0;
    installFetch({ ...routes(), "GET /admin/api/v1/stats": () => (++n === 1 ? { status: 500, body: { status: "error", code: "internal_error", error: "boom", errors: [] } } : { status: 200, body: FULL }) });
    mount();
    expect(await screen.findByText("Data dasbor tidak dapat dimuat.")).toBeInTheDocument();
    expect(screen.queryByText("boom")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Coba lagi" }));
    expect(await screen.findByTestId("dashboard")).toBeInTheDocument();
  });

  it("English locale translates the dashboard", async () => {
    window.localStorage.setItem("ad.locale", "en");
    installFetch(routes());
    mount();
    expect(await screen.findByText("Deploy activity")).toBeInTheDocument();
    expect(screen.getByText("Latest deploys")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "30 days" })).toBeInTheDocument();
  });
});
