import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import App from "./App";

describe("App component", () => {
	it("renders the initial minimal setup text", () => {
		// Mock WebSocket to prevent actual connection attempt during test
		window.WebSocket = vi.fn().mockImplementation(() => ({
			onopen: vi.fn(),
			onmessage: vi.fn(),
			onclose: vi.fn(),
			onerror: vi.fn(),
			close: vi.fn(),
		})) as unknown as typeof WebSocket;

  const { getByText } = render(<App />);
  expect(getByText(/Auto Deploy Admin/i)).toBeDefined();
 });
});
