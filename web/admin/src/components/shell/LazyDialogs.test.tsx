// @vitest-environment jsdom
// 002 T070 (FR-027): the shell's Radix dialogs ("More" menu, notifications) are not part of the
// initial JS. The trigger is a plain button that prefetches the dialog on intent
// (pointerenter/focus), opens it on click, and gets the focus back when it closes (a11y).
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetAuthStateForTests } from "@/lib/api/client";
import { AppShell } from "./AppShell";
import { SessionGate } from "./SessionGate";
import { Providers, SESSION, STATS, installFakeWebSocket, installFetch } from "./testUtils";

const nav = vi.hoisted(() => ({ pathname: "/", push: vi.fn() }));
vi.mock("next/navigation", () => ({
  usePathname: () => nav.pathname,
  useRouter: () => ({ push: nav.push, replace: vi.fn(), prefetch: vi.fn() }),
}));

const loads = vi.hoisted(() => ({ more: 0, notifications: 0 }));
vi.mock("./MoreMenuDialog", async (orig) => {
  loads.more++;
  return await orig();
});
vi.mock("./NotificationsDialog", async (orig) => {
  loads.notifications++;
  return await orig();
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
  installFakeWebSocket();
  window.localStorage.setItem("ad.locale", "id");
  installFetch({
    "GET /admin/api/v1/session": () => ({ status: 200, body: SESSION }),
    "GET /admin/api/v1/stats": () => ({ status: 200, body: STATS }),
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("lazy shell dialogs (T070)", () => {
  it("nothing of the dialogs loads with the page; intent prefetches once; click opens; closing returns the focus", async () => {
    mountShell();
    await screen.findByTestId("admin-shell");
    expect(loads.more).toBe(0);
    expect(loads.notifications).toBe(0);
    expect(screen.queryByRole("dialog")).toBeNull();

    const more = screen.getByRole("button", { name: "Lainnya" });
    expect(more).toHaveAttribute("aria-haspopup", "dialog");
    fireEvent.pointerEnter(more);
    fireEvent.focus(more);
    await waitFor(() => expect(loads.more).toBe(1)); // prefetched on intent, once
    expect(screen.queryByRole("dialog")).toBeNull(); // not opened by intent

    more.focus();
    fireEvent.click(more);
    expect(await screen.findByRole("dialog", { name: "Opsi lainnya" })).toBeInTheDocument();
    expect(more).toHaveAttribute("aria-expanded", "true");
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(more).toHaveAttribute("aria-expanded", "false");
    await waitFor(() => expect(document.activeElement).toBe(more));
    expect(loads.more).toBe(1);
  });

  it("the bell opens the notifications dialog on click even without a prior hover, and gets the focus back", async () => {
    mountShell();
    await screen.findByTestId("admin-shell");
    const bell = screen.getByRole("button", { name: /^Notifikasi/ });
    bell.focus();
    fireEvent.click(bell);
    expect(await screen.findByText("Belum ada notifikasi pada sesi ini.")).toBeInTheDocument();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(bell));
  });
});
