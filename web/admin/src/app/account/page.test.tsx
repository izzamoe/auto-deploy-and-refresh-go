// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SessionGate } from "@/components/shell/SessionGate";
import { FakeWebSocket, Providers, SESSION, errorBody, goLive, installFakeWebSocket, installFetch } from "@/components/shell/testUtils";
import type { Handler } from "@/components/shell/testUtils";
import { resetAuthStateForTests } from "@/lib/api/client";
import AccountPage from "./page";

vi.mock("next/navigation", () => ({
  usePathname: () => "/account",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

const U = "/admin/api/v1";
const STATS = { generatedAt: "", range: "7d", tz: "UTC", apps: { total: 0, enabled: 0 }, deploys24h: { total: 0, succeeded: 0, failed: 0, canceled: 0 }, active: { running: 0, queued: 0 }, successRate: { succeeded: 0, total: 0, rate: null }, daily: [], rollbacks: { count: 0 }, recent: [] };
const FULL = "adt_" + "f".repeat(40);

let tokens: object[];
function routes(extra: Record<string, Handler> = {}): Record<string, Handler> {
  return {
    [`GET ${U}/session`]: () => ({ status: 200, body: SESSION }),
    [`GET ${U}/stats`]: () => ({ status: 200, body: STATS }),
    [`GET ${U}/tokens`]: () => ({ status: 200, body: { tokens } }),
    ...extra,
  };
}
function mount() {
  return render(
    <Providers>
      <SessionGate>
        <AccountPage />
      </SessionGate>
    </Providers>,
  );
}
const bodiesOf = (api: ReturnType<typeof installFetch>, path: string) => api.calls.filter((c) => c.method === "POST" && c.path === path).map((c) => c.body as Record<string, unknown>);

beforeEach(() => {
  resetAuthStateForTests();
  installFakeWebSocket();
  tokens = [
    { id: "t1", name: "ci", prefix: "adt_ab12", createdAt: "2026-11-01T00:00:00Z", lastUsedAt: null },
    { id: "t2", name: "old", prefix: "adt_cd34", createdAt: "2026-10-01T00:00:00Z", lastUsedAt: "2026-10-05T00:00:00Z", revokedAt: "2026-10-06T00:00:00Z" },
  ];
  window.localStorage.setItem("ad.locale", "id");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Account page (T092, FR-005, FR-014, FR-016)", () => {
  it("shows who is signed in and the session limits", async () => {
    installFetch(routes());
    mount();
    expect(await screen.findByText("Masuk sebagai budi")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Akun" })).toBeInTheDocument();
  });

  it("change password: mismatch is caught before any request; success clears the fields", async () => {
    const api = installFetch(routes({ [`POST ${U}/account/password`]: () => ({ status: 200, body: { status: "ok" } }) }));
    mount();
    const form = await screen.findByTestId("password-form");
    const f = within(form);
    fireEvent.change(f.getByLabelText("Kata sandi saat ini"), { target: { value: "lama-123" } });
    fireEvent.change(f.getByLabelText("Kata sandi baru"), { target: { value: "baru-456-xyz" } });
    fireEvent.change(f.getByLabelText("Ulangi kata sandi baru"), { target: { value: "beda" } });
    fireEvent.click(f.getByRole("button", { name: "Ganti kata sandi" }));
    expect(await f.findByText("Kata sandi baru dan pengulangannya tidak sama.")).toBeInTheDocument();
    expect(bodiesOf(api, `${U}/account/password`)).toHaveLength(0);

    fireEvent.change(f.getByLabelText("Ulangi kata sandi baru"), { target: { value: "baru-456-xyz" } });
    fireEvent.click(f.getByRole("button", { name: "Ganti kata sandi" }));
    expect(await screen.findByText("Kata sandi diganti.")).toBeInTheDocument();
    expect(bodiesOf(api, `${U}/account/password`)).toEqual([{ currentPassword: "lama-123", newPassword: "baru-456-xyz" }]);
    expect(f.getByLabelText("Kata sandi saat ini")).toHaveValue("");
    expect(f.getByLabelText("Kata sandi baru")).toHaveAttribute("autocomplete", "new-password");
  });

  it("a wrong current password shows the localized error", async () => {
    installFetch(routes({ [`POST ${U}/account/password`]: () => ({ status: 401, body: errorBody("unauthorized") }) }));
    mount();
    const f = within(await screen.findByTestId("password-form"));
    fireEvent.change(f.getByLabelText("Kata sandi saat ini"), { target: { value: "salah" } });
    fireEvent.change(f.getByLabelText("Kata sandi baru"), { target: { value: "x" } });
    fireEvent.change(f.getByLabelText("Ulangi kata sandi baru"), { target: { value: "x" } });
    fireEvent.click(f.getByRole("button", { name: "Ganti kata sandi" }));
    expect(await screen.findByText("Anda belum masuk.")).toBeInTheDocument();
  });

  it("change username sends the new name + current password and refreshes the session", async () => {
    const api = installFetch(routes({ [`POST ${U}/account/username`]: () => ({ status: 200, body: { status: "ok" } }) }));
    mount();
    const f = within(await screen.findByTestId("username-form"));
    fireEvent.change(f.getByLabelText("Nama pengguna baru"), { target: { value: "  siti  " } });
    fireEvent.change(f.getByLabelText("Kata sandi saat ini"), { target: { value: "rahasia" } });
    const before = api.count("GET", `${U}/session`);
    fireEvent.click(f.getByRole("button", { name: "Ganti nama pengguna" }));
    expect(await screen.findByText("Nama pengguna diganti.")).toBeInTheDocument();
    expect(bodiesOf(api, `${U}/account/username`)).toEqual([{ currentPassword: "rahasia", newUsername: "siti" }]);
    await waitFor(() => expect(api.count("GET", `${U}/session`)).toBeGreaterThan(before));
  });

  it("a duplicate username (409) is explained, localized", async () => {
    installFetch(routes({ [`POST ${U}/account/username`]: () => ({ status: 409, body: errorBody("duplicate") }) }));
    mount();
    const f = within(await screen.findByTestId("username-form"));
    fireEvent.change(f.getByLabelText("Nama pengguna baru"), { target: { value: "admin" } });
    fireEvent.change(f.getByLabelText("Kata sandi saat ini"), { target: { value: "x" } });
    fireEvent.click(f.getByRole("button", { name: "Ganti nama pengguna" }));
    expect(await screen.findByText("Data dengan nilai yang sama sudah ada.")).toBeInTheDocument();
  });

  it("revoke other sessions asks first, then posts once", async () => {
    const api = installFetch(routes({ [`POST ${U}/account/sessions/revoke-all`]: () => ({ status: 200, body: { status: "ok" } }) }));
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Cabut sesi lain" }));
    expect(await screen.findByText("Cabut semua sesi lain?")).toBeInTheDocument();
    expect(bodiesOf(api, `${U}/account/sessions/revoke-all`)).toHaveLength(0);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Ya, cabut" }));
    expect(await screen.findByText("Sesi lain dicabut.")).toBeInTheDocument();
    expect(bodiesOf(api, `${U}/account/sessions/revoke-all`)).toHaveLength(1);
  });

  it("lists tokens with only the prefix; revoked ones show when and have no revoke button", async () => {
    installFetch(routes());
    mount();
    const table = await screen.findByTestId("tokens-table");
    expect(within(table).getByText("adt_ab12…")).toBeInTheDocument();
    expect(within(table).getByText("Belum pernah")).toBeInTheDocument();
    expect(within(table).getByText("Aktif")).toBeInTheDocument();
    expect(within(screen.getByTestId("token-t2")).queryByRole("button")).toBeNull();
    expect(within(screen.getByTestId("token-t2")).getByText(/^Dicabut /)).toBeInTheDocument();
    expect(within(screen.getByTestId("token-t1")).getByRole("button", { name: "Cabut token ci" })).toBeInTheDocument();
  });

  it("a created token is shown in full ONCE, then only its prefix remains", async () => {
    const api = installFetch(
      routes({
        [`POST ${U}/tokens`]: () => {
          tokens = [...tokens, { id: "t3", name: "deploy", prefix: "adt_ffff", createdAt: "2026-11-25T00:00:00Z" }];
          return { status: 201, body: { id: "t3", name: "deploy", prefix: "adt_ffff", createdAt: "2026-11-25T00:00:00Z", token: FULL } };
        },
      }),
    );
    mount();
    await screen.findByTestId("tokens-table");
    fireEvent.change(screen.getByLabelText("Nama token"), { target: { value: "deploy" } });
    fireEvent.click(screen.getByRole("button", { name: "Buat token" }));
    const fresh = await screen.findByTestId("token-fresh");
    expect(within(fresh).getByTestId("token-fresh-value")).toHaveTextContent(FULL);
    expect(within(fresh).getByText("Salin token ini sekarang; nilai penuhnya tidak akan ditampilkan lagi.")).toBeInTheDocument();
    expect(bodiesOf(api, `${U}/tokens`)).toEqual([{ name: "deploy" }]);
    // the list refetched and shows only the prefix of the new token
    await waitFor(() => expect(screen.getByTestId("token-t3")).toHaveTextContent("adt_ffff…"));
    expect(within(screen.getByTestId("tokens-table")).queryByText(FULL)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Saya sudah menyalinnya" }));
    expect(screen.queryByTestId("token-fresh")).toBeNull();
    expect(document.body.textContent).not.toContain(FULL); // gone everywhere once dismissed
    expect(screen.getByTestId("token-t3")).toHaveTextContent("adt_ffff…");
  });

  it("revoking a token asks first and then calls DELETE for that token", async () => {
    const api = installFetch(routes({ [`DELETE ${U}/tokens/t1`]: () => ({ status: 204 }) }));
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Cabut token ci" }));
    expect(await screen.findByText("Cabut token ci?")).toBeInTheDocument();
    expect(api.count("DELETE", `${U}/tokens/t1`)).toBe(0);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Ya, cabut" }));
    expect(await screen.findByText("Token dicabut.")).toBeInTheDocument();
    expect(api.count("DELETE", `${U}/tokens/t1`)).toBe(1);
  });

  it("an empty list and a failed list are explained", async () => {
    tokens = [];
    installFetch(routes());
    mount();
    expect(await screen.findByTestId("tokens-empty")).toHaveTextContent("Belum ada token API.");
    cleanup();
    installFetch(routes({ [`GET ${U}/tokens`]: () => ({ status: 500, body: errorBody("internal_error") }) }));
    mount();
    expect(await screen.findByText("Daftar token tidak dapat dimuat.")).toBeInTheDocument();
  });
});

describe("account changed in another session (002 T058, FR-017)", () => {
  it("a token created elsewhere appears after account.changed{tokens} with ONE GET /tokens; nothing on focus", async () => {
    const api = installFetch(routes());
    mount();
    await screen.findByTestId("tokens-card");
    await waitFor(() => expect(screen.getByTestId("tokens-card")).toHaveTextContent("ci"));
    await act(async () => goLive(FakeWebSocket.instances[0]));
    expect(FakeWebSocket.instances[0].subscribedTopics()).toContain("account");
    expect(FakeWebSocket.instances[0].subscribedTopics()).not.toContain("jobs"); // SC-011: no progress on /account/
    await act(async () => { window.dispatchEvent(new Event("visibilitychange")); await new Promise((r) => setTimeout(r, 50)); });
    expect(api.count("GET", `${U}/tokens`)).toBe(1);
    tokens = [...tokens, { id: "t3", name: "made-elsewhere", prefix: "adt_ef56", createdAt: "2026-11-25T00:00:00Z", lastUsedAt: null }];
    await act(async () => FakeWebSocket.instances[0].emit({ protocol: "3", id: "evt-4", seq: 4, topic: "account", type: "account.changed", ts: new Date().toISOString(), origin: "c-99999999", payload: { section: "tokens" } }));
    await waitFor(() => expect(screen.getByTestId("tokens-card")).toHaveTextContent("made-elsewhere"));
    expect(api.count("GET", `${U}/tokens`)).toBe(2);
  });
});
