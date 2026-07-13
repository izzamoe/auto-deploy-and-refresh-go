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

interface LogsDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	title: string;
	description?: string;
	// fetchLog resolves to the log text to display. It is read from a ref so a
	// changing inline function does not retrigger loads on every render.
	fetchLog: () => Promise<string>;
}

export function LogsDialog({ open, onOpenChange, title, description, fetchLog }: LogsDialogProps) {
	const [log, setLog] = useState("");
	const [loading, setLoading] = useState(false);
	const fetchRef = useRef(fetchLog);
	fetchRef.current = fetchLog;

	const [reloadTick, setReloadTick] = useState(0);

	useEffect(() => {
		if (!open) return;
		let cancelled = false;
		setLoading(true);
		fetchRef.current()
			.then((text) => { if (!cancelled) setLog(text); })
			.catch((err) => { if (!cancelled) setLog(String(err)); })
			.finally(() => { if (!cancelled) setLoading(false); });
		return () => { cancelled = true; };
	}, [open, reloadTick]);

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-w-3xl">
				<DialogHeader>
					<DialogTitle>{title}</DialogTitle>
					{description && <DialogDescription>{description}</DialogDescription>}
				</DialogHeader>
				<pre className="max-h-[60vh] min-h-40 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-4 font-mono text-xs leading-relaxed">
					{loading ? "Loading logs…" : (log.trim() || "No logs available.")}
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
