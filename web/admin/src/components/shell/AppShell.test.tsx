// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetAuthStateForTests } from "@/lib/api/client";
import { AppShell } from "./AppShell";
import { SessionGate } from "./SessionGate";
import { FakeWebSocket, Providers, SESSION, STATS, errorBody, installFakeWebSocket, installFetch } from "./testUtils";

const nav = vi.hoisted(() => ({ pathname: "/", push: vi.fn() }));
vi.mock("next/navigation", () => ({
  usePathname: () => nav.pathname,
  useRouter: () => ({ push: nav.push, replace: vi.fn(), prefetch: vi.fn() }),
}));

function mountShell(pathname = "/") {
  nav.pathname = pathname;
  const view = render(
    <Providers>
      <SessionGate>
        <AppShell>
          <p data-testid="page">halaman</p>
        </AppShell>
      </SessionGate>
    </Providers>,
  );
  return view;
}

const okRoutes = () => ({
  "GET /admin/api/v1/session": () => ({ status: 200, body: SESSION }),
  "GET /admin/api/v1/stats": () => ({ status: 200, body: STATS }),
});

const sendEvent = (frame: object) => act(() => FakeWebSocket.instances[0].send(frame));

beforeEach(() => {
  resetAuthStateForTests();
  installFakeWebSocket();
  nav.push.mockClear();
  window.localStorage.setItem("ad.locale", "id"); // jsdom reports en-US; the tests read Indonesian
  document.documentElement.className = "";
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("AppShell structure", () => {
  it("renders landmarks, the skip link and the page content only after the session is known", async () => {
    installFetch(okRoutes());
    mountShell();
    expect(screen.queryByTestId("admin-shell")).toBeNull();
    expect(await screen.findByTestId("admin-shell")).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Navigasi utama" })).toBeInTheDocument();
    expect(screen.getByRole("banner")).toBeInTheDocument();
    const main = screen.getByRole("main");
    expect(main).toHaveAttribute("id", "main-content");
    expect(within(main).getByTestId("page")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Lewati ke konten utama" })).toHaveAttribute("href", "#main-content");
  });

  it("the sidebar links every section and marks the current one", async () => {
    installFetch(okRoutes());
    mountShell("/apps/edit/");
    await screen.findByTestId("admin-shell");
    const nav_ = screen.getByRole("navigation", { name: "Navigasi utama" });
    // next/link applies `trailingSlash` from the build config; outside a Next build the slash is
    // dropped, so compare without it (the built HTML is checked for the slash separately).
    const norm = (h: string | null) => (h ?? "").replace(/(.)\/$/, "$1");
    const hrefs = within(nav_).getAllByRole("link").map((a) => [a.getAttribute("aria-label"), norm(a.getAttribute("href"))]);
    expect(hrefs).toEqual([
      ["Dasbor", "/"],
      ["Aplikasi", "/apps"],
      ["Riwayat", "/history"],
      ["GitHub", "/settings/github"],
      ["Telegram", "/settings/telegram"],
      ["Log sistem", "/logs"],
      ["Akun", "/account"],
    ]);
    const current = within(nav_).getAllByRole("link").filter((a) => a.getAttribute("aria-current") === "page");
    expect(current.map((a) => a.getAttribute("aria-label"))).toEqual(["Aplikasi"]);
  });

  it("marks the dashboard only on the root path", async () => {
    installFetch(okRoutes());
    mountShell("/");
    await screen.findByTestId("admin-shell");
    expect(screen.getByRole("link", { name: "Dasbor" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Aplikasi" })).not.toHaveAttribute("aria-current");
  });
});

describe("Header", () => {
  it("greets the user and shows the live counts from /stats", async () => {
    installFetch(okRoutes());
    mountShell();
    expect(await screen.findByTestId("greeting")).toHaveTextContent("Halo, budi");
    await waitFor(() => expect(screen.getByTestId("subtitle")).toHaveTextContent("3 app aktif · 2 berjalan"));
  });

  it("a failing /stats does not break the shell", async () => {
    installFetch({ ...okRoutes(), "GET /admin/api/v1/stats": () => ({ status: 500, body: errorBody("internal_error") }) });
    mountShell();
    await waitFor(() => expect(screen.getByTestId("subtitle")).toHaveTextContent("Statistik tidak dapat dimuat."));
    expect(screen.getByTestId("page")).toBeInTheDocument();
  });

  it("searching goes to the apps page with the term; an empty search goes to the plain list", async () => {
    installFetch(okRoutes());
    mountShell();
    const input = await screen.findByRole("searchbox", { name: "Cari app" });
    fireEvent.change(input, { target: { value: "  demo app " } });
    fireEvent.submit(input.closest("form") as HTMLFormElement);
    expect(nav.push).toHaveBeenLastCalledWith("/apps/?q=demo%20app");
    fireEvent.change(input, { target: { value: "" } });
    fireEvent.submit(input.closest("form") as HTMLFormElement);
    expect(nav.push).toHaveBeenLastCalledWith("/apps/");
  });

  it("shows the socket state to assistive technology", async () => {
    installFetch(okRoutes());
    mountShell();
    await screen.findByTestId("admin-shell");
    expect(screen.getByText("Terputus")).toBeInTheDocument();
    await sendEvent({ protocol: "2", id: "evt-1", type: "hello", ts: "2026-11-25T00:00:00Z", payload: { serverVersion: "v1" } });
    expect(screen.getByText("Terhubung")).toBeInTheDocument();
  });
});

describe("icon-only controls (visual review item 8)", () => {
  it("show their text label as a tooltip on hover and on keyboard focus, and Escape closes it", async () => {
    installFetch(okRoutes());
    mountShell();
    await screen.findByTestId("admin-shell");
    expect(screen.queryByTestId("rail-tip")).toBeNull();

    const link = screen.getByRole("link", { name: "Riwayat" });
    expect(link).not.toHaveAttribute("title"); // no second, native tooltip
    fireEvent.mouseEnter(link.parentElement as HTMLElement);
    expect(screen.getByTestId("rail-tip")).toHaveTextContent("Riwayat");
    expect(screen.getByTestId("rail-tip")).toHaveAttribute("aria-hidden", "true"); // the control's own name is announced
    fireEvent.mouseLeave(link.parentElement as HTMLElement);
    expect(screen.queryByTestId("rail-tip")).toBeNull();

    fireEvent.focus(link);
    expect(screen.getByTestId("rail-tip")).toHaveTextContent("Riwayat");
    fireEvent.keyDown(link, { key: "Escape" });
    expect(screen.queryByTestId("rail-tip")).toBeNull();

    fireEvent.focus(screen.getByRole("button", { name: "Keluar" }));
    expect(screen.getByTestId("rail-tip")).toHaveTextContent("Keluar");
    fireEvent.blur(screen.getByRole("button", { name: "Keluar" }));
    expect(screen.queryByTestId("rail-tip")).toBeNull();
  });

  it("the language buttons' accessible names START with their visible text (WCAG 2.5.3 label in name)", async () => {
    installFetch(okRoutes());
    mountShell();
    await screen.findByTestId("admin-shell");
    expect(screen.getByRole("button", { name: "ID Bahasa Indonesia" })).toHaveTextContent(/^ID/);
    expect(screen.getByRole("button", { name: "EN English" })).toHaveTextContent(/^EN/);
  });

  it("the theme toggle names the theme it switches to in its tooltip", async () => {
    installFetch(okRoutes());
    mountShell();
    const toggle = await screen.findByRole("button", { name: "Ganti tema" });
    fireEvent.focus(toggle);
    expect(screen.getByTestId("rail-tip")).toHaveTextContent("Gelap");
  });

  it("the connection dot has a text label (visible on wide screens, always readable by assistive technology)", async () => {
    installFetch(okRoutes());
    mountShell();
    await screen.findByTestId("admin-shell");
    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("Terputus");
    expect(status).toHaveAttribute("title", "Terputus");
  });
});

describe("theme and language", () => {
  it("the theme toggle switches the dark class on <html>", async () => {
    installFetch(okRoutes());
    mountShell();
    const toggle = await screen.findByRole("button", { name: "Ganti tema" });
    fireEvent.click(toggle);
    await waitFor(() => expect(document.documentElement.classList.contains("dark")).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Ganti tema" }));
    await waitFor(() => expect(document.documentElement.classList.contains("dark")).toBe(false));
  });

  it("the language toggle re-renders every string, stores the choice and sets <html lang>", async () => {
    installFetch(okRoutes());
    mountShell();
    await screen.findByTestId("admin-shell");
    expect(screen.getByRole("link", { name: "Riwayat" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /English/ }));
    expect(screen.getByRole("link", { name: "History" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Main navigation" })).toBeInTheDocument();
    expect(await screen.findByText("Hello, budi")).toBeInTheDocument();
    expect(window.localStorage.getItem("ad.locale")).toBe("en");
    expect(document.documentElement.lang).toBe("en");
    expect(screen.getByRole("button", { name: /English/ })).toHaveAttribute("aria-pressed", "true");

    fireEvent.click(screen.getByRole("button", { name: /Bahasa Indonesia/ }));
    expect(screen.getByRole("link", { name: "Riwayat" })).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("id");
  });
});

describe("notifications", () => {
  it("collects job results of this session, lists them localized, dismisses and clears", async () => {
    installFetch(okRoutes());
    mountShell();
    await screen.findByTestId("admin-shell");
    const bell = () => screen.getByRole("button", { name: /^Notifikasi/ });
    expect(bell()).toHaveAccessibleName("Notifikasi");

    await sendEvent({ protocol: "2", id: "e1", type: "job_status", ts: "2026-11-25T01:00:00Z", appId: "a1", jobId: "j1", payload: { status: "failed", tag: "v1.2.3" } });
    await sendEvent({ protocol: "2", id: "e2", type: "job_status", ts: "2026-11-25T01:01:00Z", appId: "a1", jobId: "j2", payload: { status: "succeeded", tag: "v1.2.4" } });
    expect(bell()).toHaveAccessibleName("Notifikasi: 2 notifikasi belum dibaca");

    fireEvent.click(bell());
    const list = await screen.findByTestId("notification-list");
    const items = within(list).getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("Deploy v1.2.4 berhasil"); // newest first
    expect(items[1]).toHaveTextContent("Deploy v1.2.3 gagal");

    fireEvent.click(within(items[0]).getByRole("button", { name: "Hapus notifikasi" }));
    expect(within(screen.getByTestId("notification-list")).getAllByRole("listitem")).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "Hapus semua" }));
    expect(await screen.findByText("Belum ada notifikasi pada sesi ini.")).toBeInTheDocument();
  });

  it("a server notice without text falls back to a localized label", async () => {
    installFetch(okRoutes());
    mountShell();
    await screen.findByTestId("admin-shell");
    await sendEvent({ protocol: "2", id: "e3", type: "notice", ts: "2026-11-25T01:00:00Z", payload: { level: "warning", code: "x" } });
    fireEvent.click(screen.getByRole("button", { name: /^Notifikasi/ }));
    expect(await screen.findByText("Pemberitahuan sistem")).toBeInTheDocument();
  });

  it("another protocol version raises a reload banner and a notification", async () => {
    installFetch(okRoutes());
    mountShell();
    await screen.findByTestId("admin-shell");
    await sendEvent({ protocol: "v1", type: "hello" });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Halaman admin ini versi lama. Muat ulang halaman.");
    expect(within(alert).getByRole("button", { name: "Muat ulang halaman" })).toBeInTheDocument();
  });
});

describe("logout", () => {
  it("revokes the session on the server (CSRF header), clears the cache and goes to the login page", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, pathname: "/admin/", search: "", hash: "", assign, replace: vi.fn() });
    const api = installFetch({ ...okRoutes(), "DELETE /admin/api/v1/session": () => ({ status: 204 }) });
    mountShell();
    await screen.findByTestId("admin-shell");
    fireEvent.click(screen.getByRole("button", { name: "Keluar" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/admin/login/"));
    expect(api.count("DELETE", "/admin/api/v1/session")).toBe(1);
    expect(assign).toHaveBeenCalledTimes(1);
  });

  it("a failed logout stays on the page and says so", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, pathname: "/admin/", search: "", hash: "", assign, replace: vi.fn() });
    installFetch({ ...okRoutes(), "DELETE /admin/api/v1/session": () => ({ status: 500, body: errorBody("internal_error") }) });
    mountShell();
    await screen.findByTestId("admin-shell");
    fireEvent.click(screen.getByRole("button", { name: "Keluar" }));
    expect(await screen.findByText("Gagal keluar. Coba lagi.")).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
  });
});
