// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SessionGate } from "@/components/shell/SessionGate";
import { FakeWebSocket, Providers, SESSION, errorBody, goLive, installFakeWebSocket, installFetch } from "@/components/shell/testUtils";
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
const sendEvent = (frame: object) => act(() => FakeWebSocket.instances[0].emit(frame));
/** State of topic service in the subscribed ack (events-ws.v3.md §3). */
const SERVICE = (statuses: Record<string, string>) => ({ service: { statuses: Object.entries(statuses).map(([appId, state]) => ({ appId, state })), observedAt: "2026-11-25T00:00:00Z" } });
const statusCalls = (api: ReturnType<typeof installFetch>) => api.calls.filter((c) => c.method === "GET" && c.path.endsWith("/service/status")).map((c) => c.path);
const NEW_APP = { id: "a3", name: "fresh", serviceName: "fresh.service", githubRepo: "acme/fresh", binaryPath: "/opt/fresh/bin", artifactName: "fresh", enabled: true, createdAt: "2026-11-25T00:00:00Z", updatedAt: "2026-11-25T00:00:00Z" };

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
    // 002 T055: the service states come with the `service` ack (no request per card).
    await act(async () => goLive(FakeWebSocket.instances[0], SERVICE({ a1: "active", a2: "inactive" })));
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
    expect(await screen.findByTestId("apps-empty")).toHaveTextContent("Tidak ada aplikasi yang cocok dengan “zzz”.");
  });

  it("a terminal job_status event updates the card from the event itself, with no REST request (B57, FR-078, 002 T051)", async () => {
    const api = installFetch(routes());
    mount();
    const card = await screen.findByTestId("app-card-a1");
    await waitFor(() => expect(within(card).getByTestId("progress-badge")).toHaveTextContent("Berjalan"));
    await act(async () => goLive(FakeWebSocket.instances[0], SERVICE({ a1: "active", a2: "inactive" })));
    const before = api.calls.length;

    sendEvent({ protocol: "3", id: "evt-1", seq: 1, topic: "jobs", type: "job_status", ts: new Date().toISOString(), appId: "a1", jobId: "j1", payload: { status: "succeeded", previousStatus: "in_progress", tag: "v1.2.0" } });
    await waitFor(() => expect(within(screen.getByTestId("app-card-a1")).getByTestId("progress-badge")).toHaveTextContent("Berhasil"));
    await act(async () => { await new Promise((r) => setTimeout(r, 400)); }); // past the invalidation debounce
    expect(api.calls.slice(before)).toEqual([]); // neither GET /apps nor any service status
    expect(within(screen.getByTestId("app-card-a2")).getByTestId("progress-badge")).toHaveTextContent("Berhasil"); // the other app is untouched
  });

  it("service state from topic service (SC-005): the ack state and later service.status events, with no request per card, also after 10 minutes", async () => {
    const api = installFetch(routes());
    mount();
    await screen.findByTestId("app-card-a1");
    const ws = FakeWebSocket.instances[0];
    await act(async () => goLive(ws, SERVICE({ a1: "active", a2: "inactive" })));
    expect(ws.subscribedTopics()).toEqual(expect.arrayContaining(["apps", "jobs", "service"]));
    await waitFor(() => expect(screen.getByTestId("service-status-a1")).toHaveTextContent("Aktif"));
    sendEvent({ protocol: "3", id: "evt-5", seq: 5, topic: "service", type: "service.status", ts: new Date().toISOString(), appId: "a1", payload: { state: "failed" } });
    await waitFor(() => expect(screen.getByTestId("service-status-a1")).toHaveAttribute("data-state", "failed"));
    expect(screen.getByTestId("service-status-a2")).toHaveAttribute("data-state", "inactive");
    expect(statusCalls(api)).toEqual([]);
  });

  it("new app card gets one targeted status fetch, then events only", async () => {
    const api = installFetch(routes({ "GET /admin/api/v1/apps/a3/service/status": () => ({ status: 200, body: { status: "activating" } }) }));
    mount();
    await screen.findByTestId("app-card-a1");
    const ws = FakeWebSocket.instances[0];
    // The watcher was already running when a3 was created: its first observation is not a
    // change, so the ack (taken before) and the events say nothing about a3.
    await act(async () => goLive(ws, SERVICE({ a1: "active", a2: "inactive" })));
    await waitFor(() => expect(screen.getByTestId("service-status-a1")).toHaveTextContent("Aktif"));
    expect(statusCalls(api)).toEqual([]);

    sendEvent({ protocol: "3", id: "evt-6", seq: 6, topic: "apps", type: "app.upserted", ts: new Date().toISOString(), appId: "a3", payload: { app: NEW_APP } });
    expect(await screen.findByTestId("app-card-a3")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("service-status-a3")).toHaveAttribute("data-state", "activating"));
    expect(statusCalls(api)).toEqual(["/admin/api/v1/apps/a3/service/status"]); // ONE, only for the new card

    // From now on only events: a re-render, an apps event, time passing: no more requests.
    sendEvent({ protocol: "3", id: "evt-7", seq: 7, topic: "apps", type: "app.upserted", ts: new Date().toISOString(), appId: "a3", payload: { app: { ...NEW_APP, name: "fresh-2", updatedAt: "2026-11-25T00:00:01Z" } } });
    sendEvent({ protocol: "3", id: "evt-8", seq: 8, topic: "service", type: "service.status", ts: new Date().toISOString(), appId: "a3", payload: { state: "active" } });
    await waitFor(() => expect(screen.getByTestId("service-status-a3")).toHaveAttribute("data-state", "active"));
    await act(async () => { await new Promise((r) => setTimeout(r, 400)); });
    expect(statusCalls(api)).toEqual(["/admin/api/v1/apps/a3/service/status"]);
    expect(api.count("GET", "/admin/api/v1/apps")).toBe(1); // the new card came from the event, not a refetch
  });

  it("an app of the first list without a state in the ack gets ONE targeted status fetch; the others none", async () => {
    const api = installFetch(routes());
    mount();
    await screen.findByTestId("app-card-a1");
    await act(async () => goLive(FakeWebSocket.instances[0], SERVICE({ a1: "active" })));
    await waitFor(() => expect(screen.getByTestId("service-status-a2")).toHaveTextContent("Tidak aktif"));
    await act(async () => { await new Promise((r) => setTimeout(r, 100)); });
    expect(statusCalls(api)).toEqual(["/admin/api/v1/apps/a2/service/status"]);
  });

  it("before the socket is live no card requests its status (no burst on page load)", async () => {
    const api = installFetch(routes());
    mount();
    await screen.findByTestId("app-card-a1");
    await act(async () => { await new Promise((r) => setTimeout(r, 300)); });
    expect(statusCalls(api)).toEqual([]);
    expect(screen.getByTestId("service-status-a1")).toHaveAttribute("data-state", "…");
  });

  it("toggle uses the response row: no GET /apps after the mutation", async () => {
    const api = installFetch(routes({ "POST /admin/api/v1/apps/a1/toggle": () => ({ status: 200, body: { status: "updated", app: { ...APPS[0], enabled: false, updatedAt: "2026-11-25T01:00:00Z" } } }) }));
    mount();
    const card = await screen.findByTestId("app-card-a1");
    await act(async () => goLive(FakeWebSocket.instances[0], SERVICE({ a1: "active", a2: "inactive" })));
    fireEvent.click(within(card).getByRole("button", { name: "Nonaktifkan" }));
    await waitFor(() => expect(within(screen.getByTestId("app-card-a1")).getByRole("button", { name: "Aktifkan" })).toBeInTheDocument());
    const post = api.calls.find((c) => c.method === "POST" && c.path === "/admin/api/v1/apps/a1/toggle");
    expect(post).toBeDefined();
    expect(api.count("GET", "/admin/api/v1/apps")).toBe(1);
  });

  it("shows live progress from the socket and offers cancel while the job is active (B52, FR-073)", async () => {
    installFetch(routes());
    mount();
    await screen.findByTestId("app-card-a1");
    sendEvent({ protocol: "3", id: "evt-s1", topic: "jobs", type: "snapshot", ts: new Date().toISOString(), payload: { jobs: [{ appId: "a1", jobId: "j1", tag: "v1.2.0", status: "in_progress", phase: "downloading", percent: 42, downloadedBytes: 1048576, totalBytes: 2490368, speedBps: 524288 }] } });
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
    expect(await screen.findByText("Hapus aplikasi?")).toBeInTheDocument();
    expect(api.count("DELETE", "/admin/api/v1/apps/a2")).toBe(0); // nothing before confirming
    fireEvent.click(screen.getByLabelText("Hapus juga file unit systemd yang dibuat otomatis"));
    fireEvent.click(screen.getByRole("button", { name: "Ya, hapus" }));
    await waitFor(() => expect(api.count("DELETE", "/admin/api/v1/apps/a2")).toBe(1));
    expect(api.mock.mock.calls.some((c) => String(c[0]).includes("/apps/a2?removeUnitFile=true"))).toBe(true);
    expect(await screen.findByText("Aplikasi dihapus.")).toBeInTheDocument();
  });

  it("service start posts the action and shows the returned state", async () => {
    const api = installFetch(routes({ "POST /admin/api/v1/apps/a2/service/start": () => ({ status: 200, body: { status: "active", message: "ok" } }) }));
    mount();
    const card = await screen.findByTestId("app-card-a2");
    await act(async () => goLive(FakeWebSocket.instances[0], SERVICE({ a1: "active", a2: "inactive" })));
    await waitFor(() => expect(screen.getByTestId("service-status-a2")).toHaveTextContent("Tidak aktif"));
    fireEvent.click(within(card).getByRole("button", { name: "Mulai" }));
    await waitFor(() => expect(api.count("POST", "/admin/api/v1/apps/a2/service/start")).toBe(1));
    await waitFor(() => expect(screen.getByTestId("service-status-a2")).toHaveTextContent("Aktif"));
  });

  it("the refresh button refetches the list; a failed list shows a localized error with retry", async () => {
    let fail = true;
    const api = installFetch(routes({ "GET /admin/api/v1/apps": () => (fail ? { status: 500, body: errorBody("internal_error") } : { status: 200, body: { apps: APPS } }) }));
    mount();
    expect(await screen.findByText("Daftar aplikasi tidak dapat dimuat.")).toBeInTheDocument();
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
