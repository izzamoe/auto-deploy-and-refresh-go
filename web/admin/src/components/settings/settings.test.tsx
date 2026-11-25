// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SystemLogsView } from "@/components/logs/SystemLogsView";
import { SessionGate } from "@/components/shell/SessionGate";
import { Providers, SESSION, errorBody, installFakeWebSocket, installFetch } from "@/components/shell/testUtils";
import type { Handler } from "@/components/shell/testUtils";
import { resetAuthStateForTests } from "@/lib/api/client";
import { GitHubSettings } from "./GitHubSettings";
import { TelegramSettings } from "./TelegramSettings";

vi.mock("next/navigation", () => ({
  usePathname: () => "/settings",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

const U = "/admin/api/v1";
const STATS = { generatedAt: "", range: "7d", tz: "UTC", apps: { total: 0, enabled: 0 }, deploys24h: { total: 0, succeeded: 0, failed: 0, canceled: 0 }, active: { running: 0, queued: 0 }, successRate: { succeeded: 0, total: 0, rate: null }, daily: [], rollbacks: { count: 0 }, recent: [] };

function base(extra: Record<string, Handler> = {}): Record<string, Handler> {
  return { [`GET ${U}/session`]: () => ({ status: 200, body: SESSION }), [`GET ${U}/stats`]: () => ({ status: 200, body: STATS }), ...extra };
}
function mount(ui: React.ReactNode) {
  return render(
    <Providers>
      <SessionGate>{ui}</SessionGate>
    </Providers>,
  );
}
const bodies = (api: ReturnType<typeof installFetch>, method: string, path: string) => api.calls.filter((c) => c.method === method && c.path === path).map((c) => c.body as Record<string, unknown>);

beforeEach(() => {
  resetAuthStateForTests();
  installFakeWebSocket();
  window.localStorage.setItem("ad.locale", "id");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("System logs (FR-029, FR-058)", () => {
  it("shows the newest line first, the service name and the truncated notice; the selector refetches", async () => {
    const api = installFetch(base({ [`GET ${U}/system/logs`]: () => ({ status: 200, body: { log: "old\nnew\n", truncated: true, service: "auto-deploy.service" } }) }));
    mount(<SystemLogsView />);
    const pre = await screen.findByTestId("system-logs-text");
    await waitFor(() => expect(pre.textContent).toBe("new\nold"));
    expect(screen.getByText("Service: auto-deploy.service")).toBeInTheDocument();
    expect(await screen.findByTestId("logs-truncated")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Tampilkan"), { target: { value: "500" } });
    await waitFor(() => expect(api.mock.mock.calls.some((c) => String(c[0]).includes("/system/logs?lines=500"))).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Muat ulang" }));
  });

  it("a failed load shows a localized error, never the raw server text", async () => {
    installFetch(base({ [`GET ${U}/system/logs`]: () => ({ status: 500, body: errorBody("internal_error") }) }));
    mount(<SystemLogsView />);
    expect(await screen.findByText("Log sistem tidak dapat dimuat.")).toBeInTheDocument();
    expect(screen.getByText("Terjadi kesalahan di server.")).toBeInTheDocument();
    expect(screen.queryByText("english server text")).toBeNull();
  });
});

describe("GitHub settings (B27, FR-029)", () => {
  const cfg = (over: object = {}) => ({ config: { token: "****wxyz", hasToken: true, source: "database", ...over } });

  it("shows the masked token as status only and never puts it into the input", async () => {
    installFetch(base({ [`GET ${U}/github`]: () => ({ status: 200, body: cfg() }) }));
    mount(<GitHubSettings />);
    expect(await screen.findByText("Token tersimpan: ****wxyz")).toBeInTheDocument();
    expect(screen.getByLabelText("Token baru")).toHaveValue("");
  });

  it("saving without typing a token sends nothing (masked value is never sent back)", async () => {
    const api = installFetch(base({ [`GET ${U}/github`]: () => ({ status: 200, body: cfg() }) }));
    mount(<GitHubSettings />);
    await screen.findByText("Token tersimpan: ****wxyz");
    fireEvent.click(screen.getByRole("button", { name: "Simpan token" }));
    expect(await screen.findByText("Isi token baru terlebih dahulu.")).toBeInTheDocument();
    expect(api.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("a typed token is sent once and the input is cleared; the status follows the response", async () => {
    let stored = cfg();
    const api = installFetch(base({ [`GET ${U}/github`]: () => ({ status: 200, body: stored }), [`POST ${U}/github`]: () => { stored = cfg({ token: "****1234" }); return { status: 200, body: { status: "updated", ...stored } }; } }));
    mount(<GitHubSettings />);
    await screen.findByText("Token tersimpan: ****wxyz");
    fireEvent.change(screen.getByLabelText("Token baru"), { target: { value: "ghp_new1234" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan token" }));
    expect(await screen.findByText("Token GitHub disimpan.")).toBeInTheDocument();
    expect(bodies(api, "POST", `${U}/github`)).toEqual([{ token: "ghp_new1234" }]);
    expect(screen.getByLabelText("Token baru")).toHaveValue("");
    expect(await screen.findByText("Token tersimpan: ****1234")).toBeInTheDocument();
  });

  it("remove token asks first, then sends clear:true (and no token)", async () => {
    const api = installFetch(base({ [`GET ${U}/github`]: () => ({ status: 200, body: cfg() }), [`POST ${U}/github`]: () => ({ status: 200, body: { status: "updated", ...cfg({ token: "", hasToken: false }) } }) }));
    mount(<GitHubSettings />);
    await screen.findByText("Token tersimpan: ****wxyz");
    fireEvent.click(screen.getByRole("button", { name: "Hapus token" }));
    expect(await screen.findByText("Hapus token GitHub?")).toBeInTheDocument();
    expect(api.calls.some((c) => c.method === "POST")).toBe(false); // nothing before confirming
    fireEvent.click(screen.getByRole("button", { name: "Ya, hapus token" }));
    expect(await screen.findByText("Token GitHub dihapus.")).toBeInTheDocument();
    expect(bodies(api, "POST", `${U}/github`)).toEqual([{ clear: true }]);
    expect(await screen.findByText("Belum ada token tersimpan.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Hapus token" })).toBeDisabled();
  });
});

describe("Telegram settings (B27, FR-029)", () => {
  const tg = (over: object = {}) => ({ config: { appId: 12345, appHash: "************abcd", botToken: "*****wxyz", chatUsername: "@ops", enabled: true, ...over } });
  const routes = (config = tg(), extra: Record<string, Handler> = {}) => base({ [`GET ${U}/telegram`]: () => ({ status: 200, body: config }), ...extra });

  it("masked secrets are status text only; the inputs start empty", async () => {
    installFetch(routes());
    mount(<TelegramSettings />);
    expect(await screen.findByTestId("tg-app-hash-status")).toHaveTextContent("Tersimpan: ************abcd");
    expect(screen.getByTestId("tg-bot-token-status")).toHaveTextContent("Tersimpan: *****wxyz");
    expect(screen.getByLabelText("App Hash")).toHaveValue("");
    expect(screen.getByLabelText("Bot token")).toHaveValue("");
    expect(screen.getByLabelText("App ID")).toHaveValue(12345);
    expect(screen.getByLabelText("Username chat")).toHaveValue("@ops");
  });

  it("saving with untouched secrets sends no appHash/botToken and no clear flag", async () => {
    const api = installFetch(routes(tg(), { [`POST ${U}/telegram`]: () => ({ status: 200, body: { status: "updated", ...tg() } }) }));
    mount(<TelegramSettings />);
    await screen.findByTestId("tg-app-hash-status");
    fireEvent.change(screen.getByLabelText("Username chat"), { target: { value: "@new_chat" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan pengaturan" }));
    expect(await screen.findByText("Pengaturan Telegram disimpan.")).toBeInTheDocument();
    const [body] = bodies(api, "POST", `${U}/telegram`);
    expect(body).toEqual({ appId: 12345, chatUsername: "@new_chat", enabled: true, clearAppHash: false, clearBotToken: false });
    expect(JSON.stringify(body)).not.toContain("****");
  });

  it("a typed secret is sent; others are kept", async () => {
    const api = installFetch(routes(tg(), { [`POST ${U}/telegram`]: () => ({ status: 200, body: { status: "updated", ...tg({ botToken: "*****9999" }) } }) }));
    mount(<TelegramSettings />);
    await screen.findByTestId("tg-app-hash-status");
    fireEvent.change(screen.getByLabelText("Bot token"), { target: { value: "123:newtoken9999" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan pengaturan" }));
    await screen.findByText("Pengaturan Telegram disimpan.");
    const [body] = bodies(api, "POST", `${U}/telegram`);
    expect(body).toMatchObject({ botToken: "123:newtoken9999", clearBotToken: false });
    expect(body).not.toHaveProperty("appHash");
  });

  it("clear buttons send clearAppHash / clearBotToken (not a generic clear) after a confirmation", async () => {
    const api = installFetch(routes(tg(), { [`POST ${U}/telegram`]: () => ({ status: 200, body: { status: "updated", ...tg({ botToken: "", enabled: false }) } }) }));
    mount(<TelegramSettings />);
    await screen.findByTestId("tg-bot-token-status");
    fireEvent.click(screen.getByRole("button", { name: "Hapus bot token" }));
    expect(await screen.findByText("Hapus bot token?")).toBeInTheDocument();
    expect(api.calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Ya, hapus" }));
    expect(await screen.findByText("bot token dihapus.")).toBeInTheDocument();
    const [body] = bodies(api, "POST", `${U}/telegram`);
    expect(body).toMatchObject({ clearBotToken: true, clearAppHash: false, enabled: false, appId: 12345, chatUsername: "@ops" });
    expect(body).not.toHaveProperty("clear");
    expect(body).not.toHaveProperty("botToken");
    expect(await screen.findByTestId("tg-bot-token-status")).toHaveTextContent("Belum diatur");
  });

  it("enabling without the required values is explained client-side and sends nothing", async () => {
    const api = installFetch(routes(tg({ appId: 0, appHash: "", botToken: "", chatUsername: "", enabled: false })));
    mount(<TelegramSettings />);
    await screen.findByTestId("tg-app-hash-status");
    fireEvent.click(screen.getByRole("switch", { name: "Aktifkan notifikasi" }));
    fireEvent.click(screen.getByRole("button", { name: "Simpan pengaturan" }));
    expect((await screen.findAllByText("Wajib diisi saat notifikasi diaktifkan.")).length).toBe(4);
    expect(screen.getByLabelText("App ID")).toHaveAttribute("aria-invalid", "true");
    expect(api.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("a server error is shown localized and the typed values stay", async () => {
    installFetch(routes(tg(), { [`POST ${U}/telegram`]: () => ({ status: 400, body: errorBody("validation_failed") }) }));
    mount(<TelegramSettings />);
    await screen.findByTestId("tg-app-hash-status");
    fireEvent.change(screen.getByLabelText("Bot token"), { target: { value: "typed" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan pengaturan" }));
    expect(await screen.findByText("Data tidak valid. Periksa kolom yang ditandai.")).toBeInTheDocument();
    expect(screen.getByLabelText("Bot token")).toHaveValue("typed");
  });
});
