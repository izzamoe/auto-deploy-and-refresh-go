"use client";

// Log viewer dialog (service journal, job logs, system logs). Loaded on demand (next/dynamic)
// so the dialog code is not in the initial bundle (Q-20). Requests are aborted on close,
// on a new load and on unmount; the server reports `truncated` (FR-058) and we say so.
import "@/i18n/id/apps";
import { RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useI18n } from "@/i18n";
import { describeError, isAbortError } from "@/lib/api/client";

const LINE_OPTIONS = [50, 100, 500, 0] as const; // 0 = all

export interface LogResult {
  log: string;
  truncated?: boolean;
}

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  /** Resolves to the log text; `lines <= 0` means all. Receives the abort signal. */
  fetchLog(lines: number, signal: AbortSignal): Promise<LogResult>;
  /** Live logs: line-count selector and newest-first order. */
  live?: boolean;
}

export function LogsDialog({ open, onOpenChange, title, description, fetchLog, live = false }: Props) {
  const i18n = useI18n();
  const { t } = i18n;
  const [text, setText] = useState("");
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [lines, setLines] = useState(100);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (!open) return;
    const ctl = new AbortController();
    setLoading(true);
    setError("");
    fetchLog(lines, ctl.signal)
      .then((res) => {
        setText(res.log);
        setTruncated(res.truncated === true);
      })
      .catch((err) => {
        if (!isAbortError(err)) setError(describeError(err, i18n));
      })
      .finally(() => {
        if (!ctl.signal.aborted) setLoading(false);
      });
    return () => ctl.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- fetchLog is an inline closure; reload only on open / lines / refresh
  }, [open, live ? lines : 0, tick]);

  // journalctl returns oldest -> newest; live view puts the newest on top.
  const shown = live ? text.replace(/\n+$/, "").split("\n").reverse().join("\n") : text;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl rounded-card">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description ? <DialogDescription>{description}</DialogDescription> : null}
        </DialogHeader>
        {live && (
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <label htmlFor="log-lines" className="text-muted-foreground">
              {t("logs.show")}
            </label>
            <select
              id="log-lines"
              value={lines}
              onChange={(e) => setLines(Number(e.target.value))}
              disabled={loading}
              className="h-9 rounded-md border border-input bg-background px-2 text-sm"
            >
              {LINE_OPTIONS.map((n) => (
                <option key={n} value={n}>
                  {n === 0 ? t("logs.all") : t("logs.lines", { n })}
                </option>
              ))}
            </select>
            <span className="text-muted-foreground">· {t("logs.newestFirst")}</span>
          </div>
        )}
        {error && (
          <p role="alert" className="rounded-xl bg-danger-soft px-3 py-2 text-sm text-danger-soft-foreground">
            {error}
          </p>
        )}
        {truncated && !loading && (
          <p role="status" className="rounded-xl bg-warning-soft px-3 py-2 text-sm text-warning-soft-foreground" data-testid="logs-truncated">
            {t("logs.truncated")}
          </p>
        )}
        <pre
          tabIndex={0}
          aria-busy={loading}
          className="max-h-[60vh] min-h-40 overflow-auto whitespace-pre-wrap rounded-xl bg-muted p-4 font-mono text-xs leading-relaxed"
        >
          {loading ? t("logs.loading") : shown.trim() || t("logs.empty")}
        </pre>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => setTick((n) => n + 1)} disabled={loading} className="gap-2">
            <RefreshCw aria-hidden="true" />
            {t("common.refresh")}
          </Button>
          <Button type="button" onClick={() => onOpenChange(false)}>
            {t("common.close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
