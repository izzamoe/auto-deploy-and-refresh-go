// T078 — reproduction of FE findings B50-B60 / K2 on the OLD (Vite) code.
// Every test ASSERTS THE BUG: a green test means the finding is PROVEN.
// This file is deleted together with the old code in T084; its output is kept
// in reference/executor/phase-5.md.
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "../App";
import { AdminEventProvider, useAdminEvents } from "../AdminEventProvider";
import { apiRequest } from "../api";
import { AppForm } from "../AppForm";
import { AppsList } from "../AppsList";
import { HistoryList } from "../HistoryList";

type FakeResponse = { ok: boolean; status: number; json: () => Promise<unknown> };

function jsonResponse(status: number, body: unknown): FakeResponse {
	return { ok: status >= 200 && status < 300, status, json: async () => body };
}

type Call = { url: string; method: string; body: unknown; signal: unknown };

// installFetch routes by "METHOD path" (path without /admin/api prefix, query kept)
// and records every call.
function installFetch(handler: (method: string, path: string, body: unknown) => FakeResponse | Promise<FakeResponse>) {
	const calls: Call[] = [];
	const mock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
		const url = String(input);
		const method = (init?.method || "GET").toUpperCase();
		const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
		calls.push({ url, method, body, signal: init?.signal });
		return Promise.resolve(handler(method, url.replace("/admin/api", ""), body));
	});
	vi.stubGlobal("fetch", mock);
	return calls;
}

class FakeWebSocket {
	static instances: FakeWebSocket[] = [];
	onopen: () => void = () => {};
	onmessage: (event: { data: string }) => void = () => {};
	onclose: () => void = () => {};
	onerror: () => void = () => {};
	close = vi.fn();
	constructor(public url: string) {
		FakeWebSocket.instances.push(this);
	}
	send(frame: unknown) {
		this.onmessage({ data: JSON.stringify(frame) });
	}
}

beforeEach(() => {
	FakeWebSocket.instances = [];
	vi.stubGlobal("WebSocket", FakeWebSocket);
	window.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
});

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
	window.history.replaceState({}, "", "/");
	window.location.hash = "";
});

const APP = {
	id: "a1",
	name: "demo",
	serviceName: "demo.service",
	githubRepo: "owner/repo",
	binaryPath: "/opt/demo/bin",
	artifactName: "demo-linux-amd64",
	enabled: true,
};

describe("B50 / K2 — 401 and failed session probe", () => {
	it("B50: a 401 on every call produces NO redirect to a login page", async () => {
		const assign = vi.fn();
		vi.stubGlobal("location", { ...window.location, hash: "#/", search: "", pathname: "/admin/", assign, replace: vi.fn() });
		installFetch(() => jsonResponse(401, { error: "unauthorized" }));
		render(<App />);
		await screen.findByTestId("admin-shell");
		await new Promise((r) => setTimeout(r, 50));
		expect(assign).not.toHaveBeenCalled();
	});

	it("K2: getAccount fails (401) -> the full admin UI is still rendered with account=null", async () => {
		installFetch(() => jsonResponse(401, { error: "unauthorized" }));
		render(<App />);
		expect(await screen.findByTestId("admin-shell")).toBeDefined();
		expect(screen.getByTestId("admin-content")).toBeDefined();
		expect(screen.getByRole("link", { name: "Apps" })).toBeDefined();
	});
});

function EventsProbe({ onRender }: { onRender: (s: ReturnType<typeof useAdminEvents>) => void }) {
	const s = useAdminEvents();
	onRender(s);
	return null;
}

describe("B51 / B52 — event store", () => {
	it("B51: 100 heartbeats -> events array holds 100 entries and consumers re-render per heartbeat", async () => {
		let latest: ReturnType<typeof useAdminEvents> | undefined;
		let renders = 0;
		render(
			<AdminEventProvider>
				<EventsProbe onRender={(s) => { latest = s; renders++; }} />
			</AdminEventProvider>,
		);
		const ws = FakeWebSocket.instances[0];
		const before = renders;
		for (let i = 0; i < 100; i++) {
			await act(async () => {
				ws.send({ type: "heartbeat" });
			});
		}
		expect(latest?.events.length).toBe(100);
		expect(renders - before).toBe(100); // one consumer re-render per heartbeat
	});

	it("B52: snapshot job with phase 'downloading' and no status -> status shown is 'downloading'", async () => {
		let latest: ReturnType<typeof useAdminEvents> | undefined;
		render(
			<AdminEventProvider>
				<EventsProbe onRender={(s) => { latest = s; }} />
			</AdminEventProvider>,
		);
		await act(async () => {
			FakeWebSocket.instances[0].send({
				type: "snapshot",
				payload: [{ AppID: "a1", JobID: "j1", Phase: "downloading", Percent: 10 }],
			});
		});
		expect(latest?.progress["j1"]?.status).toBe("downloading");
	});

	it("B52 (UI): live status of a snapshot job overrides the DB status with the phase in the apps list", async () => {
		installFetch((_method, path) => {
			if (path === "/apps") return jsonResponse(200, { apps: [{ ...APP, lastJobId: "j1", lastJobStatus: "canceled" }] });
			if (path.endsWith("/service/status")) return jsonResponse(200, { status: "active" });
			if (path.endsWith("/releases")) return jsonResponse(200, { releases: [] });
			return jsonResponse(200, {});
		});
		render(
			<AdminEventProvider>
				<AppsList setFlash={vi.fn()} />
			</AdminEventProvider>,
		);
		await screen.findByText("demo");
		await act(async () => {
			FakeWebSocket.instances[0].send({ type: "snapshot", payload: [{ AppID: "a1", JobID: "j1", Phase: "downloading" }] });
		});
		await waitFor(() => expect(document.querySelector(".deploy-status-badge")?.textContent).toBe("downloading"));
	});
});

function editAppHandler(overrides: Record<string, () => FakeResponse> = {}) {
	return (method: string, path: string): FakeResponse => {
		const key = `${method} ${path}`;
		if (overrides[key]) return overrides[key]();
		if (method === "GET" && path === "/apps/a1") return jsonResponse(200, { app: APP });
		if (method === "GET" && path === "/apps/a1/env") return jsonResponse(200, { envVars: [{ name: "SECRET", value: "x" }] });
		if (method === "GET" && path === "/apps/a1/args") return jsonResponse(200, { args: ["--port", "80"] });
		return jsonResponse(200, { ok: true });
	};
}

describe("B53 — edit form data loss", () => {
	it("B53: GET env fails (500) -> clicking Update sends PUT /env with an empty list", async () => {
		const calls = installFetch(editAppHandler({ "GET /apps/a1/env": () => jsonResponse(500, { error: "boom" }) }));
		render(<AppForm id="a1" navigate={vi.fn()} setFlash={vi.fn()} />);
		await waitFor(() => expect((screen.getByLabelText("App Name") as HTMLInputElement).value).toBe("demo"));
		await new Promise((r) => setTimeout(r, 20));
		fireEvent.click(screen.getByRole("button", { name: "Update App" }));
		await waitFor(() => expect(calls.some((c) => c.method === "PUT" && c.url.endsWith("/apps/a1/env"))).toBe(true));
		const put = calls.find((c) => c.method === "PUT" && c.url.endsWith("/apps/a1/env"));
		expect(put?.body).toEqual({ envVars: [] });
	});
});

describe("B54 — edit form reset by flash", () => {
	it("B54: a flash raised on the edit page re-fetches the app and overwrites unsaved input", async () => {
		const calls = installFetch(
			editAppHandler({ "GET /apps/a1/service-unit/preview": () => jsonResponse(500, { error: "preview failed" }) }),
		);
		window.location.hash = "#/apps/a1/edit";
		render(<App />);
		const name = (await screen.findByLabelText("App Name")) as HTMLInputElement;
		await waitFor(() => expect(name.value).toBe("demo"));
		const appFetches = () => calls.filter((c) => c.method === "GET" && c.url.endsWith("/apps/a1")).length;
		const before = appFetches();
		fireEvent.change(name, { target: { value: "unsaved-edit" } });
		expect(name.value).toBe("unsaved-edit");
		fireEvent.click(screen.getAllByRole("button", { name: "Generate service unit" })[0]);
		await screen.findByTestId("admin-flash");
		await waitFor(() => expect(appFetches()).toBeGreaterThan(before));
		await waitFor(() => expect((screen.getByLabelText("App Name") as HTMLInputElement).value).toBe("demo"));
	});
});

describe("B55 — non-atomic create", () => {
	it("B55: POST /apps succeeds, PUT /env fails 400 -> the app exists but no webhook snippet is shown", async () => {
		const calls = installFetch((method, path) => {
			if (method === "POST" && path === "/apps") return jsonResponse(201, { app: { id: "new1" } });
			if (method === "PUT" && path === "/apps/new1/env") return jsonResponse(400, { error: "invalid env", errors: ["invalid env"] });
			return jsonResponse(200, {});
		});
		render(<AppForm navigate={vi.fn()} setFlash={vi.fn()} />);
		fireEvent.change(screen.getByLabelText("App Name"), { target: { value: "x" } });
		fireEvent.change(screen.getByLabelText("Webhook Secret"), { target: { value: "s".repeat(40) } });
		fireEvent.change(screen.getByLabelText("Service Name"), { target: { value: "x.service" } });
		fireEvent.change(screen.getByLabelText("Binary Path"), { target: { value: "/opt/x" } });
		fireEvent.change(screen.getByLabelText("GitHub Repo"), { target: { value: "o/r" } });
		fireEvent.change(screen.getByLabelText("Artifact Name"), { target: { value: "x" } });
		fireEvent.change(screen.getByLabelText("Environment Variables"), { target: { value: "A=1" } });
		fireEvent.click(screen.getByRole("button", { name: "Create App" }));
		await waitFor(() => expect(document.getElementById("form-errors")).not.toBeNull());
		expect(calls.filter((c) => c.method === "POST" && c.url.endsWith("/apps")).length).toBe(1);
		expect(calls.some((c) => c.method === "PUT" && c.url.endsWith("/apps/new1/env"))).toBe(true);
		expect(screen.queryByTestId("webhook-snippet")).toBeNull();
	});
});

describe("B56 — response race", () => {
	it("B56: a slow response for the previous filter arrives last and overwrites the list; no AbortSignal is used", async () => {
		let releaseOld: (r: FakeResponse) => void = () => {};
		const old = new Promise<FakeResponse>((resolve) => { releaseOld = resolve; });
		const row = (tag: string) => ({ id: tag, tag, status: "succeeded", trigger: "webhook", errorMsg: "", createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z", appEnabled: true });
		const calls = installFetch((_method, path) => {
			if (path.startsWith("/history?") && !path.includes("appId=")) return old as unknown as FakeResponse;
			if (path.startsWith("/history?") && path.includes("appId=a2")) return jsonResponse(200, { history: [row("NEW-v2")], total: 1 });
			return jsonResponse(200, {});
		});
		window.location.hash = "#/history";
		render(
			<AdminEventProvider>
				<HistoryList setFlash={vi.fn()} />
			</AdminEventProvider>,
		);
		await waitFor(() => expect(calls.length).toBeGreaterThanOrEqual(1));
		await act(async () => {
			window.location.hash = "#/history?appId=a2";
			window.dispatchEvent(new HashChangeEvent("hashchange"));
		});
		await screen.findByText("NEW-v2");
		await act(async () => {
			releaseOld(jsonResponse(200, { history: [row("OLD-v1")], total: 1 }));
		});
		await screen.findByText("OLD-v1");
		expect(screen.queryByText("NEW-v2")).toBeNull();
		expect(calls.every((c) => c.signal === undefined)).toBe(true);
	});
});

describe("B57 — stale derived data", () => {
	it("B57: after the job reaches a terminal state there is no refetch of service status or apps", async () => {
		let progress: Record<string, unknown> = { j1: { appId: "a1", jobId: "j1", phase: "installing", status: "in_progress" } };
		const calls = installFetch((_m, path) => {
			if (path === "/apps") return jsonResponse(200, { apps: [{ ...APP, lastJobId: "j1", lastJobStatus: "in_progress" }] });
			if (path.endsWith("/service/status")) return jsonResponse(200, { status: "active" });
			if (path.endsWith("/releases")) return jsonResponse(200, { releases: [] });
			return jsonResponse(200, {});
		});
		// Drive AppsList through the real provider: send frames over the fake socket.
		render(
			<AdminEventProvider>
				<AppsList setFlash={vi.fn()} />
			</AdminEventProvider>,
		);
		await screen.findByText("demo");
		await waitFor(() => expect(calls.filter((c) => c.url.endsWith("/service/status")).length).toBe(1));
		const statusBefore = calls.filter((c) => c.url.endsWith("/service/status")).length;
		const appsBefore = calls.filter((c) => c.url.endsWith("/admin/api/apps")).length;
		progress = { j1: { ...(progress.j1 as object), status: "succeeded" } };
		await act(async () => {
			FakeWebSocket.instances[0].send({ type: "job_status", payload: { jobId: "j1", status: "succeeded" } });
			// also the compact terminal frame the runner emits
			FakeWebSocket.instances[0].send({ t: "p", a: "a1", j: "j1", ph: "done", st: "succeeded" });
		});
		await new Promise((r) => setTimeout(r, 150));
		expect(calls.filter((c) => c.url.endsWith("/service/status")).length).toBe(statusBefore);
		expect(calls.filter((c) => c.url.endsWith("/admin/api/apps")).length).toBe(appsBefore);
		expect(Object.keys(progress)).toContain("j1");
	});
});

describe("B58 — swallowed network errors", () => {
	it("B58: 'TypeError: Failed to fetch' on Disable produces no flash message", async () => {
		const setFlash = vi.fn();
		let toggleCalled = false;
		vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
			const url = String(input);
			if (url.endsWith("/toggle")) {
				toggleCalled = true;
				return Promise.reject(new TypeError("Failed to fetch"));
			}
			if (url.endsWith("/admin/api/apps")) return Promise.resolve(jsonResponse(200, { apps: [APP] }));
			void init;
			return Promise.resolve(jsonResponse(200, { status: "active", releases: [] }));
		}));
		render(
			<AdminEventProvider>
				<AppsList setFlash={setFlash} />
			</AdminEventProvider>,
		);
		await screen.findByText("demo");
		fireEvent.click(screen.getByRole("button", { name: "Disable" }));
		await waitFor(() => expect(toggleCalled).toBe(true));
		await new Promise((r) => setTimeout(r, 50));
		expect(setFlash).not.toHaveBeenCalled();
	});

	it("B58: an HTML 502 body (Cloudflare) yields only the generic 'API request failed'", async () => {
		vi.stubGlobal("fetch", vi.fn(() =>
			Promise.resolve({ ok: false, status: 502, json: async () => { throw new SyntaxError("Unexpected token <"); } }),
		));
		await expect(apiRequest("/apps")).rejects.toThrow("API request failed");
	});
});

describe("B59 — ?flash= spoofing", () => {
	it("B59: a flash message supplied in the query string is displayed as a success notice", async () => {
		installFetch(() => jsonResponse(200, { apps: [], username: "admin", mustChangePassword: false }));
		window.history.replaceState({}, "", "/admin/?flash=Sesi+habis,+login+ulang+di+evil.example#/");
		render(<App />);
		expect(await screen.findByTestId("admin-flash")).toHaveTextContent("Sesi habis, login ulang di evil.example");
	});
});

describe("B60 — test hook exposure", () => {
	it("B60: AdminUITest.injectEvent is installed whenever MODE != production (test/staging)", async () => {
		render(
			<AdminEventProvider>
				<div />
			</AdminEventProvider>,
		);
		expect(import.meta.env.MODE).not.toBe("production");
		expect(typeof window.AdminUITest?.injectEvent).toBe("function");
		// Production-build exposure is checked separately by grep over the build output.
	});
});
