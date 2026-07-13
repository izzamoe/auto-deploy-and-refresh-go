import { useCallback, useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";

import { getSystemLogs } from "./api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

// LINE_OPTIONS mirror the live-log windows offered elsewhere. 0 means "all".
const LINE_OPTIONS = [
	{ value: 50, label: "50 lines" },
	{ value: 100, label: "100 lines" },
	{ value: 500, label: "500 lines" },
	{ value: 0, label: "All" },
];

export function SystemLogs({ setFlash }: { setFlash: (flash: { message: string, type: "success" | "error" }) => void }) {
	const [log, setLog] = useState("");
	const [loading, setLoading] = useState(true);
	const [lines, setLines] = useState(100);

	const load = useCallback((n: number) => {
		setLoading(true);
		getSystemLogs(n)
			.then(setLog)
			.catch((err) => setFlash({ message: String(err), type: "error" }))
			.finally(() => setLoading(false));
	}, [setFlash]);

	useEffect(() => { load(lines); }, [load, lines]);

	// journalctl returns oldest→newest; reverse so the newest entries are on top.
	const displayLog = log.replace(/\n+$/, "").split("\n").reverse().join("\n");

	return (
		<div className="space-y-6">
			<Card className="border-border/70 bg-card/95 shadow-sm">
				<CardHeader className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
					<div className="space-y-2">
						<CardTitle className="text-3xl font-bold tracking-tight">
							<h1>Application Logs</h1>
						</CardTitle>
						<CardDescription>
							Auto-deploy's own systemd journal (journalctl), for diagnosing the service itself —
							e.g. why fetching GitHub release tags failed. Newest entries first.
						</CardDescription>
					</div>
					<Button variant="outline" asChild>
						<a href="#/">Back to Apps</a>
					</Button>
				</CardHeader>
			</Card>

			<Card className="border-border/70 bg-card/80 shadow-sm">
				<CardContent className="grid gap-4 p-6">
					<div className="flex items-center gap-2 text-sm">
						<label htmlFor="system-log-lines" className="text-muted-foreground">Show</label>
						<select
							id="system-log-lines"
							value={lines}
							onChange={(e) => setLines(Number(e.target.value))}
							disabled={loading}
							className="h-9 rounded-md border border-input bg-background px-2 text-sm shadow-sm focus:outline-none focus:ring-1 focus:ring-ring"
						>
							{LINE_OPTIONS.map((opt) => (
								<option key={opt.value} value={opt.value}>{opt.label}</option>
							))}
						</select>
						<span className="text-muted-foreground">· newest first</span>
						<Button
							type="button"
							variant="outline"
							size="sm"
							onClick={() => load(lines)}
							disabled={loading}
							className="ml-auto gap-2"
						>
							<RefreshCw className="size-4" />
							Refresh
						</Button>
					</div>
					<pre className="max-h-[65vh] min-h-40 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-4 font-mono text-xs leading-relaxed">
						{loading ? "Loading logs…" : (displayLog.trim() || "No logs available.")}
					</pre>
				</CardContent>
			</Card>
		</div>
	);
}
