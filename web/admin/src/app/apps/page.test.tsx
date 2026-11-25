// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SessionGate } from "@/components/shell/SessionGate";
import { FakeWebSocket, Providers, SESSION, errorBody, installFakeWebSocket, installFetch } from "@/components/shell/testUtils";
import type { Handler } from "@/components/shell/testUtils";
import { resetAuthStateForTests } from "@/lib/api/client";
import AppsPage from "./page";

const nav = vi.hoisted(() => ({ q: "" }));
vi.mock("next/navigation", () => ({
  usePathname: () => "/apps",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(nav.q ? `q=${nav.q}` : ""),
}));

const APPS = [
  { id: "a1", name: "web-api", serviceName: "web-api.service", githubRepo: "acme/web-api", binaryPath: "/opt/web-api/bin", artifactName: "web-api-linux-amd64", enabled: true, createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z", lastJobId: "j1", lastJobStatus: "in_progress", lastDeployTag: "v1.2.0", lastDeployTime: "2026-11-25T00:00:00Z" },
  { id: "a2", name: "worker", serviceName: "worker.service", githubRepo: "acme/worker", binaryPath: "/opt/worker/bin", artifactName: "worker", enabled: false, createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z", lastJobId: "j2", lastJobStatus: "succeeded", lastDeployTag: "v0.9.0", lastDeployTime: "2026-11-24T00:00:00Z" },
];

function routes(extra: Record<string, Handler> = {}): Record<string, Handler> {
  return {
    "GET /admin/api/v1/session": () => ({ status: 200, body: SESSION }),
    "GET /admin/api/v1/stats": () => ({ status: 200, body: { generatedAt: "", range: "7d", tz: "UTC", apps: { total: 2, enabled: 1 }, deploys24h: { total: 0, succeeded: 0, failed: 0, canceled: 0 }, active: { running: 0, queued: 0 }, successRate: { succeeded: 0, total: 0, rate: null }, daily: [], rollbacks: { count: 0 }, recent: [] } }),
    "GET /admin/api/v1/apps": () => ({ status: 200, body: { apps: APPS } }),
    "GET /admin/api/v1/apps/a1/service/status": () => ({ status: 200, body: { status: "active" } }),
    "GET /admin/api/v1/apps/a2/service/status": () => ({ status: 200, body: { status: "inactive" } }),
    "GET /admin/api/v1/apps/a1/releases": () => ({ status: 200, body: { releases: ["v1.3.0", "v1.2.0"] } }),
    ...extra,
  };
}

function mount() {
  return render(
    <Providers>
      <SessionGate>
        <AppsPage />
      </SessionGate>
    </Providers>,
  );
}
const sendEvent = (frame: object) => act(() => FakeWebSocket.instances[0].send(frame));

beforeEach(() => {
  resetAuthStateForTests();
  installFakeWebSocket();
  nav.q = "";
  window.localStorage.setItem("ad.locale", "id");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Apps page (T088, FR-078, B57, B52, FR-073)", () => {
  it("lists apps with their fields, enabled state and the localized service state", async () => {
    installFetch(routes());
    mount();
    const card = await screen.findByTestId("app-card-a1");
    expect(within(card).getByRole("heading", { level: 2, name: "web-api" })).toBeInTheDocument();
    expect(within(card).getByText("acme/web-api")).toBeInTheDocument();
    expect(within(card).getByText("Aktif", { selector: "span.rounded-pill" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("service-status-a1")).toHaveTextContent("Aktif"));
    await waitFor(() => expect(screen.getByTestId("service-status-a2")).toHaveTextContent("Tidak aktif"));
    const second = screen.getByTestId("app-card-a2");
    expect(within(second).getByText("Nonaktif")).toBeInTheDocument();
    expect(within(second).queryByRole("button", { name: "Deploy" })).toBeNull(); // disabled app: no manual deploy
    expect(within(card).getByRole("link", { name: "Ubah" })).toHaveAttribute("href", expect.stringContaining("apps/edit"));
    expect(within(card).getByRole("link", { name: "Ubah" }).getAttribute("href")).toContain("id=a1");
  });

  it("filters by ?q (header search)", async () => {
    nav.q = "work";
    installFetch(routes());
    mount();
    expect(await screen.findByTestId("app-card-a2")).toBeInTheDocument();
    expect(screen.queryByTestId("app-card-a1")).toBeNull();
    cleanup();
    nav.q = "zzz";
    mount();
    expect(await screen.findByTestId("apps-empty")).toHaveTextContent("Tidak ada app yang cocok dengan “zzz”.");
  });

  it("a terminal job_status event invalidates the apps list AND the service status (B57, FR-078)", async () => {
    const api = installFetch(routes());
    mount();
    await screen.findByTestId("app-card-a1");
    await waitFor(() => expect(api.count("GET", "/admin/api/v1/apps/a1/service/status")).toBe(1));
    const appsBefore = api.count("GET", "/admin/api/v1/apps");

    sendEvent({ protocol: "2", id: "e1", type: "job_status", ts: new Date().toISOString(), appId: "a1", jobId: "j1", payload: { status: "succeeded", previousStatus: "in_progress", tag: "v1.2.0" } });
    await waitFor(() => expect(api.count("GET", "/admin/api/v1/apps")).toBeGreaterThan(appsBefore));
    await waitFor(() => expect(api.count("GET", "/admin/api/v1/apps/a1/service/status")).toBe(2));
    expect(api.count("GET", "/admin/api/v1/apps/a2/service/status")).toBe(1); // the other app is untouched
  });

  it("shows live progress from the socket and offers cancel while the job is active (B52, FR-073)", async () => {
    installFetch(routes());
    mount();
    await screen.findByTestId("app-card-a1");
    sendEvent({ protocol: "2", id: "s1", type: "snapshot", ts: new Date().toISOString(), payload: { jobs: [{ appId: "a1", jobId: "j1", tag: "v1.2.0", status: "in_progress", phase: "downloading", percent: 42, downloadedBytes: 1048576, totalBytes: 2490368, speedBps: 524288 }] } });
    const live = await screen.findByTestId("progress-live");
    expect(live).toHaveTextContent("1.0 MB / 2.4 MB (42%)");
    expect(live).toHaveTextContent("512 KB/s");
    expect(within(screen.getByTestId("app-card-a1")).getByText("Aktivitas: Mengunduh artefak rilis")).toBeInTheDocument();
    expect(within(live).getByRole("progressbar", { name: "Kemajuan deploy" })).toBeInTheDocument();
    expect(within(screen.getByTestId("app-card-a1")).getByRole("button", { name: "Batalkan deploy" })).toBeInTheDocument();
    expect(within(screen.getByTestId("app-card-a2")).queryByRole("button", { name: "Batalkan deploy" })).toBeNull();
  });

  it("deploy dialog lists releases and POSTs the chosen tag", async () => {
    const api = installFetch(routes({ "POST /admin/api/v1/apps/a1/deploy": () => ({ status: 202, body: { status: "queued", tag: "v1.3.0" } }) }));
    mount();
    const card = await screen.findByTestId("app-card-a1");
    fireEvent.click(within(card).getByRole("button", { name: "Deploy" }));
    const select = await screen.findByLabelText("Tag rilis");
    await waitFor(() => expect(within(select as HTMLElement).getByRole("option", { name: "v1.3.0" })).toBeInTheDocument());
    fireEvent.change(select, { target: { value: "v1.3.0" } });
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Deploy" }));
    await waitFor(() => expect(api.calls.find((c) => c.method === "POST" && c.path === "/admin/api/v1/apps/a1/deploy")?.body).toEqual({ tag: "v1.3.0" }));
    expect(await screen.findByText("Deploy v1.3.0 masuk antrean.")).toBeInTheDocument();
  });

  it("falls back to a text input when the release list fails", async () => {
    installFetch(routes({ "GET /admin/api/v1/apps/a1/releases": () => ({ status: 502, body: errorBody("upstream_error") }) }));
    mount();
    const card = await screen.findByTestId("app-card-a1");
    fireEvent.click(within(card).getByRole("button", { name: "Deploy" }));
    const input = await screen.findByPlaceholderText("Tag (mis. v1.2.3)");
    expect(input.tagName).toBe("INPUT");
    expect(screen.getByText("Daftar rilis tidak tersedia; isi tag secara manual.")).toBeInTheDocument();
  });

  it("delete asks for confirmation, can also remove the unit file (Q-10), then refreshes", async () => {
    const api = installFetch(routes({ "DELETE /admin/api/v1/apps/a2": () => ({ status: 200, body: { status: "ok" } }) }));
    mount();
    const card = await screen.findByTestId("app-card-a2");
    fireEvent.click(within(card).getByRole("button", { name: "Hapus" }));
    expect(await screen.findByText("Hapus app?")).toBeInTheDocument();
    expect(api.count("DELETE", "/admin/api/v1/apps/a2")).toBe(0); // nothing before confirming
    fireEvent.click(screen.getByLabelText("Hapus juga file unit systemd yang dibuat otomatis"));
    fireEvent.click(screen.getByRole("button", { name: "Ya, hapus" }));
    await waitFor(() => expect(api.count("DELETE", "/admin/api/v1/apps/a2")).toBe(1));
    expect(api.mock.mock.calls.some((c) => String(c[0]).includes("/apps/a2?removeUnitFile=true"))).toBe(true);
    expect(await screen.findByText("App dihapus.")).toBeInTheDocument();
  });

  it("service start posts the action and shows the returned state", async () => {
    const api = installFetch(routes({ "POST /admin/api/v1/apps/a2/service/start": () => ({ status: 200, body: { status: "active", message: "ok" } }) }));
    mount();
    const card = await screen.findByTestId("app-card-a2");
    await waitFor(() => expect(screen.getByTestId("service-status-a2")).toHaveTextContent("Tidak aktif"));
    fireEvent.click(within(card).getByRole("button", { name: "Mulai" }));
    await waitFor(() => expect(api.count("POST", "/admin/api/v1/apps/a2/service/start")).toBe(1));
    await waitFor(() => expect(screen.getByTestId("service-status-a2")).toHaveTextContent("Aktif"));
  });

  it("the refresh button refetches the list; a failed list shows a localized error with retry", async () => {
    let fail = true;
    const api = installFetch(routes({ "GET /admin/api/v1/apps": () => (fail ? { status: 500, body: errorBody("internal_error") } : { status: 200, body: { apps: APPS } }) }));
    mount();
    expect(await screen.findByText("Daftar app tidak dapat dimuat.")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Coba lagi" }));
    expect(await screen.findByTestId("app-card-a1")).toBeInTheDocument();
    const before = api.count("GET", "/admin/api/v1/apps");
    fireEvent.click(screen.getByRole("button", { name: "Muat ulang" }));
    await waitFor(() => expect(api.count("GET", "/admin/api/v1/apps")).toBe(before + 1));
  });

  it("logs dialog loads the journal lazily, newest first, and says when it was truncated", async () => {
    installFetch(routes({ "GET /admin/api/v1/apps/a1/logs": () => ({ status: 200, body: { log: "line one\nline two\n", truncated: true } }) }));
    mount();
    const card = await screen.findByTestId("app-card-a1");
    fireEvent.click(within(card).getByRole("button", { name: "Lihat log" }));
    const pre = await screen.findByText(/line two/);
    expect(pre.textContent).toBe("line two\nline one");
    expect(await screen.findByTestId("logs-truncated")).toBeInTheDocument();
  });
});
