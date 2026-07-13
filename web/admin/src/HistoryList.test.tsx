import { cleanup, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HistoryList } from "./HistoryList";

const mockHistoryData: Record<string, unknown> = {
	history: [
		{ id: "job-1", tag: "v1.0.0", status: "succeeded", trigger: "webhook", errorMsg: "", createdAt: "2025-01-01T00:00:00Z", updatedAt: "2025-01-01T00:05:00Z", appEnabled: true },
	],
};

function mockFetch(): void {
	vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
		ok: true,
		json: async () => mockHistoryData,
	}));
}

vi.mock("./AdminEventProvider", () => ({
	useAdminEvents: () => ({ progress: {} }),
}));

describe("HistoryList", () => {
	beforeEach(() => {
		mockFetch();
		window.history.replaceState({}, "", "#/history");
	});

	afterEach(() => {
		cleanup();
		vi.unstubAllGlobals();
		window.history.replaceState({}, "", "/");
	});

	it("fetches history for the current appId from the URL hash", async () => {
		window.history.replaceState({}, "", "#/history?appId=app-1");
		render(<HistoryList setFlash={vi.fn()} />);
		await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
		const fetchUrl = (fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
		expect(fetchUrl).toBe("/admin/api/history?appId=app-1&page=1&pageSize=20");
	});

	it("BUG CONFIRMED: appId is stale after navigation", async () => {
		window.history.replaceState({}, "", "#/history?appId=app-1");
		const { rerender } = render(<HistoryList setFlash={vi.fn()} />);
		await waitFor(() => {
			expect((fetch as ReturnType<typeof vi.fn>).mock.calls[0][0]).toBe("/admin/api/history?appId=app-1&page=1&pageSize=20");
		});

		window.history.replaceState({}, "", "#/history?appId=app-2");
		vi.mocked(fetch).mockClear();

		rerender(<HistoryList setFlash={vi.fn()} />);

		const fetchAfterNav = (fetch as ReturnType<typeof vi.fn>).mock.calls[0]?.[0] as string | undefined;
		expect(fetchAfterNav).not.toBe("/admin/api/history?appId=app-2");
	});
});
