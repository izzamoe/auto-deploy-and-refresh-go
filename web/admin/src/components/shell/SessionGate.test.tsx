// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api as apiClient, resetAuthStateForTests } from "@/lib/api/client";
import { SessionGate, useSession } from "./SessionGate";
import { FakeWebSocket, Providers, SESSION, errorBody, installFakeWebSocket, installFetch } from "./testUtils";

function Data() {
  const s = useSession();
  return <p data-testid="data">{s.username}</p>;
}

function mountGate() {
  return render(
    <Providers>
      <SessionGate>
        <Data />
      </SessionGate>
    </Providers>,
  );
}

beforeEach(() => {
  resetAuthStateForTests();
  installFakeWebSocket();
  window.localStorage.setItem("ad.locale", "id"); // jsdom reports en-US; the tests read Indonesian
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("SessionGate (FR-071, B50, K2)", () => {
  it("renders NO data and opens NO socket while the session is unknown", async () => {
    let release: (r: { status: number; body: unknown }) => void = () => {};
    const gate = new Promise<{ status: number; body: unknown }>((r) => { release = r; });
    const api = installFetch({ "GET /admin/api/v1/session": () => gate });
    mountGate();

    expect(await screen.findByText("Memeriksa sesi…")).toBeInTheDocument();
    expect(screen.queryByTestId("data")).toBeNull();
    expect(FakeWebSocket.instances).toHaveLength(0);
    expect(api.calls.map((c) => c.path)).toEqual(["/admin/api/v1/session"]); // nothing but the probe

    release({ status: 200, body: SESSION });
    expect(await screen.findByTestId("data")).toHaveTextContent("budi");
    expect(FakeWebSocket.instances).toHaveLength(1); // the socket opens only after the session is known
  });

  it("a failed probe (500) shows an error with retry and still renders no data (K2)", async () => {
    let attempt = 0;
    installFetch({
      "GET /admin/api/v1/session": () => (++attempt === 1 ? { status: 500, body: errorBody("internal_error") } : { status: 200, body: SESSION }),
    });
    mountGate();
    expect(await screen.findByText("Sesi tidak dapat diperiksa.")).toBeInTheDocument();
    expect(screen.getByText("Terjadi kesalahan di server.")).toBeInTheDocument();
    expect(screen.queryByTestId("data")).toBeNull();
    expect(FakeWebSocket.instances).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Coba lagi" }));
    expect(await screen.findByTestId("data")).toBeInTheDocument();
  });

  it("a network failure is shown localized (B58)", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("Failed to fetch"))));
    mountGate();
    expect(await screen.findByText("Tidak dapat terhubung ke server. Periksa koneksi lalu coba lagi.")).toBeInTheDocument();
    expect(screen.queryByTestId("data")).toBeNull();
  });

  it("a 401 redirects to the login page once and shows no data and no popup", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, pathname: "/admin/apps/", search: "?q=x", hash: "", assign, replace: vi.fn() });
    const api = installFetch({ "GET /admin/api/v1/session": () => ({ status: 401, body: errorBody("session_expired") }) });
    mountGate();
    expect(await screen.findByText("Mengalihkan ke halaman masuk…")).toBeInTheDocument();
    expect(assign).toHaveBeenCalledTimes(1);
    expect(assign).toHaveBeenCalledWith("/admin/login/?next=" + encodeURIComponent("/admin/apps/?q=x"));
    expect(screen.queryByTestId("data")).toBeNull();
    expect(api.calls).toHaveLength(1);
    expect(FakeWebSocket.instances).toHaveLength(0);
  });

  it("mustChangePassword replaces the whole UI with the change screen; success reveals the app", async () => {
    let changed = false;
    const api = installFetch({
      "GET /admin/api/v1/session": () => ({ status: 200, body: { ...SESSION, mustChangePassword: !changed } }),
      "POST /admin/api/v1/account/password": () => { changed = true; return { status: 200, body: { status: "updated" } }; },
    });
    mountGate();
    expect(await screen.findByTestId("forced-password-change")).toBeInTheDocument();
    expect(screen.queryByTestId("data")).toBeNull();
    expect(FakeWebSocket.instances).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Kata sandi saat ini"), { target: { value: "old-pass" } });
    fireEvent.change(screen.getByLabelText("Kata sandi baru"), { target: { value: "a-very-long-new-pass" } });
    fireEvent.change(screen.getByLabelText("Ulangi kata sandi baru"), { target: { value: "a-very-long-new-pass" } });
    fireEvent.click(screen.getByRole("button", { name: "Ganti kata sandi" }));

    expect(await screen.findByTestId("data")).toBeInTheDocument();
    expect(api.calls.find((c) => c.path === "/admin/api/v1/account/password")?.body).toEqual({
      currentPassword: "old-pass",
      newPassword: "a-very-long-new-pass",
    });
  });

  it("the change screen rejects a mismatched repetition without calling the API", async () => {
    const api = installFetch({ "GET /admin/api/v1/session": () => ({ status: 200, body: { ...SESSION, mustChangePassword: true } }) });
    mountGate();
    await screen.findByTestId("forced-password-change");
    fireEvent.change(screen.getByLabelText("Kata sandi saat ini"), { target: { value: "x" } });
    fireEvent.change(screen.getByLabelText("Kata sandi baru"), { target: { value: "aaaa" } });
    fireEvent.change(screen.getByLabelText("Ulangi kata sandi baru"), { target: { value: "bbbb" } });
    fireEvent.click(screen.getByRole("button", { name: "Ganti kata sandi" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Kata sandi baru dan pengulangannya tidak sama.");
    expect(api.count("POST", "/admin/api/v1/account/password")).toBe(0);
  });

  it("a 403 password_change_required from any data call also shows the change screen", async () => {
    installFetch({
      "GET /admin/api/v1/session": () => ({ status: 200, body: SESSION }),
      "GET /admin/api/v1/apps": () => ({ status: 403, body: errorBody("password_change_required") }),
    });
    mountGate();
    await screen.findByTestId("data");
    await apiClient.listApps().catch(() => {});
    expect(await screen.findByTestId("forced-password-change")).toBeInTheDocument();
    expect(screen.queryByTestId("data")).toBeNull();
  });
});
