import type { FormEvent } from "react";
import { useCallback, useEffect, useState } from "react";
import { useAdminEvents } from "./AdminEventProvider";
import { AdminAPIError, apiRequest } from "./api";
import { ProgressBadge } from "./ProgressBadge";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";

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

interface AppsListProps {
	setFlash: (flash: { message: string, type: "success" | "error" }) => void;
	navigate?: (hash: string, flashMessage?: string) => void;
}

const terminalDeployStatuses = new Set(["succeeded", "failed", "canceled", "unknown"]);

export function AppsList({ setFlash }: AppsListProps) {
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
		try {
			await apiRequest(`/apps/${id}`, { method: "DELETE" });
			setFlash({ message: "App deleted successfully", type: "success" });
			loadApps();
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) setFlash({ message: err.message, type: "error" });
		}
	};

	const deployApp = async (id: string, e: FormEvent) => {
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
		try {
			await apiRequest(`/apps/${id}/cancel`, { method: "POST" });
			setFlash({ message: "Cancel requested", type: "success" });
			loadApps();
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) setFlash({ message: err.message, type: "error" });
		}
	};

	return (
		<div className="space-y-6">
			<Card className="apps-header overflow-hidden border-border/70 bg-card/95 shadow-sm">
				<CardHeader className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
					<div className="space-y-2">
						<CardTitle className="text-3xl font-bold tracking-tight">
							<h1>Apps</h1>
						</CardTitle>
						<CardDescription>
							Manage registered services, deployment actions, and live release progress.
						</CardDescription>
					</div>
					<Button asChild>
						<a href="#/apps/new">Add New App</a>
					</Button>
				</CardHeader>
			</Card>

			<Card className="apps-grid border-border/70 bg-card/80 shadow-sm" id="apps-table">
				<CardContent className="grid gap-4 p-4 md:grid-cols-2">
				{apps.map((app) => {
					const progress = app.lastJobId ? liveProgress[app.lastJobId] : undefined;
					const currentStatus = progress?.status || app.lastJobStatus;
					const isTerminal = terminalDeployStatuses.has(currentStatus || "");
					const progressProps = !isTerminal && app.lastJobId ? { "data-progress-job": app.lastJobId, "data-job-id": app.lastJobId } : {};
					const statusText = app.enabled ? "Active" : "Disabled";
					
					return (
						<Card key={app.id} id={`app-card-${app.id}`} data-app-id={app.id} {...progressProps} className="app-card flex h-full flex-col border-border/70 bg-background/95 shadow-sm">
							<CardHeader className="flex flex-row items-start justify-between gap-4 space-y-0">
								<div className="min-w-0 space-y-1">
									<CardTitle className="text-xl leading-tight">
										<h2 className="truncate">{app.name}</h2>
									</CardTitle>
									<CardDescription className="truncate">{app.serviceName}</CardDescription>
								</div>
								<Badge variant={app.enabled ? "default" : "secondary"} className="shrink-0">
									{statusText}
								</Badge>
							</CardHeader>

							<CardContent className="flex-1 space-y-4">
								<div className="grid gap-2 text-sm text-muted-foreground">
									<p><span className="font-medium text-foreground">Service:</span> {app.serviceName}</p>
									<p><span className="font-medium text-foreground">Repo:</span> {app.githubRepo}</p>
									<p><span className="font-medium text-foreground">Binary:</span> {app.binaryPath}</p>
									<p><span className="font-medium text-foreground">Artifact:</span> {app.artifactName}</p>
								</div>

								<Separator />

								<ProgressBadge progress={progress} status={app.lastJobStatus} message={app.lastDeployStatus} />
							</CardContent>

							<CardFooter className="flex flex-col items-stretch gap-4">
								<div className="actions flex flex-wrap gap-2">
									<Button variant="outline" size="sm" asChild>
										<a href={`#/apps/${app.id}/edit`}>Edit</a>
									</Button>
									<Button variant="outline" size="sm" asChild>
										<a href={`#/history?appId=${app.id}`}>History</a>
									</Button>

									{app.enabled ? (
										<Button type="button" variant="secondary" size="sm" onClick={() => disableApp(app.id)}>Disable</Button>
									) : (
										<Button type="button" variant="secondary" size="sm" onClick={() => enableApp(app.id)}>Enable</Button>
									)}

									<Dialog>
										<DialogTrigger asChild>
											<Button type="button" variant="destructive" size="sm">Delete</Button>
										</DialogTrigger>
										<DialogContent>
											<DialogHeader>
												<DialogTitle>Delete app?</DialogTitle>
												<DialogDescription>Are you sure you want to delete this app?</DialogDescription>
											</DialogHeader>
											<DialogFooter>
												<DialogClose asChild>
													<Button type="button" variant="outline">Keep App</Button>
												</DialogClose>
												<DialogClose asChild>
													<Button type="button" variant="destructive" onClick={() => deleteApp(app.id)} aria-label="Confirm delete app">Delete</Button>
												</DialogClose>
											</DialogFooter>
										</DialogContent>
									</Dialog>

									{!isTerminal && (
										<Dialog>
											<DialogTrigger asChild>
												<Button type="button" variant="outline" size="sm">Cancel Deploy</Button>
											</DialogTrigger>
											<DialogContent>
												<DialogHeader>
													<DialogTitle>Cancel deployments?</DialogTitle>
													<DialogDescription>Cancel all pending/running deployments for this app?</DialogDescription>
												</DialogHeader>
												<DialogFooter>
													<DialogClose asChild>
														<Button type="button" variant="outline">Keep Deploying</Button>
													</DialogClose>
													<DialogClose asChild>
														<Button type="button" variant="destructive" onClick={() => cancelAppJobs(app.id)} aria-label="Confirm cancel deploy">Cancel Deploy</Button>
													</DialogClose>
												</DialogFooter>
											</DialogContent>
										</Dialog>
									)}
								</div>

								{app.enabled && (
									<>
										<Separator />
										<form className="flex flex-col gap-2 sm:flex-row" onSubmit={(e) => deployApp(app.id, e)}>
											<input 
												type="hidden" 
												name="source" 
												value="list" 
											/>
											<Input 
												type="text" 
												name="tag" 
												placeholder="Tag (e.g. v1.2.3)" 
												required 
												value={tagMap[app.id] || ""}
												onChange={(e) => setTagMap({ ...tagMap, [app.id]: e.target.value })}
												className="sm:flex-1"
											/>
											<Button type="submit">Deploy</Button>
										</form>
									</>
								)}
							</CardFooter>
						</Card>
					);
				})}
				</CardContent>
			</Card>
		</div>
	);
}
