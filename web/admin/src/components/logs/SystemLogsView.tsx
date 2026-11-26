"use client";

import "@/i18n/id/apps";
import "@/i18n/id/syslogs";
import { useQuery } from "@tanstack/react-query";
import { Pause, Play, RefreshCw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import { useTopic, useTopicFrames } from "@/lib/events/provider";
import type { LogLine, TopicFrame } from "@/lib/events/types";
import { queryKeys } from "@/lib/query";
import { cn } from "@/lib/utils";

const LINE_OPTIONS = [50, 100, 500, 0] as const; // 0 = all
/** At most this many live lines are kept on screen (and held while paused). */
export const MAX_LIVE_LINES = 5_000;

const formatLine = (l: LogLine) => (l.ts ? `${l.ts} ${l.message}` : l.message);

/**
 * auto-deploy's own journal: lines selector, newest first, `truncated` notice (FR-029, FR-058).
 * 002 (FR-009, SC-012, Q-5=A): live mode is ON when the page opens. The REST snapshot gives the
 * last lines and a journal cursor; topic `logs:system` then streams the lines after it (no
 * polling). "Pause" holds new lines and says how many arrived; the log region itself is NOT a
 * live region (a screen reader is not flooded), only the short status line is polite.
 */
export function SystemLogsView() {
  const i18n = useI18n();
  const { t } = i18n;
  const [lines, setLines] = useState<number>(100);
  const [paused, setPaused] = useState(false);
  const [live, setLive] = useState<string[]>([]); // newest first
  const [held, setHeld] = useState<string[]>([]); // newest first, while paused
  const [gap, setGap] = useState(0);
  const [unavailable, setUnavailable] = useState(false);
  const pausedRef = useRef(paused);
  pausedRef.current = paused;

  const logs = useQuery({
    queryKey: queryKeys.systemLogs(lines),
    queryFn: ({ signal }) => api.getSystemLogs(lines, { signal }),
    staleTime: Infinity, // the stream keeps it current; only the button or a gap reloads it
    refetchOnWindowFocus: false,
  });

  // A new snapshot contains everything up to its cursor: the stream continues after it.
  const cursor = logs.data?.cursor;
  const snapshotAt = logs.dataUpdatedAt;
  useEffect(() => {
    setLive([]);
    setHeld([]);
    setGap(0);
  }, [snapshotAt]);

  useTopic("logs:system", cursor ? { after: cursor } : {}, logs.isSuccess);

  useTopicFrames((frame: TopicFrame) => {
    if (frame.kind === "logs.lines") {
      if (frame.lines.length === 0) return;
      setUnavailable(false);
      const fresh = frame.lines.map(formatLine).reverse();
      const add = (prev: string[]) => [...fresh, ...prev].slice(0, MAX_LIVE_LINES);
      if (pausedRef.current) setHeld(add);
      else setLive(add);
    } else if (frame.kind === "logs.gap") {
      if (frame.reason === "rate") setGap((n) => n + frame.dropped);
      else void logs.refetch(); // cursor_behind / resync: reload the snapshot, then continue after its cursor
    } else if (frame.kind === "notice" && frame.code === "logs_unavailable") {
      setUnavailable(true);
    }
  });

  function togglePause() {
    if (paused) {
      setLive((prev) => [...held, ...prev].slice(0, MAX_LIVE_LINES));
      setHeld([]);
    }
    setPaused(!paused);
  }

  // journalctl returns oldest -> newest; show the newest on top.
  const snapshot = logs.data ? logs.data.log.replace(/\n+$/, "").split("\n").reverse().join("\n") : "";
  const text = [live.join("\n"), snapshot].filter((s) => s.trim() !== "").join("\n");
  const status = paused
    ? `${t("syslogs.paused")} ${t(held.length === 1 ? "syslogs.newLines.one" : "syslogs.newLines", { n: held.length })}`
    : t("syslogs.live.on");

  return (
    <div className="space-y-4" data-testid="system-logs-page">
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-card bg-card p-5 shadow-card">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("syslogs.title")}</h1>
          <p className="text-sm text-muted-foreground">{t("syslogs.subtitle")}</p>
          {logs.data?.service && <p className="mt-1 text-sm text-muted-foreground">{t("syslogs.service", { name: logs.data.service })}</p>}
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <label htmlFor="syslog-lines" className="text-sm text-muted-foreground">
            {t("logs.show")}
          </label>
          <select
            id="syslog-lines"
            value={lines}
            onChange={(e) => setLines(Number(e.target.value))}
            className="h-9 rounded-md border border-input bg-background px-2 text-sm"
          >
            {LINE_OPTIONS.map((n) => (
              <option key={n} value={n}>
                {n === 0 ? t("logs.all") : t("logs.lines", { n })}
              </option>
            ))}
          </select>
          <Button type="button" variant="secondary" onClick={togglePause} aria-pressed={paused} data-testid="logs-pause">
            {paused ? <Play aria-hidden="true" /> : <Pause aria-hidden="true" />}
            {paused ? t("syslogs.resume") : t("syslogs.pause")}
          </Button>
          <Button type="button" variant="outline" onClick={() => void logs.refetch()} disabled={logs.isFetching}>
            <RefreshCw aria-hidden="true" className={cn(logs.isFetching && "motion-safe:animate-spin")} />
            {t("common.refresh")}
          </Button>
        </div>
      </div>

      <section className="space-y-3 rounded-card bg-card p-4 shadow-card" aria-busy={logs.isFetching}>
        <p role="status" aria-live="polite" data-testid="logs-live-status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <span aria-hidden="true" className={cn("inline-flex size-2 rounded-full", paused ? "bg-muted-foreground/60" : "bg-primary")} />
          <span className="font-medium">{t("syslogs.live")}</span>
          <span>{status}</span>
        </p>
        {logs.isError && (
          <div role="alert" className="space-y-2 rounded-xl bg-danger-soft px-4 py-3 text-sm text-danger-soft-foreground">
            <p>{t("syslogs.loadError")}</p>
            <p>{describeError(logs.error, i18n)}</p>
          </div>
        )}
        {unavailable && (
          <p data-testid="logs-unavailable" className="rounded-xl bg-warning-soft px-3 py-2 text-sm text-warning-soft-foreground">
            {t("syslogs.unavailable")}
          </p>
        )}
        {gap > 0 && (
          <p data-testid="logs-gap" className="rounded-xl bg-warning-soft px-3 py-2 text-sm text-warning-soft-foreground">
            {t("syslogs.gap", { n: gap })}
          </p>
        )}
        {logs.data?.truncated && (
          <p role="status" data-testid="logs-truncated" className="rounded-xl bg-warning-soft px-3 py-2 text-sm text-warning-soft-foreground">
            {t("logs.truncated")}
          </p>
        )}
        <pre
          tabIndex={0}
          aria-label={t("syslogs.region")}
          className="max-h-[70vh] min-h-48 overflow-auto whitespace-pre-wrap rounded-xl bg-muted p-4 font-mono text-xs leading-relaxed"
          data-testid="system-logs-text"
        >
          {logs.isPending ? t("logs.loading") : text.trim() || t("logs.empty")}
        </pre>
      </section>
    </div>
  );
}
