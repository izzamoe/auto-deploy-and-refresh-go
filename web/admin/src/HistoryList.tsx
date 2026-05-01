import { useCallback, useEffect, useState } from "react";
import { useAdminEvents } from "./AdminEventProvider";
import { AdminAPIError, apiRequest } from "./api";

export interface HistoryJob {
	id: string;
	tag: string;
	status: string;
	trigger: string;
	errorMsg: string;
	createdAt: string;
	updatedAt: string;
	appEnabled: boolean;
}

export function HistoryList({ setFlash }: { setFlash: (flash: { message: string, type: "success" | "error" }) => void }) {
	const [history, setHistory] = useState<HistoryJob[]>([]);
	const { progress: liveProgress } = useAdminEvents();
	
	const searchParams = new URLSearchParams(window.location.hash.split("?")[1] || "");
	const appId = searchParams.get("appId");
	
	const loadHistory = useCallback(async () => {
		try {
			const url = appId ? `/history?appId=${appId}` : "/history";
			const res = await apiRequest(url);
			setHistory(res.history || []);
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) {
				setFlash({ message: err.message, type: "error" });
			} else {
				setFlash({ message: String(err), type: "error" });
			}
		}
	}, [appId, setFlash]);

	useEffect(() => {
		loadHistory();
	}, [loadHistory]);

	const retryJob = async (jobId: string) => {
		try {
			await apiRequest(`/history/${jobId}/retry`, { method: "POST" });
			setFlash({ message: "Retry queued", type: "success" });
			loadHistory();
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) setFlash({ message: err.message, type: "error" });
		}
	};

	const cancelJob = async (jobId: string) => {
		try {
			await apiRequest(`/jobs/${jobId}/cancel`, { method: "POST" });
			setFlash({ message: "Cancel requested", type: "success" });
			loadHistory();
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) setFlash({ message: err.message, type: "error" });
		}
	};

	return (
		<div id="history-content" data-testid="history-content">
			<h1>History</h1>
			
			<div id="history-table-region" data-testid="history-table-region">
				<table id="history-table" className="table">
					<thead>
						<tr>
							<th>Tag</th>
							<th>Status</th>
							<th>Trigger</th>
							<th>Error</th>
							<th>Created</th>
							<th>Updated</th>
							<th>Actions</th>
						</tr>
					</thead>
					<tbody>
						{history.map((job) => {
							const progress = liveProgress[job.id];
							const currentStatus = progress?.status || job.status;
							
							const showRetry = currentStatus === "succeeded" || currentStatus === "failed";
							const isTerminal = ["succeeded", "failed", "canceled", "unknown"].includes(currentStatus);
							
							return (
								<tr key={job.id} id={`history-row-${job.id}`} data-status={currentStatus} data-job-id={job.id}>
									<td>{job.tag}</td>
									<td>{currentStatus}</td>
									<td>{job.trigger}</td>
									<td>{job.errorMsg}</td>
									<td>{new Date(job.createdAt).toLocaleString()}</td>
									<td>{new Date(job.updatedAt).toLocaleString()}</td>
									<td>
										{showRetry && (
											<button 
												type="button"
												className="retry-button" 
												data-testid={`retry-button-${job.id}`}
												onClick={() => retryJob(job.id)}
											>
												↻ Retry Deploy
											</button>
										)}
										{!isTerminal && (
											<button type="button" onClick={() => cancelJob(job.id)}>Cancel</button>
										)}
									</td>
								</tr>
							);
						})}
					</tbody>
				</table>
			</div>
		</div>
	);
}
