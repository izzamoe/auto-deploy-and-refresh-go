// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SessionGate } from "@/components/shell/SessionGate";
import { FakeWebSocket, Providers, SESSION, errorBody, installFakeWebSocket, installFetch } from "@/components/shell/testUtils";
import type { Handler } from "@/components/shell/testUtils";
import { resetAuthStateForTests } from "@/lib/api/client";
import { AppForm } from "./AppForm";

const push = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({
  usePathname: () => "/apps/edit",
  useRouter: () => ({ push, replace: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

const APP = { id: "a1", name: "web-api", serviceName: "web-api.service", githubRepo: "acme/web-api", binaryPath: "/opt/web-api/bin", artifactName: "web-api-linux", enabled: true, runAsUser: "", hardening: false, healthUrl: "", requireDigest: false, isSelf: false, createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z" };
const U = "/admin/api/v1";

function routes(extra: Record<string, Handler> = {}): Record<string, Handler> {
  return {
    [`GET ${U}/session`]: () => ({ status: 200, body: SESSION }),
    [`GET ${U}/stats`]: () => ({ status: 200, body: { generatedAt: "", range: "7d", tz: "UTC", apps: { total: 1, enabled: 1 }, deploys24h: { total: 0, succeeded: 0, failed: 0, canceled: 0 }, active: { running: 0, queued: 0 }, successRate: { succeeded: 0, total: 0, rate: null }, daily: [], rollbacks: { count: 0 }, recent: [] } }),
    [`GET ${U}/apps/a1`]: () => ({ status: 200, body: { app: APP } }),
    [`GET ${U}/apps/a1/env`]: () => ({ status: 200, body: { envVars: [{ name: "LOG", value: "info" }] } }),
    [`GET ${U}/apps/a1/args`]: () => ({ status: 200, body: { args: ["--port", "8080"] } }),
    [`PUT ${U}/apps/a1`]: () => ({ status: 200, body: { status: "updated", app: APP } }),
    [`PUT ${U}/apps/a1/env`]: () => ({ status: 200, body: { status: "ok", envVars: [] } }),
    [`PUT ${U}/apps/a1/args`]: () => ({ status: 200, body: { status: "ok", args: [] } }),
    ...extra,
  };
}

function mount(appId?: string) {
  return render(
    <Providers>
      <SessionGate>
        <AppForm appId={appId} />
      </SessionGate>
    </Providers>,
  );
}

const edit = async () => {
  mount("a1");
  return screen.findByLabelText("Nama aplikasi");
};
const puts = (api: ReturnType<typeof installFetch>, suffix: string) => api.calls.filter((c) => c.method === "PUT" && c.path === `${U}/apps/a1${suffix}`);

function fillCreate() {
  fireEvent.change(screen.getByLabelText("Nama aplikasi"), { target: { value: "my-app" } });
  fireEvent.change(screen.getByLabelText("Nama service"), { target: { value: "my-app.service" } });
  fireEvent.change(screen.getByLabelText("Path binary"), { target: { value: "/opt/my-app/bin" } });
  fireEvent.change(screen.getByLabelText("Repo GitHub"), { target: { value: "owner/repo" } });
  fireEvent.change(screen.getByLabelText("Nama artefak"), { target: { value: "my-app-linux-amd64" } });
}

beforeEach(() => {
  resetAuthStateForTests();
  installFakeWebSocket();
  push.mockClear();
  window.localStorage.setItem("ad.locale", "id");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("AppForm create (B55, FR-028, FR-076)", () => {
  it("creates with ONE atomic POST carrying env + args, and shows the secret once", async () => {
    const api = installFetch(routes({ [`POST ${U}/apps`]: () => ({ status: 201, body: { status: "created", app: { ...APP, id: "n1", name: "my-app" }, webhookSecret: "S".repeat(40) } }) }));
    mount();
    await screen.findByLabelText("Nama aplikasi");
    fillCreate();
    fireEvent.change(screen.getByLabelText("Variabel lingkungan"), { target: { value: "A=1\nB=two" } });
    fireEvent.change(screen.getByLabelText("Argumen baris perintah"), { target: { value: '--port 8080 --msg "hi there"' } });
    fireEvent.click(screen.getByRole("button", { name: "Buat aplikasi" }));

    expect(await screen.findByTestId("webhook-snippet")).toBeInTheDocument();
    const posts = api.calls.filter((c) => c.method === "POST" && c.path === `${U}/apps`);
    expect(posts).toHaveLength(1);
    expect(posts[0].body).toMatchObject({ name: "my-app", githubRepo: "owner/repo", envVars: [{ name: "A", value: "1" }, { name: "B", value: "two" }], args: ["--port", "8080", "--msg", "hi there"] });
    expect(posts[0].body).not.toHaveProperty("webhookSecret"); // empty = the server generates (FR-055)
    expect(api.calls.some((c) => c.method === "PUT")).toBe(false); // no follow-up env/args calls (B55)
    expect(screen.getByTestId("webhook-secret-value")).toHaveTextContent("S".repeat(40));
    expect(screen.getByTestId("webhook-curl-snippet").textContent).toContain(`Authorization: Bearer ${"S".repeat(40)}`);
    expect(screen.getByTestId("webhook-gha-snippet").textContent).not.toContain("SSSS");
    expect(push).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Selesai" }));
    expect(push).toHaveBeenCalledWith("/apps/");
  });

  it("a failed create leaves no ambiguous state: still the form, nothing else was called", async () => {
    const api = installFetch(routes({ [`POST ${U}/apps`]: () => ({ status: 409, body: errorBody("duplicate") }) }));
    mount();
    await screen.findByLabelText("Nama aplikasi");
    fillCreate();
    fireEvent.change(screen.getByLabelText("Variabel lingkungan"), { target: { value: "A=1" } });
    fireEvent.click(screen.getByRole("button", { name: "Buat aplikasi" }));
    expect(await screen.findByText("Data dengan nilai yang sama sudah ada.")).toBeInTheDocument();
    expect(screen.queryByTestId("webhook-snippet")).toBeNull();
    expect(api.calls.filter((c) => c.method !== "GET")).toHaveLength(1); // the single POST
    expect(screen.getByLabelText("Nama aplikasi")).toHaveValue("my-app"); // typed values kept
  });

  it("shows per-field errors from errors[] (localized) linked with aria-invalid/aria-describedby", async () => {
    installFetch(
      routes({
        [`POST ${U}/apps`]: () => ({
          status: 400,
          body: { status: "error", code: "validation_failed", error: "Validation failed", errors: [{ field: "binaryPath", code: "validation_failed", message: "Binary path must not contain spaces" }, { field: "healthUrl", code: "validation_failed", message: "x" }] },
        }),
      }),
    );
    mount();
    await screen.findByLabelText("Nama aplikasi");
    fillCreate();
    fireEvent.click(screen.getByRole("button", { name: "Buat aplikasi" }));
    const binary = await screen.findByLabelText("Path binary");
    await waitFor(() => expect(binary).toHaveAttribute("aria-invalid", "true"));
    expect(binary.getAttribute("aria-describedby")).toContain("f-binary-error");
    expect(screen.getByText("Harus path absolut tanpa spasi, %, $, atau backslash, dan tidak di direktori sistem.")).toBeInTheDocument();
    expect(screen.getByText("URL health check tidak valid.")).toBeInTheDocument();
    expect(screen.queryByText(/must not contain spaces/)).toBeNull(); // raw server English is never shown
    expect(screen.getByLabelText("Nama aplikasi")).not.toHaveAttribute("aria-invalid");
    const summary = screen.getByTestId("form-errors");
    expect(within(summary).getByRole("link", { name: "Path binary" })).toHaveAttribute("href", "#f-binary");
  });

  it("validates required fields and the repo format before any request", async () => {
    const api = installFetch(routes());
    mount();
    await screen.findByLabelText("Nama aplikasi");
    fireEvent.change(screen.getByLabelText("Repo GitHub"), { target: { value: "not a repo" } });
    fireEvent.click(screen.getByRole("button", { name: "Buat aplikasi" }));
    expect(await screen.findAllByText("Wajib diisi.")).not.toHaveLength(0);
    expect(screen.getByText("Gunakan format pemilik/repo.")).toBeInTheDocument();
    expect(api.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("rejects an invalid env variable name client-side", async () => {
    const api = installFetch(routes());
    mount();
    await screen.findByLabelText("Nama aplikasi");
    fillCreate();
    fireEvent.change(screen.getByLabelText("Variabel lingkungan"), { target: { value: "1BAD=x" } });
    fireEvent.click(screen.getByRole("button", { name: "Buat aplikasi" }));
    expect(await screen.findByText("Nama variabel tidak valid: 1BAD")).toBeInTheDocument();
    expect(api.calls.some((c) => c.method === "POST")).toBe(false);
  });
});

describe("AppForm edit (B53, B54, FR-074)", () => {
  it("B53: when GET env fails the editor is disabled and PUT env is NEVER sent", async () => {
    const api = installFetch(routes({ [`GET ${U}/apps/a1/env`]: () => ({ status: 500, body: errorBody("internal_error") }) }));
    const name = await edit();
    const env = await screen.findByLabelText("Variabel lingkungan");
    await waitFor(() => expect(screen.getByTestId("env-vars-editor")).toHaveAttribute("data-status", "error"));
    expect(env).toBeDisabled();
    expect(screen.getByText("Variabel lingkungan tidak dapat dimuat, jadi tidak akan diubah.")).toBeInTheDocument();
    fireEvent.change(name, { target: { value: "web-api-2" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan perubahan" }));
    await waitFor(() => expect(puts(api, "")).toHaveLength(1));
    expect(puts(api, "")[0].body).toMatchObject({ name: "web-api-2" });
    expect(puts(api, "/env")).toHaveLength(0);
    expect(await screen.findByText("Aplikasi diperbarui.")).toBeInTheDocument();
  });

  it("an unchanged env/args text sends no PUT for them; only the app is saved", async () => {
    const api = installFetch(routes());
    const name = await edit();
    await waitFor(() => expect(screen.getByLabelText("Variabel lingkungan")).toHaveValue("LOG=info"));
    await waitFor(() => expect(screen.getByLabelText("Argumen baris perintah")).toHaveValue("--port\n8080"));
    fireEvent.change(name, { target: { value: "renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan perubahan" }));
    await waitFor(() => expect(puts(api, "")).toHaveLength(1));
    await screen.findByText("Aplikasi diperbarui.");
    expect(puts(api, "/env")).toHaveLength(0);
    expect(puts(api, "/args")).toHaveLength(0);
    // A cosmetic change (extra blank line / comment) is not a change either.
    fireEvent.change(screen.getByLabelText("Variabel lingkungan"), { target: { value: "LOG=info\n\n# note\n" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan perubahan" }));
    await waitFor(() => expect(puts(api, "")).toHaveLength(2));
    expect(puts(api, "/env")).toHaveLength(0);
  });

  it("a changed env is sent once, after the app, and is not re-sent on the next save", async () => {
    const api = installFetch(routes());
    await edit();
    await waitFor(() => expect(screen.getByLabelText("Variabel lingkungan")).toHaveValue("LOG=info"));
    fireEvent.change(screen.getByLabelText("Variabel lingkungan"), { target: { value: "LOG=debug\nNEW=1" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan perubahan" }));
    await waitFor(() => expect(puts(api, "/env")).toHaveLength(1));
    expect(puts(api, "/env")[0].body).toEqual({ envVars: [{ name: "LOG", value: "debug" }, { name: "NEW", value: "1" }] });
    expect(puts(api, "/args")).toHaveLength(0);
    await screen.findByText("Aplikasi diperbarui.");
    fireEvent.click(screen.getByRole("button", { name: "Simpan perubahan" }));
    await waitFor(() => expect(puts(api, "")).toHaveLength(2));
    expect(puts(api, "/env")).toHaveLength(1);
  });

  it("when PUT env fails after the app was saved it says exactly that and keeps the env dirty for a retry", async () => {
    let fail = true;
    const api = installFetch(routes({ [`PUT ${U}/apps/a1/env`]: () => (fail ? { status: 400, body: errorBody("validation_failed") } : { status: 200, body: { status: "ok", envVars: [] } }) }));
    await edit();
    await waitFor(() => expect(screen.getByLabelText("Variabel lingkungan")).toHaveValue("LOG=info"));
    fireEvent.change(screen.getByLabelText("Variabel lingkungan"), { target: { value: "LOG=x" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan perubahan" }));
    expect(await screen.findByText(/Aplikasi tersimpan, tetapi variabel lingkungan gagal disimpan/)).toBeInTheDocument();
    expect(screen.queryByText("Aplikasi diperbarui.")).toBeNull();
    expect(screen.getByLabelText("Variabel lingkungan")).toHaveValue("LOG=x"); // typing kept
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Simpan perubahan" }));
    await waitFor(() => expect(puts(api, "/env")).toHaveLength(2)); // retried because still dirty
  });

  it("B54: a notification/event does not reset what the user typed", async () => {
    installFetch(routes());
    const name = await edit();
    fireEvent.change(name, { target: { value: "typing-in-progress" } });
    fireEvent.change(screen.getByLabelText("Webhook secret"), { target: { value: "x".repeat(33) } });
    act(() => FakeWebSocket.instances[0].emit({ protocol: "3", id: "evt-1", seq: 1, topic: "jobs", type: "job_status", ts: new Date().toISOString(), appId: "a1", jobId: "j9", payload: { status: "succeeded", previousStatus: "in_progress", tag: "v1" } }));
    act(() => FakeWebSocket.instances[0].emit({ protocol: "3", id: "evt-2", seq: 2, topic: "summary", type: "notice", ts: new Date().toISOString(), payload: { level: "info", code: "x", message: "m" } }));
    await act(async () => {
      window.dispatchEvent(new Event("focus")); // window refocus refetch must not clobber the form either
    });
    expect(screen.getByLabelText("Nama aplikasi")).toHaveValue("typing-in-progress");
    expect(screen.getByLabelText("Webhook secret")).toHaveValue("x".repeat(33));
  });

  it("the webhook reference uses a placeholder until a new secret is typed, then that secret", async () => {
    installFetch(routes());
    await edit();
    const ref = await screen.findByTestId("webhook-ref-curl-snippet");
    expect(ref.textContent).toContain("<secret-webhook-anda>");
    fireEvent.change(screen.getByLabelText("Webhook secret"), { target: { value: "n".repeat(40) } });
    expect(screen.getByTestId("webhook-ref-curl-snippet").textContent).toContain("n".repeat(40));
  });

  it("an unknown app shows 'not found' with a way back", async () => {
    installFetch(routes({ [`GET ${U}/apps/a1`]: () => ({ status: 404, body: errorBody("not_found") }) }));
    mount("a1");
    expect(await screen.findByText("Aplikasi tidak ditemukan.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Kembali ke daftar aplikasi" })).toBeInTheDocument();
  });
});
