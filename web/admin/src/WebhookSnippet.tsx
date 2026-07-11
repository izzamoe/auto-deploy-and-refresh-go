import { useState } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";

function buildCurlSnippet(origin: string, secret: string): string {
	return `curl -s -w '\\n%{http_code}' -X POST ${origin}/webhook -H "Authorization: Bearer ${secret}" -H "Content-Type: application/json" -d '{"tag":"v1.0.0"}'`;
}

function buildGithubActionsSnippet(origin: string): string {
	return [
		"name: Deploy Webhook",
		"on:",
		"  release:",
		"    types: [published]",
		"jobs:",
		"  notify:",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - name: Trigger deploy webhook",
		"        run: |",
		`          curl -s -w '\\n%{http_code}' -X POST ${origin}/webhook \\`,
		'            -H "Authorization: Bearer ${{ secrets.DEPLOY_WEBHOOK_SECRET }}" \\',
		'            -H "Content-Type: application/json" \\',
		'            -d \'{"tag":"${{ github.event.release.tag_name }}"}\'',
	].join("\n");
}

function CopyableSnippet({ label, code, testId }: { label: string; code: string; testId: string }) {
	const [copied, setCopied] = useState(false);

	const handleCopy = async () => {
		try {
			await navigator.clipboard.writeText(code);
			setCopied(true);
			setTimeout(() => setCopied(false), 2000);
		} catch {
			setCopied(false);
		}
	};

	return (
		<div className="grid gap-2">
			<div className="flex items-center justify-between gap-3">
				<p className="text-sm font-medium">{label}</p>
				<Button type="button" size="sm" variant="outline" onClick={handleCopy}>
					{copied ? "Copied" : "Copy"}
				</Button>
			</div>
			<pre
				data-testid={testId}
				className="max-h-64 overflow-x-auto rounded-md bg-muted p-3 text-xs whitespace-pre-wrap break-all"
			>
				{code}
			</pre>
		</div>
	);
}

export function WebhookSnippet({ appName, secret, onDismiss }: { appName: string; secret: string; onDismiss: () => void }) {
	const origin = window.location.origin;
	const curlSnippet = buildCurlSnippet(origin, secret);
	const githubActionsSnippet = buildGithubActionsSnippet(origin);

	return (
		<Card className="border-border/70 bg-card/95 shadow-sm" data-testid="webhook-snippet">
			<CardHeader>
				<CardTitle className="text-xl font-semibold">Webhook secret for {appName}</CardTitle>
				<CardDescription>Use these snippets to test or automate deployments for this app.</CardDescription>
			</CardHeader>
			<CardContent className="grid gap-5">
				<Alert variant="destructive">
					<AlertDescription>
						This secret is shown once and will not be shown again. Copy it now and store it somewhere safe.
					</AlertDescription>
				</Alert>

				<CopyableSnippet label="Test webhook with curl" code={curlSnippet} testId="webhook-curl-snippet" />

				<CopyableSnippet
					label="GitHub Actions workflow (add secrets.DEPLOY_WEBHOOK_SECRET to your repo)"
					code={githubActionsSnippet}
					testId="webhook-gha-snippet"
				/>
			</CardContent>
			<CardFooter className="flex justify-end">
				<Button type="button" onClick={onDismiss}>
					Done
				</Button>
			</CardFooter>
		</Card>
	);
}
