import { useState } from "react";
import { AdminAPIError, apiRequest } from "./api";

import { Button } from "@/components/ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog";

export interface ServiceUnitButtonProps {
	appId: string;
	setFlash: (flash: { message: string, type: "success" | "error" }) => void;
	variant?: "outline" | "secondary";
	/** Called after a successful apply, e.g. to refresh a service status badge. */
	onApplied?: () => void;
}

// ServiceUnitButton renders the "Generate service unit" button together with its
// preview-then-confirm dialog. It lives in its own component because the apps
// list and the app edit form both offer the action: an operator who just changed
// the env vars or the command-line arguments should be able to regenerate the
// unit from where they are, without the two places drifting apart.
//
// Applying is deliberately a two-step flow (preview, then an explicit confirm):
// it writes to /etc/systemd/system as root.
export function ServiceUnitButton({ appId, setFlash, variant = "outline", onApplied }: ServiceUnitButtonProps) {
	const [unit, setUnit] = useState<string | null>(null);
	const [loading, setLoading] = useState(false);
	const [applying, setApplying] = useState(false);

	const preview = async () => {
		setLoading(true);
		try {
			const res = await apiRequest(`/apps/${appId}/service-unit/preview`);
			setUnit(res.unit);
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) setFlash({ message: err.message, type: "error" });
		} finally {
			setLoading(false);
		}
	};

	const apply = async () => {
		setApplying(true);
		try {
			const res = await apiRequest(`/apps/${appId}/service-unit/apply`, { method: "POST" });
			setFlash({ message: res.message || "Service unit created and enabled", type: "success" });
			setUnit(null);
			onApplied?.();
		} catch (err: unknown) {
			if (err instanceof AdminAPIError) setFlash({ message: err.message, type: "error" });
		} finally {
			setApplying(false);
		}
	};

	return (
		<>
			<Button type="button" variant={variant} size="sm" onClick={preview} disabled={loading}>
				{loading ? "Loading..." : "Generate service unit"}
			</Button>

			<Dialog open={unit !== null} onOpenChange={(open) => { if (!open) setUnit(null); }}>
				<DialogContent className="max-w-2xl">
					<DialogHeader>
						<DialogTitle>Generate systemd service unit</DialogTitle>
						<DialogDescription>
							Applying this writes a unit file to /etc/systemd/system as root and runs
							"systemctl daemon-reload" and "systemctl enable". Review the generated content below
							before confirming.
						</DialogDescription>
					</DialogHeader>
					<pre className="max-h-96 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-4 text-xs">
						{unit}
					</pre>
					<DialogFooter>
						<Button type="button" variant="outline" onClick={() => setUnit(null)}>
							Cancel
						</Button>
						<Button
							type="button"
							variant="destructive"
							onClick={apply}
							disabled={applying}
							aria-label="Confirm and apply service unit"
						>
							{applying ? "Applying..." : "Confirm and Apply"}
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</>
	);
}
