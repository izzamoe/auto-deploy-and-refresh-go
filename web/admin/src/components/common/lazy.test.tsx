// @vitest-environment jsdom
// 002 T070 / RV-22: on-demand modules. A failed `import()` (ChunkLoadError of an old tab) never
// throws into React: it resolves to null, is reported once (banner state), and is retried the
// next time. ConfirmDialog closes itself again instead of waiting forever.
import { act, cleanup, render, renderHook, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Providers } from "@/components/shell/testUtils";
import { lazyModule, renderNothing, resetChunkErrorForTests, useChunkFailed, useLazyModule } from "./lazy";

const chunkError = () => Object.assign(new Error("Loading chunk 123 failed."), { name: "ChunkLoadError" });

beforeEach(() => {
  resetChunkErrorForTests();
  vi.spyOn(console, "warn").mockImplementation(() => {});
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("lazyModule", () => {
  it("loads once and shares the promise", async () => {
    const importer = vi.fn(async () => ({ value: 42 }));
    const lazy = lazyModule(importer);
    expect(lazy.current()).toBeNull();
    const [a, b] = await Promise.all([lazy.load(), lazy.load()]);
    expect(a).toEqual({ value: 42 });
    expect(b).toBe(a);
    expect(lazy.current()).toBe(a);
    await lazy.load();
    expect(importer).toHaveBeenCalledTimes(1);
  });

  it("a failed import resolves null, flags the page as out of date, and is retried next time", async () => {
    const importer = vi.fn().mockRejectedValueOnce(chunkError()).mockResolvedValueOnce({ ok: true });
    const lazy = lazyModule(importer);
    const failed = renderHook(() => useChunkFailed());
    expect(failed.result.current).toBe(false);
    await act(async () => {
      expect(await lazy.load()).toBeNull();
    });
    expect(failed.result.current).toBe(true);
    expect(await lazy.load()).toEqual({ ok: true });
    expect(importer).toHaveBeenCalledTimes(2);
  });

  it("useLazyModule calls onFail when the import fails (the caller closes what it was opening)", async () => {
    const onFail = vi.fn();
    const lazy = lazyModule(() => Promise.reject(chunkError()));
    const { result } = renderHook(() => useLazyModule(lazy, true, onFail));
    await waitFor(() => expect(onFail).toHaveBeenCalledTimes(1));
    expect(result.current).toBeNull();
  });

  it("renderNothing (next/dynamic loaders) reports the failure and renders nothing", () => {
    const failed = renderHook(() => useChunkFailed());
    let Comp: () => null = () => null;
    act(() => {
      Comp = renderNothing(chunkError());
    });
    expect(failed.result.current).toBe(true);
    const { container } = render(<Comp />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("ConfirmDialog with a missing chunk", () => {
  it("closes itself again instead of hanging, and the page is flagged out of date", async () => {
    vi.resetModules();
    vi.doMock("./ConfirmDialogContent", () => {
      throw chunkError();
    });
    const { ConfirmDialog } = await import("./ConfirmDialog");
    const lazyMod = await import("./lazy");
    const onOpenChange = vi.fn();
    render(
      <Providers>
        <ConfirmDialog open onOpenChange={onOpenChange} title="Hapus?" body="Tidak bisa dibatalkan." confirmLabel="Ya" onConfirm={() => {}} />
      </Providers>,
    );
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(screen.queryByRole("dialog")).toBeNull();
    const failed = renderHook(() => lazyMod.useChunkFailed());
    expect(failed.result.current).toBe(true);
    vi.doUnmock("./ConfirmDialogContent");
  });
});
