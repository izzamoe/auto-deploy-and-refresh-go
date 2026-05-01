import type { NormalizedProgress } from "./AdminEventProvider";

const progressStageDetails: Record<string, string> = {
	queued: "Waiting for an available deploy slot.",
	downloading: "Receiving release artifact.",
	validating: "Validating downloaded artifact.",
	backing_up: "Backing up current binary.",
	installing: "Download complete. Restarting service and verifying health.",
	restarting: "Restarting service.",
	healthcheck: "Verifying service health.",
	rollback: "Restoring the previous version.",
	succeeded: "Deployment completed successfully.",
	failed: "Deployment failed."
};

export function ProgressBadge({ 
	progress, 
	status, 
	message 
}: { 
	progress?: NormalizedProgress; 
	status?: string; 
	message?: string; 
}) {
	const currentStatus = progress?.status || status || "unknown";
	const isTerminal = ["succeeded", "failed", "canceled", "unknown"].includes(currentStatus);
	const phase = progress?.phase || "";
	
	const stageKey = phase || currentStatus;
	const detailFallback = progressStageDetails[stageKey] || stageKey;
	const displayMessage = progress?.message || (progress ? undefined : message) || detailFallback;
	
	const formatBytes = (bytes: number) => {
		const mb = bytes / (1024 * 1024);
		return `${mb.toFixed(2)} MB`;
	};

	return (
		<div data-progress-region>
			<span className={`deploy-status-badge status-${currentStatus}`}>
				{currentStatus}
			</span>
			
			{!isTerminal && progress && (
				<div data-progress-live>
					<p>{phase}</p>
					{progress.doneBytes !== undefined && progress.totalBytes !== undefined && (
						<div>
							{formatBytes(progress.doneBytes)} / {formatBytes(progress.totalBytes)}
							{progress.percent !== undefined && ` (${Math.round(progress.percent)}%)`}
						</div>
					)}
					{progress.doneBytes !== undefined && progress.totalBytes === undefined && (
						<div>
							{formatBytes(progress.doneBytes)} (Indeterminate)
						</div>
					)}
					{progress.bytesPerSecond !== undefined && (
						<div>{formatBytes(progress.bytesPerSecond)}/s</div>
					)}
				</div>
			)}

			{(displayMessage) && (
				<div data-progress-detail>
					{displayMessage}
				</div>
			)}
		</div>
	);
}
