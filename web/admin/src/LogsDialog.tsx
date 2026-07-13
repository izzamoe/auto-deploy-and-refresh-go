import { useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog";

// LINE_OPTIONS are the selectable window sizes for live logs. 0 means "all"
// (the full available journal).
const LINE_OPTIONS = [
	{ value: 50, label: "50 lines" },
	{ value: 100, label: "100 lines" },
	{ value: 500, label: "500 lines" },
	{ value: 0, label: "All" },
];

interface LogsDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	title: string;
	description?: string;
	// fetchLog resolves to the log text to display. It receives the requested
	// line count (<= 0 means all). It is read from a ref so a changing inline
	// function does not retrigger loads on every render.
	fetchLog: (lines: number) => Promise<string>;
	// live enables the line-count selector and orders newest-first (top to
	// bottom). Leave false for a captured, chronological snapshot.
	live?: boolean;
}

export function LogsDialog({ open, onOpenChange, title, description, fetchLog, live = false }: LogsDialogProps) {
	const [log, setLog] = useState("");
	const [loading, setLoading] = useState(false);
	const [lines, setLines] = useState(100);
	const fetchRef = useRef(fetchLog);
	fetchRef.current = fetchLog;

	const [reloadTick, setReloadTick] = useState(0);

	useEffect(() => {
		if (!open) return;
		let cancelled = false;
		setLoading(true);
		fetchRef.current(lines)
			.then((text) => { if (!cancelled) setLog(text); })
			.catch((err) => { if (!cancelled) setLog(String(err)); })
			.finally(() => { if (!cancelled) setLoading(false); });
		return () => { cancelled = true; };
	}, [open, reloadTick, live ? lines : 0]);

	// For live logs, journalctl returns oldest→newest; reverse so the most
	// recent entries are at the top. Captured snapshots stay chronological.
	const displayLog = live
		? log.replace(/\n+$/, "").split("\n").reverse().join("\n")
		: log;

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-w-3xl">
				<DialogHeader>
					<DialogTitle>{title}</DialogTitle>
					{description && <DialogDescription>{description}</DialogDescription>}
				</DialogHeader>
				{live && (
					<div className="flex items-center gap-2 text-sm">
						<label htmlFor="log-lines" className="text-muted-foreground">Show</label>
						<select
							id="log-lines"
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
					</div>
				)}
				<pre className="max-h-[60vh] min-h-40 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-4 font-mono text-xs leading-relaxed">
					{loading ? "Loading logs…" : (displayLog.trim() || "No logs available.")}
				</pre>
				<DialogFooter>
					<Button type="button" variant="outline" onClick={() => setReloadTick((n) => n + 1)} disabled={loading} className="gap-2">
						<RefreshCw className="size-4" />
						Refresh
					</Button>
					<Button type="button" onClick={() => onOpenChange(false)}>Close</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
