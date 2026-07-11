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
});
