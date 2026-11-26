// @vitest-environment jsdom
// 002 T070 / RV-22: a tab left open while a new binary was installed asks for chunk hashes that
// no longer exist (404 -> ChunkLoadError). Opening a lazily loaded dialog must not freeze the
// UI: the dialog stays closed, the trigger keeps working, and the shell shows the existing
// "this admin page is out of date, reload" banner (events_stale text, id/en).
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetChunkErrorForTests } from "@/components/common/lazy";
import { resetAuthStateForTests } from "@/lib/api/client";
import { AppShell } from "./AppShell";
import { SessionGate } from "./SessionGate";
import { Providers, SESSION, STATS, installFakeWebSocket, installFetch } from "./testUtils";

const nav = vi.hoisted(() => ({ pathname: "/" }));
vi.mock("next/navigation", () => ({
  usePathname: () => nav.pathname,
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
}));

// The chunk of the dialog is gone (old tab after an update).
vi.mock("./NotificationsDialog", () => {
  const err = new Error("Loading chunk web_admin_src_components_shell_NotificationsDialog failed (404)");
  err.name = "ChunkLoadError";
  throw err;
});

function mountShell() {
  return render(
    <Providers>
      <SessionGate>
        <AppShell>
          <p data-testid="page">halaman</p>
        </AppShell>
      </SessionGate>
    </Providers>,
  );
}

beforeEach(() => {
  resetAuthStateForTests();
  resetChunkErrorForTests();
  installFakeWebSocket();
  installFetch({
    "GET /admin/api/v1/session": () => ({ status: 200, body: SESSION }),
    "GET /admin/api/v1/stats": () => ({ status: 200, body: STATS }),
  });
  vi.spyOn(console, "warn").mockImplementation(() => {});
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("a lazy dialog whose chunk failed to load (RV-22)", () => {
  it("does not open, does not freeze, and shows the reload banner (id)", async () => {
    window.localStorage.setItem("ad.locale", "id");
    mountShell();
    await screen.findByTestId("admin-shell");
    expect(screen.queryByRole("alert")).toBeNull();

    const bell = screen.getByRole("button", { name: /^Notifikasi/ });
    fireEvent.click(bell);
    const banner = await screen.findByRole("alert");
    expect(banner).toHaveTextContent("Halaman admin ini versi lama. Muat ulang halaman.");
    expect(screen.getByRole("button", { name: "Muat ulang halaman" })).toBeInTheDocument();
    await waitFor(() => expect(bell).toHaveAttribute("aria-expanded", "false")); // closed again
    expect(screen.queryByRole("dialog")).toBeNull();

    // Still usable: the page content is there and the trigger can be pressed again.
    expect(screen.getByTestId("page")).toBeInTheDocument();
    fireEvent.click(bell);
    await waitFor(() => expect(bell).toHaveAttribute("aria-expanded", "false"));
    expect(screen.getByTestId("page")).toBeInTheDocument();
  });

  it("the banner is localized (en)", async () => {
    window.localStorage.setItem("ad.locale", "en");
    mountShell();
    await screen.findByTestId("admin-shell");
    fireEvent.click(await screen.findByRole("button", { name: /^Notifications/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This admin page is out of date. Reload the page.");
  });
});
