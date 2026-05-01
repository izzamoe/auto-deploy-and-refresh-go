import { act, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AdminEventProvider, useAdminEvents } from "./AdminEventProvider";

class MockWebSocket {
	onopen: () => void = () => {};
	onmessage: (event: { data: string }) => void = () => {};
	onclose: () => void = () => {};
	onerror: () => void = () => {};
	close = vi.fn();

	constructor(public url: string) {}
}

describe("AdminEventProvider", () => {
	let originalWebSocket: typeof WebSocket;

	beforeEach(() => {
		originalWebSocket = window.WebSocket;
		window.WebSocket = MockWebSocket as unknown as typeof WebSocket;
		vi.useFakeTimers();
	});

	afterEach(() => {
		window.WebSocket = originalWebSocket;
		vi.useRealTimers();
	});

	it("isolates WebSocket creation to provider and handles unknown events", () => {
		let wsInstance: MockWebSocket | null = null;
		window.WebSocket = vi.fn().mockImplementation((url: string) => {
			wsInstance = new MockWebSocket(url);
			return wsInstance;
		}) as unknown as typeof WebSocket;

		let currentState: ReturnType<typeof useAdminEvents> | undefined;
		const TestComponent = () => {
			currentState = useAdminEvents();
			return (
				<div data-testid="connected">{String(currentState.connected)}</div>
			);
		};

		render(
			<AdminEventProvider>
				<TestComponent />
			</AdminEventProvider>,
		);

		expect(window.WebSocket).toHaveBeenCalledTimes(1);
		expect(window.WebSocket).toHaveBeenCalledWith(
			expect.stringContaining("/admin/events/ws"),
		);

		if (!wsInstance) throw new Error("WebSocket instance not created");

		act(() => {
			wsInstance?.onopen();
		});

		expect(currentState?.connected).toBe(true);

		act(() => {
			wsInstance?.onmessage({
				data: JSON.stringify({ type: "unknown_event" }),
			});
		});

		expect(currentState?.events.length).toBe(0); // Should be ignored
	});

	it("normalizes compact progress frames", () => {
		let wsInstance: MockWebSocket | null = null;
		window.WebSocket = vi.fn().mockImplementation((url: string) => {
			wsInstance = new MockWebSocket(url);
			return wsInstance;
		}) as unknown as typeof WebSocket;

		let currentState: ReturnType<typeof useAdminEvents> | undefined;
		const TestComponent = () => {
			currentState = useAdminEvents();
			return null;
		};

		render(
			<AdminEventProvider>
				<TestComponent />
			</AdminEventProvider>,
		);

		if (!wsInstance) throw new Error("WebSocket instance not created");

		act(() => {
			wsInstance?.onopen();
		});

		// Known totals
		act(() => {
			wsInstance?.onmessage({
				data: JSON.stringify({
					t: "p",
					a: "app1",
					j: "job1",
					ph: "downloading",
					st: "running",
					pct: 50,
					db: 500,
					tb: 1000,
					bps: 100,
					msg: "Downloading...",
				}),
			});
		});

		expect(currentState?.progress.job1).toEqual({
			appId: "app1",
			jobId: "job1",
			phase: "downloading",
			status: "running",
			percent: 50,
			doneBytes: 500,
			totalBytes: 1000,
			bytesPerSecond: 100,
			message: "Downloading...",
		});

		// Unknown totals (indeterminate)
		act(() => {
			wsInstance?.onmessage({
				data: JSON.stringify({
					t: "p",
					a: "app2",
					j: "job2",
					ph: "starting",
					st: "running",
				}),
			});
		});

		expect(currentState?.progress.job2).toEqual({
			appId: "app2",
			jobId: "job2",
			phase: "starting",
			status: "running",
			percent: undefined,
			doneBytes: undefined,
			totalBytes: undefined,
			bytesPerSecond: undefined,
			message: undefined,
		});
	});

	it("handles readable events (hello, snapshot, job_status, cancel_requested)", () => {
		let wsInstance: MockWebSocket | null = null;
		window.WebSocket = vi.fn().mockImplementation((url: string) => {
			wsInstance = new MockWebSocket(url);
			return wsInstance;
		}) as unknown as typeof WebSocket;

		let currentState: ReturnType<typeof useAdminEvents> | undefined;
		const TestComponent = () => {
			currentState = useAdminEvents();
			return null;
		};

		render(
			<AdminEventProvider>
				<TestComponent />
			</AdminEventProvider>,
		);

		if (!wsInstance) throw new Error("WebSocket instance not created");

		act(() => {
			wsInstance?.onopen();
		});

		act(() => {
			wsInstance?.onmessage({
				data: JSON.stringify({ type: "hello", payload: { version: "1" } }),
			});
			wsInstance?.onmessage({
				data: JSON.stringify({
					type: "snapshot",
					payload: [
						{
							JobID: "job_snap",
							AppID: "app1",
							Phase: "idle",
							Status: "pending",
						},
					],
				}),
			});
		});

		expect(currentState?.events.length).toBe(2);
		expect(currentState?.progress.job_snap).toBeDefined();
		expect(currentState?.progress.job_snap.status).toBe("pending");

		act(() => {
			wsInstance?.onmessage({
				data: JSON.stringify({
					type: "job_status",
					payload: { jobId: "job_snap", status: "running" },
				}),
			});
		});

		expect(currentState?.events.length).toBe(3);
		expect(currentState?.progress.job_snap.status).toBe("running");

		act(() => {
			wsInstance?.onmessage({
				data: JSON.stringify({
					type: "cancel_requested",
					payload: { jobId: "job_snap" },
				}),
			});
		});

		expect(currentState?.events.length).toBe(4);
		expect(currentState?.progress.job_snap.status).toBe("cancel_requested");
	});
});
