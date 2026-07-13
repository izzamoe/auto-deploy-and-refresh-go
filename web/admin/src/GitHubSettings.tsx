import { useEffect, useState } from "react";
import { AdminAPIError, getGitHubConfig, saveGitHubConfig, type GitHubConfig } from "./api";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

function sourceLabel(source: GitHubConfig["source"]) {
	switch (source) {
		case "database":
			return { text: "Saved in database", variant: "default" as const };
		case "environment":
			return { text: "From GITHUB_TOKEN env", variant: "secondary" as const };
		default:
			return { text: "Not configured (anonymous, 60 req/hour)", variant: "outline" as const };
	}
}

export function GitHubSettings({ setFlash }: { setFlash: (flash: { message: string, type: "success" | "error" }) => void }) {
	const [token, setToken] = useState("");
	const [savedToken, setSavedToken] = useState("");
	const [source, setSource] = useState<GitHubConfig["source"]>("none");
	const [errors, setErrors] = useState<string[]>([]);
	const [loading, setLoading] = useState(true);

	useEffect(() => {
		getGitHubConfig().then(cfg => {
			setToken(cfg.token || "");
			setSavedToken(cfg.token || "");
			setSource(cfg.source);
			setLoading(false);
		}).catch(err => {
			if (err instanceof AdminAPIError) {
				setFlash({ message: err.message, type: "error" });
			} else {
				setFlash({ message: String(err), type: "error" });
			}
			setLoading(false);
		});
	}, [setFlash]);

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault();
		setErrors([]);
		try {
			// Only send a new token if the user actually typed a different value.
			const nextToken = token === savedToken ? "" : token;
			const saved = await saveGitHubConfig(nextToken);
			setToken(saved.token || "");
			setSavedToken(saved.token || "");
			setSource(saved.source);
			setFlash({ message: "GitHub token saved successfully", type: "success" });
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) {
				const msgs = Array.isArray(err.errors) && err.errors.length > 0 ? err.errors : [err.message || "An error occurred"];
				setErrors(msgs);
			} else {
				setErrors([String(err)]);
			}
		}
	};

	if (loading) {
		return null;
	}

	const label = sourceLabel(source);

	return (
		<div className="space-y-6">
			<Card className="border-border/70 bg-card/95 shadow-sm">
				<CardHeader className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
					<div className="space-y-2">
						<CardTitle className="text-3xl font-bold tracking-tight">
							<h1>GitHub Access</h1>
						</CardTitle>
						<CardDescription>
							Personal access token used to read release tags from the GitHub API. Authenticating
							raises the rate limit from 60 to 5000 requests/hour and allows reading private repos.
							Saving here stores the token in the database, so it no longer needs to be set via the
							GITHUB_TOKEN environment variable.
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

			<Card className="border-border/70 bg-card/80 shadow-sm">
				<form id="github-form" onSubmit={handleSubmit}>
					<CardContent className="grid gap-5 p-6">
						<div className="flex items-center gap-2">
							<span className="text-sm text-muted-foreground">Current status:</span>
							<Badge variant={label.variant} data-testid="github-token-source">{label.text}</Badge>
						</div>

						<div className="grid gap-2">
							<Label htmlFor="token">Personal Access Token</Label>
							<Input
								type="password"
								id="token"
								name="token"
								value={token}
								onChange={(e) => setToken(e.target.value)}
								placeholder="ghp_… or github_pat_… (leave unchanged to keep current token)"
							/>
							<p className="text-sm text-muted-foreground">
								A fine-grained token with read-only access to the repositories you deploy is enough.
							</p>
						</div>
					</CardContent>

					<CardFooter className="form-actions flex flex-col-reverse items-stretch gap-3 sm:flex-row sm:justify-end">
						<Button variant="outline" asChild>
							<a href="#/">Cancel</a>
						</Button>
						<Button type="submit" id="submit-btn">Save</Button>
					</CardFooter>
				</form>
			</Card>
		</div>
	);
}
