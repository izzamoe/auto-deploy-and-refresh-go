import { useCallback, useEffect, useState } from "react";
import { useAdminEvents } from "./AdminEventProvider";
import { AdminAPIError, apiRequest } from "./api";
import { ProgressBadge } from "./ProgressBadge";

export interface AppItem {
	id: string;
	name: string;
	serviceName: string;
	githubRepo: string;
	binaryPath: string;
	artifactName: string;
	enabled: boolean;
	lastJobId?: string;
	lastJobStatus?: string;
	lastDeployStatus?: string;
}

export function AppsList({ setFlash }: { setFlash: (flash: { message: string, type: "success" | "error" }) => void }) {
	const [apps, setApps] = useState<AppItem[]>([]);
	const { progress: liveProgress } = useAdminEvents();
	const [tagMap, setTagMap] = useState<Record<string, string>>({});

	const loadApps = useCallback(async () => {
		try {
			const res = await apiRequest("/apps");
			setApps(res.apps || []);
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) {
				setFlash({ message: err.message, type: "error" });
			} else {
				setFlash({ message: String(err), type: "error" });
			}
		}
	}, [setFlash]);

	useEffect(() => {
		loadApps();
	}, [loadApps]);

	const disableApp = async (id: string) => {
		try {
			await apiRequest(`/apps/${id}/toggle`, { method: "POST", body: JSON.stringify({ enabled: false }) });
			setFlash({ message: "App disabled successfully", type: "success" });
			loadApps();
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) setFlash({ message: err.message, type: "error" });
		}
	};

	const enableApp = async (id: string) => {
		try {
			await apiRequest(`/apps/${id}/toggle`, { method: "POST", body: JSON.stringify({ enabled: true }) });
			setFlash({ message: "App enabled successfully", type: "success" });
			loadApps();
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) setFlash({ message: err.message, type: "error" });
		}
	};

	const deleteApp = async (id: string) => {
		if (!window.confirm("Are you sure you want to delete this app?")) return;
		try {
			await apiRequest(`/apps/${id}`, { method: "DELETE" });
			setFlash({ message: "App deleted successfully", type: "success" });
			loadApps();
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) setFlash({ message: err.message, type: "error" });
		}
	};

	const deployApp = async (id: string, e: React.FormEvent) => {
		e.preventDefault();
		const tag = tagMap[id];
		if (!tag) return;
		try {
			await apiRequest(`/apps/${id}/deploy`, {
				method: "POST",
				body: JSON.stringify({ tag }),
			});
			setFlash({ message: `Manual deploy queued for ${tag}`, type: "success" });
			loadApps();
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) setFlash({ message: err.message, type: "error" });
		}
	};

	const cancelAppJobs = async (id: string) => {
		if (!window.confirm("Cancel all pending/running deployments for this app?")) return;
		try {
			await apiRequest(`/apps/${id}/cancel`, { method: "POST" });
			setFlash({ message: "Cancel requested", type: "success" });
			loadApps();
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) setFlash({ message: err.message, type: "error" });
		}
	};

	return (
		<div>
			<div className="apps-header">
				<h1>Apps</h1>
				<a href="#/apps/new" className="button">Add New App</a>
			</div>
			
			<div className="apps-grid" id="apps-table">
				{apps.map((app) => {
					const progress = app.lastJobId ? liveProgress[app.lastJobId] : undefined;
					const currentStatus = progress?.status || app.lastJobStatus;
					const isTerminal = ["succeeded", "failed", "canceled", "unknown"].includes(currentStatus || "");
					const progressProps = !isTerminal && app.lastJobId ? { "data-progress-job": app.lastJobId, "data-job-id": app.lastJobId } : {};
					
					return (
						<div key={app.id} id={`app-card-${app.id}`} data-app-id={app.id} {...progressProps} className="app-card">
							<h2>{app.name}</h2>
							<div>{app.enabled ? "Active" : "Disabled"}</div>
							<p>Service: {app.serviceName}</p>
							<p>Repo: {app.githubRepo}</p>
							<p>Binary: {app.binaryPath}</p>
							<p>Artifact: {app.artifactName}</p>
							
							<ProgressBadge progress={progress} status={app.lastJobStatus} message={app.lastDeployStatus} />
							
							<div className="actions">
								<a href={`#/apps/${app.id}/edit`}>Edit</a>
								<a href={`#/history?appId=${app.id}`}>History</a>
								
								{app.enabled ? (
									<button type="button" onClick={() => disableApp(app.id)}>Disable</button>
								) : (
									<button type="button" onClick={() => enableApp(app.id)}>Enable</button>
								)}
								
								<button type="button" onClick={() => deleteApp(app.id)}>Delete</button>
								{!isTerminal && (
									<button type="button" onClick={() => cancelAppJobs(app.id)}>Cancel Deploy</button>
								)}
							</div>

							{app.enabled && (
								<form onSubmit={(e) => deployApp(app.id, e)}>
									<input 
										type="hidden" 
										name="source" 
										value="list" 
									/>
									<input 
										type="text" 
										name="tag" 
										placeholder="Tag (e.g. v1.2.3)" 
										required 
										value={tagMap[app.id] || ""}
										onChange={(e) => setTagMap({ ...tagMap, [app.id]: e.target.value })}
									/>
									<button type="submit">Deploy</button>
								</form>
							)}
						</div>
					);
				})}
			</div>
		</div>
	);
}
