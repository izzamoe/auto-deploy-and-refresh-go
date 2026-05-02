import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";

describe("App component", () => {
	beforeEach(() => {
		// Mock WebSocket to prevent actual connection attempt during test
		const webSocket = vi.fn().mockImplementation(() => ({
			onopen: vi.fn(),
			onmessage: vi.fn(),
			onclose: vi.fn(),
			onerror: vi.fn(),
			close: vi.fn(),
		})) as unknown as typeof WebSocket;
		vi.stubGlobal("WebSocket", webSocket);
		window.WebSocket = webSocket;

		vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
			ok: true,
			json: async () => ({ apps: [] }),
		}));
	});

	afterEach(() => {
		cleanup();
		vi.unstubAllGlobals();
		window.history.replaceState({}, "", "/");
	});

	it("renders the shadcn admin shell without changing stable selectors", async () => {
		const { container } = render(<App />);

		expect(screen.getByTestId("admin-shell")).toBeDefined();
		expect(screen.getByTestId("admin-nav")).toBeDefined();
		expect(screen.getByTestId("admin-content")).toBeDefined();
		expect(screen.getByRole("link", { name: /Auto Deploy Admin/i })).toHaveAttribute("href", "#/");
		expect(screen.getByRole("link", { name: "Apps" })).toHaveAttribute("href", "#/");
		expect(screen.getByRole("link", { name: "History" })).toHaveAttribute("href", "#/history");
		expect(container.querySelectorAll('[data-admin-ws-url="/admin/events/ws"]')).toHaveLength(1);
		await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
	});

	it("renders and dismisses flash messaging accessibly", async () => {
		window.history.replaceState({}, "", "/admin/apps?flash=Saved#/");

		render(<App />);

		expect(screen.getByTestId("admin-flash")).toHaveTextContent("Saved");

		fireEvent.click(screen.getByRole("button", { name: "Dismiss notification" }));

		expect(screen.queryByTestId("admin-flash")).toBeNull();
		await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
	});

	it("does not show the enabled switch while creating an app", () => {
		window.history.replaceState({}, "", "/admin/apps#/apps/new");

		render(<App />);

		expect(screen.getByRole("heading", { name: "New App" })).toBeDefined();
		expect(screen.queryByRole("switch", { name: "Enable deployments" })).toBeNull();
	});
});
