// @vitest-environment jsdom
// 002 T071 (FR-028, Q-8=A): routes are prefetched only on intent (pointerenter, focus,
// touchstart), once per href; never on render (no viewport prefetch).
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { IntentLink, resetIntentPrefetchForTests } from "./IntentLink";

const router = vi.hoisted(() => ({ prefetch: vi.fn(), push: vi.fn(), replace: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => router, usePathname: () => "/" }));

beforeEach(() => {
  resetIntentPrefetchForTests();
  router.prefetch.mockClear();
});
afterEach(cleanup);

describe("IntentLink", () => {
  it("renders a normal link and prefetches nothing on render", () => {
    render(<IntentLink href="/apps/">Aplikasi</IntentLink>);
    const a = screen.getByRole("link", { name: "Aplikasi" });
    expect(a.getAttribute("href")).toMatch(/^\/apps\/?$/);
    expect(router.prefetch).not.toHaveBeenCalled();
  });

  it("pointerenter, focus and touchstart prefetch that href once", () => {
    render(
      <>
        <IntentLink href="/apps/">Aplikasi</IntentLink>
        <IntentLink href="/history/">Riwayat</IntentLink>
      </>,
    );
    const apps = screen.getByRole("link", { name: "Aplikasi" });
    fireEvent.pointerEnter(apps);
    fireEvent.focus(apps);
    fireEvent.touchStart(apps);
    expect(router.prefetch).toHaveBeenCalledTimes(1);
    expect(router.prefetch).toHaveBeenCalledWith("/apps/");

    fireEvent.touchStart(screen.getByRole("link", { name: "Riwayat" }));
    expect(router.prefetch).toHaveBeenCalledTimes(2);
    expect(router.prefetch).toHaveBeenLastCalledWith("/history/");
  });

  it("keeps the caller's handlers", () => {
    const onFocus = vi.fn();
    const onPointerEnter = vi.fn();
    render(
      <IntentLink href="/logs/" onFocus={onFocus} onPointerEnter={onPointerEnter}>
        Log
      </IntentLink>,
    );
    const a = screen.getByRole("link", { name: "Log" });
    fireEvent.pointerEnter(a);
    fireEvent.focus(a);
    expect(onPointerEnter).toHaveBeenCalledTimes(1);
    expect(onFocus).toHaveBeenCalledTimes(1);
    expect(router.prefetch).toHaveBeenCalledTimes(1);
  });
});
