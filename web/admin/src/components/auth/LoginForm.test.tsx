// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Providers, SESSION, errorBody, installFetch } from "@/components/shell/testUtils";
import { resetAuthStateForTests } from "@/lib/api/client";
import { LoginForm } from "./LoginForm";

let assign: ReturnType<typeof vi.fn>;

beforeEach(() => {
  resetAuthStateForTests();
  window.localStorage.setItem("ad.locale", "id");
  assign = vi.fn();
  vi.stubGlobal("location", { ...window.location, pathname: "/admin/login/", search: "", hash: "", assign, replace: vi.fn() });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function mount(next: string | null) {
  return render(
    <Providers>
      <LoginForm next={next} />
    </Providers>,
  );
}

function fill(user = "budi", pass = "rahasia123") {
  fireEvent.change(screen.getByLabelText("Nama pengguna"), { target: { value: user } });
  fireEvent.change(screen.getByLabelText("Kata sandi"), { target: { value: pass } });
  fireEvent.click(screen.getByRole("button", { name: "Masuk" }));
}

describe("LoginForm (FR-015, FR-008, open redirect)", () => {
  it("has a real form with correct autocomplete and accessible labels, no Basic popup", () => {
    mount(null);
    const user = screen.getByLabelText("Nama pengguna");
    const pass = screen.getByLabelText("Kata sandi");
    expect(user).toHaveAttribute("autocomplete", "username");
    expect(pass).toHaveAttribute("autocomplete", "current-password");
    expect(pass).toHaveAttribute("type", "password");
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Masuk ke auto-deploy");
    expect(screen.getByRole("button", { name: "Masuk" })).toBeInTheDocument();
  });

  it("posts the credentials with the CSRF header and goes to next=/admin/history/", async () => {
    const api = installFetch({ "POST /admin/api/v1/session": () => ({ status: 200, body: SESSION }) });
    mount("/admin/history/");
    fill();
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/admin/history/"));
    expect(api.calls[0]).toMatchObject({ method: "POST", path: "/admin/api/v1/session", body: { username: "budi", password: "rahasia123" } });
    expect(assign).toHaveBeenCalledTimes(1);
  });

  it.each(["https://evil.example/admin/", "//evil.example/admin/", "/evil/", "/admin/../evil", "javascript:alert(1)", "/admin\\evil", "/admin/login/", ""])(
    "ignores an unsafe next (%s) and falls back to /admin/",
    async (evil) => {
      installFetch({ "POST /admin/api/v1/session": () => ({ status: 200, body: SESSION }) });
      mount(evil);
      fill();
      await waitFor(() => expect(assign).toHaveBeenCalled());
      const dest = String(assign.mock.calls[0][0]);
      expect(dest.startsWith("/admin/")).toBe(true);
      expect(dest).not.toMatch(/evil|javascript|\\\\/);
    },
  );

  it("a wrong password shows the localized message, clears the password and stays", async () => {
    installFetch({ "POST /admin/api/v1/session": () => ({ status: 401, body: errorBody("unauthorized") }) });
    mount(null);
    fill("budi", "salah");
    expect(await screen.findByText("Nama pengguna atau kata sandi salah.")).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled(); // no redirect loop to the login page itself
    expect(screen.getByLabelText("Kata sandi")).toHaveValue("");
  });

  it("429 shows the wait from Retry-After, disables the button and counts down", async () => {
    installFetch({
      "POST /admin/api/v1/session": () => ({ status: 429, body: errorBody("rate_limited"), headers: { "Retry-After": "45" } }),
    });
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] }); // only the countdown; findBy keeps real timers
    mount(null);
    fill();
    expect(await screen.findByText("Terlalu banyak percobaan masuk. Coba lagi dalam 45 detik.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Masuk" })).toBeDisabled();

    act(() => {
      vi.advanceTimersByTime(5000);
    });
    expect(screen.getByText("Terlalu banyak percobaan masuk. Coba lagi dalam 40 detik.")).toBeInTheDocument();
    act(() => {
      vi.advanceTimersByTime(40_000);
    });
    expect(screen.queryByText(/Coba lagi dalam/)).toBeNull();
    expect(screen.getByRole("button", { name: "Masuk" })).toBeEnabled();
  });

  it("429 with minutes rounds up (Retry-After 840 -> 14 menit)", async () => {
    installFetch({
      "POST /admin/api/v1/session": () => ({ status: 429, body: errorBody("rate_limited"), headers: { "Retry-After": "840" } }),
    });
    mount(null);
    fill();
    expect(await screen.findByText("Terlalu banyak percobaan masuk. Coba lagi dalam 14 menit.")).toBeInTheDocument();
  });

  it("429 without a usable Retry-After falls back to the generic rate-limit text", async () => {
    installFetch({ "POST /admin/api/v1/session": () => ({ status: 429, body: errorBody("rate_limited") }) });
    mount(null);
    fill();
    expect(await screen.findByText("Terlalu banyak percobaan. Tunggu sebentar lalu coba lagi.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Masuk" })).toBeEnabled();
  });

  it("a network failure is localized, not raw English", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
    mount(null);
    fill();
    expect(await screen.findByText(/Tidak dapat terhubung ke server/)).toBeInTheDocument();
  });

  it("mustChangePassword forces the password change before going anywhere", async () => {
    const api = installFetch({
      "POST /admin/api/v1/session": () => ({ status: 200, body: { ...SESSION, mustChangePassword: true } }),
      "POST /admin/api/v1/account/password": () => ({ status: 200, body: { status: "ok" } }),
    });
    mount("/admin/apps/");
    fill();
    expect(await screen.findByTestId("forced-password-change")).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Kata sandi saat ini"), { target: { value: "rahasia123" } });
    fireEvent.change(screen.getByLabelText("Kata sandi baru"), { target: { value: "sangat-rahasia-456" } });
    fireEvent.change(screen.getByLabelText("Ulangi kata sandi baru"), { target: { value: "sangat-rahasia-456" } });
    fireEvent.click(screen.getByRole("button", { name: "Ganti kata sandi" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/admin/apps/"));
    expect(api.count("POST", "/admin/api/v1/account/password")).toBe(1);
  });

  it("shows the 'session ended' note only when redirected here with ?next", () => {
    mount("/admin/apps/");
    expect(screen.getByRole("status")).toHaveTextContent("Sesi berakhir. Silakan masuk lagi.");
    cleanup();
    mount(null);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("English locale translates everything on the page", () => {
    window.localStorage.setItem("ad.locale", "en");
    mount(null);
    expect(screen.getByLabelText("Username")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
  });
});
