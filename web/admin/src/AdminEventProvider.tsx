import type { PropsWithChildren } from "react";
import { createContext, useContext, useEffect, useRef, useState } from "react";

declare global {
	interface Window {
		AdminUITest?: {
			injectEvent: (data: string) => void;
		};
	}
}

export type NormalizedProgress = {
	appId: string;
	jobId: string;
	phase: string;
	status: string;
	percent?: number;
	doneBytes?: number;
	totalBytes?: number;
	bytesPerSecond?: number;
	message?: string;
};

export type AdminEvent =
	| { type: "hello"; payload?: unknown }
	| {
			type: "snapshot";
			payload?: Array<{
				AppID?: string;
				appId?: string;
				JobID?: string;
				jobId?: string;
				Phase?: string;
				phase?: string;
				Status?: string;
				status?: string;
				Percent?: number;
				percent?: number;
				TotalBytes?: number;
				totalBytes?: number;
				DownloadedBytes?: number;
				downloadedBytes?: number;
				SpeedBPS?: number;
				speedBPS?: number;
				Message?: string;
				message?: string;
			}>;
	  }
	| { type: "job_status"; payload?: { jobId: string; status: string } }
	| { type: "cancel_requested"; payload?: { jobId: string } }
	| { type: "notice"; payload?: unknown }
	| { type: "heartbeat"; payload?: unknown };

interface AdminEventState {
	connected: boolean;
	events: AdminEvent[];
	progress: Record<string, NormalizedProgress>; // keyed by jobId
}

const initialState: AdminEventState = {
	connected: false,
	events: [],
	progress: {},
};

const AdminEventContext = createContext<AdminEventState>(initialState);

export const useAdminEvents = () => useContext(AdminEventContext);

export function AdminEventProvider({ children }: PropsWithChildren) {
	const [state, setState] = useState<AdminEventState>(initialState);
	const wsRef = useRef<WebSocket | null>(null);
	const reconnectTimeoutRef = useRef<number | null>(null);
	const backoffRef = useRef(1000);

	useEffect(() => {
		let unmounted = false;

		const connect = () => {
			if (unmounted) return;

			const wsProtocol = window.location.protocol === "https:" ? "wss:" : "ws:";
			const wsUrl = `${wsProtocol}//${window.location.host}/admin/events/ws`;

			const ws = new WebSocket(wsUrl);
			wsRef.current = ws;

			ws.onopen = () => {
				if (unmounted) {
					ws.close();
					return;
				}
				backoffRef.current = 1000;
				setState((s) => ({ ...s, connected: true }));
			};

			ws.onmessage = (event) => {
				if (unmounted) return;
				try {
					const data = JSON.parse(event.data) as Record<string, unknown>;

					if (data.t === "p") {
						// Compact progress
						const normalized: NormalizedProgress = {
							appId: String(data.a),
							jobId: String(data.j),
							phase: String(data.ph),
							status: String(data.st),
							percent: typeof data.pct === "number" ? data.pct : undefined,
							doneBytes: typeof data.db === "number" ? data.db : undefined,
							totalBytes: typeof data.tb === "number" ? data.tb : undefined,
							bytesPerSecond:
								typeof data.bps === "number" ? data.bps : undefined,
							message: typeof data.msg === "string" ? data.msg : undefined,
						};

						setState((s) => ({
							...s,
							progress: {
								...s.progress,
								[normalized.jobId]: normalized,
							},
						}));
					} else if (typeof data.type === "string") {
						// Readable event
						const type = data.type;
						const knownTypes = [
							"hello",
							"snapshot",
							"job_status",
							"cancel_requested",
							"notice",
							"heartbeat",
						];
						if (knownTypes.includes(type)) {
							const adminEvent = data as unknown as AdminEvent;

							setState((s) => {
								const nextState = {
									...s,
									events: [...s.events, adminEvent],
									progress: { ...s.progress },
								};

								// Normalize state based on readable events
								if (
									adminEvent.type === "snapshot" &&
									Array.isArray(adminEvent.payload)
								) {
									for (const job of adminEvent.payload) {
										const appId = job.AppID ?? job.appId;
										const jobId = job.JobID ?? job.jobId;
										if (jobId && appId) {
											const tb =
												typeof job.TotalBytes === "number"
													? job.TotalBytes
													: typeof job.totalBytes === "number"
														? job.totalBytes
														: undefined;
											const pct =
												typeof job.Percent === "number"
													? job.Percent
													: typeof job.percent === "number"
														? job.percent
														: undefined;

											const phase = String(job.Phase ?? job.phase ?? "unknown");
											const status = String(job.Status ?? job.status ?? phase);

											nextState.progress[jobId] = {
												appId: String(appId),
												jobId: String(jobId),
												phase,
												status,
												percent:
													pct !== undefined && pct >= 0 ? pct : undefined,
												totalBytes: tb !== undefined && tb > 0 ? tb : undefined,
												doneBytes:
													typeof job.DownloadedBytes === "number"
														? job.DownloadedBytes
														: typeof job.downloadedBytes === "number"
															? job.downloadedBytes
															: undefined,
												bytesPerSecond:
													typeof job.SpeedBPS === "number"
														? job.SpeedBPS
														: typeof job.speedBPS === "number"
															? job.speedBPS
															: undefined,
												message:
													typeof job.Message === "string"
														? job.Message
														: typeof job.message === "string"
															? job.message
															: undefined,
											};
										}
									}
								} else if (
									adminEvent.type === "job_status" &&
									adminEvent.payload?.jobId
								) {
									const job = nextState.progress[adminEvent.payload.jobId];
									if (job) {
										nextState.progress[adminEvent.payload.jobId] = {
											...job,
											status: adminEvent.payload.status,
										};
									}
								} else if (
									adminEvent.type === "cancel_requested" &&
									adminEvent.payload?.jobId
								) {
									const job = nextState.progress[adminEvent.payload.jobId];
									if (job) {
										nextState.progress[adminEvent.payload.jobId] = {
											...job,
											status: "cancel_requested",
										};
									}
								}

								return nextState;
							});
						}
					}
				} catch {
					// Ignore parse errors or unknown frames
				}
			};

			ws.onclose = () => {
				if (unmounted) return;
				setState((s) => ({ ...s, connected: false }));
				// Reconnect with backoff
				if (reconnectTimeoutRef.current !== null) {
					window.clearTimeout(reconnectTimeoutRef.current);
				}
				reconnectTimeoutRef.current = window.setTimeout(
					connect,
					backoffRef.current,
				);
				backoffRef.current = Math.min(backoffRef.current * 2, 30000);
			};

			ws.onerror = () => {
				// Handled by close
			};

			if (import.meta.env.MODE !== "production") {
				window.AdminUITest = {
					injectEvent: (data: string) => {
						if (ws.onmessage) {
							ws.onmessage(new MessageEvent("message", { data }));
						}
					},
				};
			}
		};

		connect();

		return () => {
			unmounted = true;
			if (reconnectTimeoutRef.current !== null) {
				window.clearTimeout(reconnectTimeoutRef.current);
			}
			if (wsRef.current) {
				wsRef.current.close();
			}
		};
	}, []);

	return (
		<AdminEventContext.Provider value={state}>
			{children}
		</AdminEventContext.Provider>
	);
}
