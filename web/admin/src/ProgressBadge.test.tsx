import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { NormalizedProgress } from "./AdminEventProvider";
import {
	ProgressBadge,
	formatTransferBytes,
	formatTransferSpeed,
	getCurrentActivity,
} from "./ProgressBadge";

function progress(overrides: Partial<NormalizedProgress>): NormalizedProgress {
	return {
		appId: "app-1",
		jobId: "job-1",
		phase: "downloading",
		status: "in_progress",
		...overrides,
	};
}

function liveText(container: HTMLElement) {
	return container.querySelector("[data-progress-live]")?.textContent ?? "";
}

function detailText(container: HTMLElement) {
	return container.querySelector("[data-progress-detail]")?.textContent ?? "";
}

describe("ProgressBadge", () => {
	it.each([
		[1536, "1.5 KB/s"],
		[524288, "512 KB/s"],
		[999999, "977 KB/s"],
		[1048576, "1.0 MB/s"],
	])("formatTransferSpeed formats %i B/s as %s", (bytesPerSecond, expected) => {
		expect(formatTransferSpeed(bytesPerSecond)).toBe(expected);
	});

	it.each([
		[1536, "1.5 KB/s"],
		[524288, "512 KB/s"],
		[999999, "977 KB/s"],
		[1048576, "1.0 MB/s"],
	])("formats %i B/s as %s", (bytesPerSecond, expected) => {
		const { container } = render(
			<ProgressBadge progress={progress({ bytesPerSecond })} />,
		);

		expect(liveText(container)).toContain(expected);
		expect(container.querySelector("[data-progress-speed]")?.textContent).toBe(
			expected,
		);
	});

	it.each([undefined, null, Number.NaN, -1])(
		"omits transfer speed placeholders when speed is %s",
		(bytesPerSecond) => {
			const { container } = render(
				<ProgressBadge
					progress={progress({
						bytesPerSecond: bytesPerSecond as unknown as number,
					})}
				/>,
			);

			const text = liveText(container);
			expect(formatTransferSpeed(bytesPerSecond as unknown as number)).toBeUndefined();
			expect(container.querySelector("[data-progress-speed]")).toBeNull();
			expect(text).not.toContain("NaN");
			expect(text).not.toContain("undefined");
			expect(text).not.toContain("0 MB/s");
		},
	);

	it("renders zero transfer speed as 0 KB/s", () => {
		const { container } = render(
			<ProgressBadge progress={progress({ bytesPerSecond: 0 })} />,
		);

		expect(container.querySelector("[data-progress-speed]")?.textContent).toBe(
			"0 KB/s",
		);
	});

	it("keeps byte totals readable", () => {
		const { container } = render(
			<ProgressBadge
				progress={progress({ doneBytes: 1536, totalBytes: 1048576, percent: 50 })}
			/>,
		);

		expect(formatTransferBytes(1536)).toBe("1.5 KB");
		expect(liveText(container)).toContain("1.5 KB / 1.0 MB (50%)");
	});

	it.each([
		["queued", "Waiting in deployment queue"],
		["pending", "Waiting in deployment queue"],
		["starting", "Starting deployment"],
		["running", "Deployment is running"],
		["in_progress", "Deployment is running"],
		["downloading", "Downloading release artifact"],
		["validating", "Validating downloaded artifact"],
		["backing_up", "Backing up current binary"],
		["installing", "Installing new binary"],
		["restarting", "Restarting systemd service"],
		["healthcheck", "Checking service health"],
		["rollback", "Rolling back to previous binary"],
		["cancel_requested", "Cancel requested; waiting for deployment to stop"],
		["canceled", "Deployment canceled"],
		["succeeded", "Deployment completed successfully"],
		["failed", "Deployment failed"],
		["idle", "Idle; no deployment running"],
		["unknown_future_phase", "Waiting for status update"],
	])("renders %s current activity as %s", (status, expected) => {
		const { container } = render(<ProgressBadge status={status} />);

		expect(getCurrentActivity(undefined, status)).toBe(expected);
		expect(detailText(container)).toBe(`Current activity: ${expected}`);
	});

	it("uses phase activity for active jobs and preserves backend detail", () => {
		const { container } = render(
			<ProgressBadge
				progress={progress({
					phase: "downloading",
					status: "in_progress",
					message: "backend says half complete",
				})}
			/>,
		);

		expect(detailText(container)).toContain(
			"Current activity: Downloading release artifact",
		);
		expect(detailText(container)).toContain("Detail: backend says half complete");
	});

	it("uses terminal status activity even when an older active phase remains", () => {
		const { container } = render(
			<ProgressBadge
				progress={progress({ phase: "downloading", status: "failed" })}
			/>,
		);

		expect(detailText(container)).toBe("Current activity: Deployment failed");
	});

	it("renders raw status in a shadcn badge with legacy status classes", () => {
		const { container } = render(<ProgressBadge status="failed" />);
		const badge = container.querySelector(".deploy-status-badge");

		expect(badge?.textContent).toBe("failed");
		expect(badge?.className).toContain("deploy-status-badge");
		expect(badge?.className).toContain("status-failed");
	});
});
