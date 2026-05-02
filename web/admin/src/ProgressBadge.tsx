import type { NormalizedProgress } from "./AdminEventProvider";

import { Badge } from "@/components/ui/badge";
import { Progress } from "@/components/ui/progress";

const currentActivityByStage: Record<string, string> = {
	queued: "Waiting in deployment queue",
	pending: "Waiting in deployment queue",
	starting: "Starting deployment",
	running: "Deployment is running",
	in_progress: "Deployment is running",
	downloading: "Downloading release artifact",
	validating: "Validating downloaded artifact",
	backing_up: "Backing up current binary",
	installing: "Installing new binary",
	restarting: "Restarting systemd service",
	healthcheck: "Checking service health",
	rollback: "Rolling back to previous binary",
	cancel_requested: "Cancel requested; waiting for deployment to stop",
	canceled: "Deployment canceled",
	succeeded: "Deployment completed successfully",
	failed: "Deployment failed",
	idle: "Idle; no deployment running",
};

const terminalStatuses = new Set([
	"cancel_requested",
	"canceled",
	"succeeded",
	"failed",
	"idle",
]);

function isFiniteNonNegative(value: number | undefined) {
	return typeof value === "number" && Number.isFinite(value) && value >= 0;
}

function formatKilobytes(value: number) {
	return value < 10 ? value.toFixed(1) : String(Math.round(value));
}

export function formatTransferSpeed(bytesPerSecond?: number) {
	if (!isFiniteNonNegative(bytesPerSecond)) return undefined;
	if (bytesPerSecond === 0) return "0 KB/s";
	if (bytesPerSecond < 1024 * 1024) {
		return `${formatKilobytes(bytesPerSecond / 1024)} KB/s`;
	}

	return `${(bytesPerSecond / (1024 * 1024)).toFixed(1)} MB/s`;
}

export function formatTransferBytes(bytes?: number) {
	if (!isFiniteNonNegative(bytes)) return undefined;
	if (bytes < 1024) return `${Math.round(bytes)} B`;
	if (bytes < 1024 * 1024) return `${formatKilobytes(bytes / 1024)} KB`;

	return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function getCurrentActivity(progress?: NormalizedProgress, status?: string) {
	const currentStatus = progress?.status || status;
	const currentPhase = progress?.phase;

	if (currentStatus && terminalStatuses.has(currentStatus)) {
		return currentActivityByStage[currentStatus];
	}

	if (currentPhase && currentPhase !== "unknown") {
		return currentActivityByStage[currentPhase] || "Waiting for status update";
	}

	if (currentStatus) {
		return currentActivityByStage[currentStatus] || "Waiting for status update";
	}

	return "Waiting for status update";
}

function progressStatusVariant(status: string) {
	if (status === "failed") return "destructive";
	if (status === "succeeded") return "default";
	if (status === "canceled" || status === "unknown" || status === "idle") return "secondary";
	return "outline";
}

export function ProgressBadge({
	progress,
	status,
	message,
}: {
	progress?: NormalizedProgress;
	status?: string;
	message?: string;
}) {
	const currentStatus = progress?.status || status || "unknown";
	const currentActivity = getCurrentActivity(progress, status);
	const backendMessage = progress?.message || (progress ? undefined : message);
	const doneBytes = formatTransferBytes(progress?.doneBytes);
	const totalBytes = formatTransferBytes(progress?.totalBytes);
	const transferSpeed = formatTransferSpeed(progress?.bytesPerSecond);

	return (
		<div data-progress-region className="space-y-3">
			<Badge
				variant={progressStatusVariant(currentStatus)}
				className={`deploy-status-badge status-${currentStatus}`}
			>
				{currentStatus}
			</Badge>

			{progress && (
				<div data-progress-live className="space-y-2 text-sm text-muted-foreground">
					{progress.percent !== undefined && (
						<Progress value={Math.max(0, Math.min(100, progress.percent))} aria-label="Deployment progress" />
					)}
					{doneBytes && totalBytes && (
						<div>
							{doneBytes} / {totalBytes}
							{progress.percent !== undefined && ` (${Math.round(progress.percent)}%)`}
						</div>
					)}
					{doneBytes && !totalBytes && <div>{doneBytes} (Indeterminate)</div>}
					{transferSpeed && <div data-progress-speed>{transferSpeed}</div>}
				</div>
			)}

			<div data-progress-detail>
				<div>Current activity: {currentActivity}</div>
				{backendMessage && <div>Detail: {backendMessage}</div>}
			</div>
		</div>
	);
}
