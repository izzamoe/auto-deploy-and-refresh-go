import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AppForm } from "./AppForm";

describe("AppForm webhook snippet", () => {
	beforeEach(() => {
		vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
			ok: true,
			json: async () => ({ app: { id: "app-1" } }),
		}));
	});

	afterEach(() => {
		cleanup();
		vi.unstubAllGlobals();
	});

	it("shows a webhook curl snippet with the real secret after creating an app", async () => {
		const navigate = vi.fn();
		const setFlash = vi.fn();

		render(<AppForm navigate={navigate} setFlash={setFlash} />);

		fireEvent.change(screen.getByLabelText("App Name"), { target: { value: "my-app" } });
		fireEvent.change(screen.getByLabelText("Webhook Secret"), { target: { value: "super-secret-value" } });
		fireEvent.change(screen.getByLabelText("Service Name"), { target: { value: "my-app.service" } });
		fireEvent.change(screen.getByLabelText("Binary Path"), { target: { value: "/opt/my-app/bin" } });
		fireEvent.change(screen.getByLabelText("GitHub Repo"), { target: { value: "owner/repo" } });
		fireEvent.change(screen.getByLabelText("Artifact Name"), { target: { value: "my-app-linux-amd64" } });

		fireEvent.click(screen.getByRole("button", { name: "Create App" }));

		await waitFor(() => expect(screen.getByTestId("webhook-snippet")).toBeDefined());

		const curlSnippet = screen.getByTestId("webhook-curl-snippet");
		expect(curlSnippet.textContent).toContain("super-secret-value");
		expect(curlSnippet.textContent).toContain(`${window.location.origin}/webhook`);
		expect(curlSnippet.textContent).toContain("Authorization: Bearer super-secret-value");

		const ghaSnippet = screen.getByTestId("webhook-gha-snippet");
		expect(ghaSnippet.textContent).toContain("secrets.DEPLOY_WEBHOOK_SECRET");
		expect(ghaSnippet.textContent).not.toContain("super-secret-value");

		// Navigation to the app list only happens after the operator dismisses the panel.
		expect(navigate).not.toHaveBeenCalled();

		fireEvent.click(screen.getByRole("button", { name: "Done" }));
		expect(navigate).toHaveBeenCalledWith("#/", "App created successfully");
	});

	it("shows webhook reference snippets on the edit form, using a placeholder until a new secret is typed", async () => {
		vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string) => {
			if (String(url).endsWith("/env")) {
				return Promise.resolve({ ok: true, json: async () => ({ envVars: [] }) });
			}
			if (String(url).endsWith("/args")) {
				return Promise.resolve({ ok: true, json: async () => ({ args: [] }) });
			}
			return Promise.resolve({
				ok: true,
				json: async () => ({
					app: {
						id: "app-1",
						name: "my-app",
						serviceName: "my-app.service",
						binaryPath: "/opt/my-app/bin",
						githubRepo: "owner/repo",
						artifactName: "my-app-linux-amd64",
						enabled: true,
					},
				}),
			});
		}));

		render(<AppForm id="app-1" navigate={vi.fn()} setFlash={vi.fn()} />);

		// The reference card renders in edit mode without needing to submit.
		await waitFor(() => expect(screen.getByTestId("webhook-reference")).toBeDefined());

		const curl = screen.getByTestId("webhook-ref-curl-snippet");
		expect(curl.textContent).toContain(`${window.location.origin}/webhook`);
		// No secret is available for an existing app, so the snippet uses a placeholder.
		expect(curl.textContent).toContain("<your-webhook-secret>");

		// Typing a replacement secret fills it into the curl snippet live.
		fireEvent.change(screen.getByLabelText("Webhook Secret"), { target: { value: "rotated-secret" } });
		expect(screen.getByTestId("webhook-ref-curl-snippet").textContent).toContain("Authorization: Bearer rotated-secret");
		expect(screen.getByTestId("webhook-ref-curl-snippet").textContent).not.toContain("<your-webhook-secret>");
	});

	it("loads command-line arguments from GET /args and PUTs the parsed args on submit", async () => {
		const calls: { url: string; method?: string; body?: string }[] = [];
		vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string, options?: RequestInit) => {
			calls.push({ url: String(url), method: options?.method, body: options?.body as string | undefined });
			if (String(url).endsWith("/env")) {
				return Promise.resolve({ ok: true, json: async () => ({ envVars: [] }) });
			}
			if (String(url).endsWith("/args")) {
				return Promise.resolve({ ok: true, json: async () => ({ args: ["--port", "8080"] }) });
			}
			return Promise.resolve({
				ok: true,
				json: async () => ({
					app: {
						id: "app-1",
						name: "my-app",
						serviceName: "my-app.service",
						binaryPath: "/opt/my-app/bin",
						githubRepo: "owner/repo",
						artifactName: "my-app-linux-amd64",
						enabled: true,
					},
				}),
			});
		}));

		render(<AppForm id="app-1" navigate={vi.fn()} setFlash={vi.fn()} />);

		const textarea = await screen.findByLabelText("Command-line Arguments") as HTMLTextAreaElement;
		await waitFor(() => expect(textarea.value).toBe("--port\n8080"));

		fireEvent.change(textarea, { target: { value: "--port 9090 --msg \"hello world\"" } });
		fireEvent.click(screen.getByRole("button", { name: "Update App" }));

		await waitFor(() => {
			const putArgs = calls.find((c) => c.url.endsWith("/args") && c.method === "PUT");
			expect(putArgs).toBeDefined();
			expect(JSON.parse(putArgs!.body!)).toEqual({ args: ["--port", "9090", "--msg", "hello world"] });
		});
	});

	it("previews and applies the service unit from the edit form", async () => {
		const calls: { url: string; method?: string }[] = [];
		const setFlash = vi.fn();
		const unitText = 'ExecStart=/opt/my-app/bin "--port" "8080"';
		vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string, options?: RequestInit) => {
			calls.push({ url: String(url), method: options?.method });
			if (String(url).endsWith("/env")) {
				return Promise.resolve({ ok: true, json: async () => ({ envVars: [] }) });
			}
			if (String(url).endsWith("/args")) {
				return Promise.resolve({ ok: true, json: async () => ({ args: ["--port", "8080"] }) });
			}
			if (String(url).endsWith("/service-unit/preview")) {
				return Promise.resolve({ ok: true, json: async () => ({ unit: unitText }) });
			}
			if (String(url).endsWith("/service-unit/apply")) {
				return Promise.resolve({ ok: true, json: async () => ({ status: "ok", message: "Service unit created and enabled" }) });
			}
			return Promise.resolve({
				ok: true,
				json: async () => ({
					app: {
						id: "app-1",
						name: "my-app",
						serviceName: "my-app.service",
						binaryPath: "/opt/my-app/bin",
						githubRepo: "owner/repo",
						artifactName: "my-app-linux-amd64",
						enabled: true,
					},
				}),
			});
		}));

		render(<AppForm id="app-1" navigate={vi.fn()} setFlash={setFlash} />);

		fireEvent.click(await screen.findByRole("button", { name: "Generate service unit" }));

		// Nothing is written until the operator sees the unit and confirms.
		await waitFor(() => expect(screen.getByText(unitText)).toBeDefined());
		expect(calls.some((c) => c.url.endsWith("/service-unit/apply"))).toBe(false);

		fireEvent.click(screen.getByLabelText("Confirm and apply service unit"));

		await waitFor(() => {
			const apply = calls.find((c) => c.url.endsWith("/service-unit/apply"));
			expect(apply).toBeDefined();
			expect(apply!.method).toBe("POST");
			expect(apply!.url).toContain("/apps/app-1/");
		});
		await waitFor(() => expect(setFlash).toHaveBeenCalledWith({ message: "Service unit created and enabled", type: "success" }));
	});

	it("does not offer the service unit action when creating a new app", () => {
		render(<AppForm navigate={vi.fn()} setFlash={vi.fn()} />);
		expect(screen.queryByRole("button", { name: "Generate service unit" })).toBeNull();
	});
});
