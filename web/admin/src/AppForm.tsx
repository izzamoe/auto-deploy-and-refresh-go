import { useEffect, useState } from "react";
import { AdminAPIError, apiRequest, envVarsToText, getAppEnv, parseEnvText, saveAppEnv } from "./api";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { WebhookSnippet } from "./WebhookSnippet";

export function AppForm({ id, navigate, setFlash }: { id?: string; navigate: (hash: string, flashMessage?: string) => void; setFlash: (flash: { message: string, type: "success" | "error" }) => void }) {
	const [formData, setFormData] = useState({
		name: "",
		webhook_secret: "",
		service_name: "",
		binary_path: "",
		github_repo: "",
		artifact_name: "",
		enabled: true
	});
	const [errors, setErrors] = useState<string[]>([]);
	const [createdApp, setCreatedApp] = useState<{ name: string; secret: string } | null>(null);
	const [envText, setEnvText] = useState("");

	useEffect(() => {
		if (id) {
			apiRequest(`/apps/${id}`).then(res => {
				setFormData({
					name: res.app.name,
					webhook_secret: "",
					service_name: res.app.serviceName,
					binary_path: res.app.binaryPath,
					github_repo: res.app.githubRepo,
					artifact_name: res.app.artifactName,
					enabled: res.app.enabled
				});
			}).catch(err => {
				if (err instanceof AdminAPIError) {
					setFlash({ message: err.message, type: "error" });
				} else {
					setFlash({ message: String(err), type: "error" });
				}
				navigate("#/");
			});
			getAppEnv(id)
				.then((vars) => setEnvText(envVarsToText(vars)))
				.catch(() => { /* non-fatal: leave the env editor empty */ });
		}
	}, [id, navigate, setFlash]);

	const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
		setFormData({ ...formData, [e.target.name]: e.target.value });
	};

	const handleEnabledChange = (enabled: boolean) => {
		setFormData({ ...formData, enabled });
	};

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault();
		setErrors([]);
		try {
			const body = {
				name: formData.name,
				webhookSecret: formData.webhook_secret,
				serviceName: formData.service_name,
				binaryPath: formData.binary_path,
				githubRepo: formData.github_repo,
				artifactName: formData.artifact_name,
				enabled: formData.enabled
			};
			
			const envVars = parseEnvText(envText);
			if (id) {
				await apiRequest(`/apps/${id}`, {
					method: "PUT",
					body: JSON.stringify(body)
				});
				await saveAppEnv(id, envVars);
				navigate("#/", "App updated successfully");
			} else {
				const res = await apiRequest(`/apps`, {
					method: "POST",
					body: JSON.stringify(body)
				});
				if (envVars.length > 0 && res.app?.id) {
					await saveAppEnv(res.app.id, envVars);
				}
				setCreatedApp({ name: formData.name, secret: formData.webhook_secret });
			}
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) {
				const msgs = Array.isArray(err.errors) && err.errors.length > 0 ? err.errors : [err.message || "An error occurred"];
				setErrors(msgs);
			} else {
				setErrors([String(err)]);
			}
		}
	};

	return (
		<div className="space-y-6">
			<Card className="border-border/70 bg-card/95 shadow-sm">
				<CardHeader className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
					<div className="space-y-2">
						<CardTitle className="text-3xl font-bold tracking-tight">
							<h1>{id ? "Edit App" : "New App"}</h1>
						</CardTitle>
						<CardDescription>
							{id ? "Update deployment settings while keeping the current webhook secret unless replaced." : "Register a service and release artifact for automated deployments."}
						</CardDescription>
					</div>
					<Button variant="outline" asChild>
						<a href="#/">Back to Apps</a>
					</Button>
				</CardHeader>
			</Card>

			{errors.length > 0 && (
				<Alert id="form-errors" variant="destructive">
					<AlertDescription className="space-y-1">
						{errors.map((errStr) => <div key={errStr}>{errStr}</div>)}
					</AlertDescription>
				</Alert>
			)}

			{createdApp && (
				<WebhookSnippet
					appName={createdApp.name}
					secret={createdApp.secret}
					onDismiss={() => navigate("#/", "App created successfully")}
				/>
			)}

			{!createdApp && <Card className="border-border/70 bg-card/80 shadow-sm">
				<form id="app-form" onSubmit={handleSubmit}>
					<CardContent className="grid gap-5 p-6">
						<div className="grid gap-2">
							<Label htmlFor="name">App Name</Label>
							<Input type="text" id="name" name="name" value={formData.name} onChange={handleChange} required />
						</div>

						<div className="grid gap-2">
							<Label htmlFor="webhook_secret">Webhook Secret</Label>
							<Input
								type="password"
								id="webhook_secret"
								name="webhook_secret"
								value={formData.webhook_secret}
								onChange={handleChange}
								required={!id}
								placeholder={id ? "Leave blank to keep current secret" : ""}
							/>
						</div>

						<div className="grid gap-2 sm:grid-cols-2 sm:gap-4">
							<div className="grid gap-2">
								<Label htmlFor="service_name">Service Name</Label>
								<Input type="text" id="service_name" name="service_name" value={formData.service_name} onChange={handleChange} required />
							</div>

							<div className="grid gap-2">
								<Label htmlFor="binary_path">Binary Path</Label>
								<Input type="text" id="binary_path" name="binary_path" value={formData.binary_path} onChange={handleChange} required />
							</div>
						</div>

						<div className="grid gap-2 sm:grid-cols-2 sm:gap-4">
							<div className="grid gap-2">
								<Label htmlFor="github_repo">GitHub Repo</Label>
								<Input type="text" id="github_repo" name="github_repo" value={formData.github_repo} onChange={handleChange} required />
							</div>

							<div className="grid gap-2">
								<Label htmlFor="artifact_name">Artifact Name</Label>
								<Input type="text" id="artifact_name" name="artifact_name" value={formData.artifact_name} onChange={handleChange} required />
							</div>
						</div>

						<div className="grid gap-2">
							<Label htmlFor="env_vars">Environment Variables</Label>
							<textarea
								id="env_vars"
								name="env_vars"
								value={envText}
								onChange={(e) => setEnvText(e.target.value)}
								rows={4}
								spellCheck={false}
								placeholder={"BOT_TOKEN=123:abc\nLOG_LEVEL=info"}
								className="flex min-h-[80px] w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
							/>
							<p className="text-sm text-muted-foreground">
								One <code>NAME=VALUE</code> per line (e.g. the bot reads <code>BOT_TOKEN</code> from its
								environment). Injected into the systemd unit — run <strong>Generate/Apply service unit</strong> and
								restart the service for changes to take effect.
							</p>
						</div>

						{id && (
							<div className="flex items-center justify-between rounded-lg border bg-background/80 p-4">
								<div className="space-y-0.5">
									<Label htmlFor="enabled">Enable deployments</Label>
									<p className="text-sm text-muted-foreground">
										Disabled apps stay registered but ignore webhook and manual deploy actions.
									</p>
								</div>
								<Switch
									id="enabled"
									name="enabled"
									checked={formData.enabled}
									onCheckedChange={handleEnabledChange}
									aria-label="Enable deployments"
								/>
							</div>
						)}
					</CardContent>

					<CardFooter className="form-actions flex flex-col-reverse items-stretch gap-3 sm:flex-row sm:justify-end">
						<Button variant="outline" asChild>
							<a href="#/">Cancel</a>
						</Button>
						<Button type="submit" id="submit-btn">{id ? "Update App" : "Create App"}</Button>
					</CardFooter>
				</form>
				</Card>}
		</div>
	);
}
