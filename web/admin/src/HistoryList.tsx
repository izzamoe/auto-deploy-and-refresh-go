import { useCallback, useEffect, useMemo, useState } from "react";
import { useAdminEvents } from "./AdminEventProvider";
import { AdminAPIError, apiRequest, getJobLog } from "./api";
import { LogsDialog } from "./LogsDialog";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

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

interface RetryHistoryResponse {
	jobId?: string;
}

const terminalHistoryStatuses = new Set(["succeeded", "failed", "canceled", "unknown"]);

// HISTORY_PAGE_SIZE must match the backend default page size so the "Page X of
// Y" math lines up with what the API returns.
const HISTORY_PAGE_SIZE = 20;

function historyStatusVariant(status: string) {
	if (status === "failed") return "destructive";
	if (status === "succeeded") return "default";
	if (status === "canceled" || status === "unknown") return "secondary";
	return "outline";
}

export function HistoryList({ setFlash }: { setFlash: (flash: { message: string, type: "success" | "error" }) => void }) {
	const [history, setHistory] = useState<HistoryJob[]>([]);
	const [total, setTotal] = useState(0);
	const [page, setPage] = useState(1);
	const [logsJobId, setLogsJobId] = useState<string | null>(null);
	const [optimisticRetryIds, setOptimisticRetryIds] = useState<Set<string>>(() => new Set());
	const [retriedSourceIds, setRetriedSourceIds] = useState<Set<string>>(() => new Set());
	const { progress: liveProgress } = useAdminEvents();

	const [hash, setHash] = useState(() => window.location.hash);
	useEffect(() => {
		const onHashChange = () => setHash(window.location.hash);
		window.addEventListener("hashchange", onHashChange);
		return () => window.removeEventListener("hashchange", onHashChange);
	}, []);

	const appId = useMemo(() => {
		const searchParams = new URLSearchParams(hash.split("?")[1] || "");
		return searchParams.get("appId");
	}, [hash]);

	// Reset to the first page whenever the app filter changes so we never land
	// on a page that no longer exists for the newly selected app.
	useEffect(() => {
		setPage(1);
	}, [appId]);

	const loadHistory = useCallback(async () => {
		try {
			const params = new URLSearchParams();
			if (appId) params.set("appId", appId);
			params.set("page", String(page));
			params.set("pageSize", String(HISTORY_PAGE_SIZE));
			const res = await apiRequest(`/history?${params.toString()}`);
			setHistory(res.history || []);
			setTotal(typeof res.total === "number" ? res.total : (res.history || []).length);
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) {
				setFlash({ message: err.message, type: "error" });
			} else {
				setFlash({ message: String(err), type: "error" });
			}
		}
	}, [appId, page, setFlash]);

	useEffect(() => {
		loadHistory();
	}, [loadHistory]);

	const totalPages = Math.max(1, Math.ceil(total / HISTORY_PAGE_SIZE));

	const retryJob = async (job: HistoryJob) => {
		try {
			const res = await apiRequest(`/history/${job.id}/retry`, { method: "POST" }) as RetryHistoryResponse;
			if (res.jobId) {
				const now = new Date().toISOString();
				setRetriedSourceIds((ids) => new Set(ids).add(job.id));
				setOptimisticRetryIds((currentIds) => new Set(currentIds).add(res.jobId || ""));
				setHistory((currentHistory) => {
					if (currentHistory.some((historyJob) => historyJob.id === res.jobId)) return currentHistory;

					return [{
						...job,
						id: res.jobId || job.id,
						status: "pending",
						trigger: "manual_retry",
						errorMsg: "",
						createdAt: now,
						updatedAt: now,
					}, ...currentHistory];
				});
			} else {
				await loadHistory();
			}
			setFlash({ message: "Retry queued", type: "success" });
			window.setTimeout(() => {
				if (res.jobId) {
					setOptimisticRetryIds((currentIds) => {
						const nextIds = new Set(currentIds);
						nextIds.delete(res.jobId || "");
						return nextIds;
					});
				}
				void loadHistory();
			}, 1000);
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
		<div id="history-content" data-testid="history-content" className="space-y-6">
			<Card className="border-border/70 bg-card/95 shadow-sm">
				<CardHeader className="space-y-2">
					<CardTitle className="text-3xl font-bold tracking-tight">
						<h1>History</h1>
					</CardTitle>
					<CardDescription>
						Review deployment attempts, live statuses, and recovery actions without leaving the current history view.
					</CardDescription>
				</CardHeader>
			</Card>
			
			<Card id="history-table-region" data-testid="history-table-region" className="border-border/70 bg-card/80 shadow-sm">
				<CardContent className="space-y-4 p-4">
					{history.length === 0 && (
						<Alert>
							<AlertTitle>No deployment history yet</AlertTitle>
							<AlertDescription>
								Deployments will appear here after a webhook or manual release is queued.
							</AlertDescription>
						</Alert>
					)}

					<Table id="history-table" className="table">
						<TableHeader>
							<TableRow>
								<TableHead>Tag</TableHead>
								<TableHead>Status</TableHead>
								<TableHead>Trigger</TableHead>
								<TableHead>Error</TableHead>
								<TableHead>Created</TableHead>
								<TableHead>Updated</TableHead>
								<TableHead>Actions</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
						{history.map((job) => {
							const progress = liveProgress[job.id];
							const currentStatus = optimisticRetryIds.has(job.id) ? job.status : progress?.status || job.status;
							
							const showRetry = (currentStatus === "succeeded" || currentStatus === "failed") && !retriedSourceIds.has(job.id);
							const isTerminal = terminalHistoryStatuses.has(currentStatus);
							
							return (
								<TableRow key={job.id} id={`history-row-${job.id}`} data-status={currentStatus} data-job-id={job.id}>
									<TableCell className="font-medium">{job.tag}</TableCell>
									<TableCell>
										<Badge variant={historyStatusVariant(currentStatus)}>
											{currentStatus}
										</Badge>
									</TableCell>
									<TableCell>{job.trigger}</TableCell>
									<TableCell className="max-w-xs whitespace-normal text-muted-foreground">{job.errorMsg}</TableCell>
									<TableCell className="whitespace-nowrap">{new Date(job.createdAt).toLocaleString()}</TableCell>
									<TableCell className="whitespace-nowrap">{new Date(job.updatedAt).toLocaleString()}</TableCell>
									<TableCell>
										<div className="flex flex-wrap gap-2">
										{showRetry && (
											<Button
												type="button"
												variant="outline"
												size="sm"
												className="retry-button"
												data-testid={`retry-button-${job.id}`}
												onClick={() => retryJob(job)}
											>
												↻ Retry Deploy
											</Button>
										)}
										<Button
											type="button"
											variant="secondary"
											size="sm"
											data-testid={`logs-button-${job.id}`}
											onClick={() => setLogsJobId(job.id)}
										>
											View Logs
										</Button>
										{!isTerminal && (
											<Button type="button" variant="destructive" size="sm" onClick={() => cancelJob(job.id)}>Cancel</Button>
										)}
										</div>
									</TableCell>
								</TableRow>
							);
						})}
						</TableBody>
					</Table>

					{total > 0 && (
						<div
							id="history-pagination"
							data-testid="history-pagination"
							className="flex flex-col items-center justify-between gap-3 pt-2 sm:flex-row"
						>
							<p className="text-sm text-muted-foreground">
								Page {page} of {totalPages} · {total} total deployment{total === 1 ? "" : "s"}
							</p>
							<div className="flex gap-2">
								<Button
									type="button"
									variant="outline"
									size="sm"
									data-testid="history-prev-page"
									disabled={page <= 1}
									onClick={() => setPage((p) => Math.max(1, p - 1))}
								>
									← Previous
								</Button>
								<Button
									type="button"
									variant="outline"
									size="sm"
									data-testid="history-next-page"
									disabled={page >= totalPages}
									onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
								>
									Next →
								</Button>
							</div>
						</div>
					)}
				</CardContent>
			</Card>

			<LogsDialog
				open={logsJobId !== null}
				onOpenChange={(open) => { if (!open) setLogsJobId(null); }}
				title="Deploy logs"
				description="Service logs captured when this deploy finished — including the health-check failure output for a failed deploy."
				fetchLog={() => (logsJobId ? getJobLog(logsJobId) : Promise.resolve(""))}
			/>
		</div>
	);
}
