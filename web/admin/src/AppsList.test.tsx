import { cleanup, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AppsList } from "./AppsList";

// AppsList consumes the admin event stream; stub it so the component mounts
// without a real WebSocket.
vi.mock("./AdminEventProvider", () => ({
	useAdminEvents: () => ({ progress: {} }),
}));

const ENABLED_APP = {
	id: "app-1",
	name: "demo",
	serviceName: "demo.service",
	githubRepo: "owner/repo",
	binaryPath: "/opt/demo/bin",
	artifactName: "demo-linux-amd64",
	enabled: true,
};

describe("AppsList release loading", () => {
	afterEach(() => {
		cleanup();
		vi.unstubAllGlobals();
	});

	// Regression: a 502 from /apps/:id/releases used to leave releasesByApp[id]
	// undefined, so the load-once effect re-fired on every render and hammered
	// the failing endpoint in an unbounded loop. It must be fetched exactly once.
	it("does not re-fetch releases in a loop when the endpoint fails", async () => {
		const fetchMock = vi.fn((input: RequestInfo | URL) => {
			const url = typeof input === "string" ? input : input.toString();
			if (url.endsWith("/releases")) {
				return Promise.resolve({
					ok: false,
					status: 502,
					json: async () => ({ error: "Failed to fetch releases from GitHub" }),
				} as Response);
			}
			return Promise.resolve({
				ok: true,
				status: 200,
				json: async () => ({ apps: [ENABLED_APP] }),
			} as Response);
		});
		vi.stubGlobal("fetch", fetchMock);

		render(<AppsList setFlash={vi.fn()} />);

		const releaseCalls = () =>
			fetchMock.mock.calls.filter(([input]) => String(input).endsWith("/releases")).length;

		await waitFor(() => expect(releaseCalls()).toBe(1));

		// Give any runaway effect several render cycles to misbehave.
		await new Promise((resolve) => setTimeout(resolve, 100));
		expect(releaseCalls()).toBe(1);
	});
});
