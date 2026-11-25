"use client";

import "@/i18n/id/apps";
import "@/i18n/id/syslogs";
import { useQuery } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import { queryKeys } from "@/lib/query";
import { cn } from "@/lib/utils";

const LINE_OPTIONS = [50, 100, 500, 0] as const; // 0 = all

/** auto-deploy's own journal: lines selector, newest first, `truncated` notice (FR-029, FR-058). */
export function SystemLogsView() {
  const i18n = useI18n();
  const { t } = i18n;
  const [lines, setLines] = useState<number>(100);
  const [auto, setAuto] = useState(false);

  const logs = useQuery({
    queryKey: queryKeys.systemLogs(lines),
    queryFn: ({ signal }) => api.getSystemLogs(lines, { signal }),
    refetchInterval: auto ? 10_000 : false,
    refetchOnWindowFocus: false,
  });

  // journalctl returns oldest -> newest; show the newest on top.
  const text = logs.data ? logs.data.log.replace(/\n+$/, "").split("\n").reverse().join("\n") : "";

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
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} className="size-4 accent-primary" />
            {t("syslogs.autoRefresh")}
          </label>
          <Button type="button" variant="outline" onClick={() => void logs.refetch()} disabled={logs.isFetching}>
            <RefreshCw aria-hidden="true" className={cn(logs.isFetching && "motion-safe:animate-spin")} />
            {t("common.refresh")}
          </Button>
        </div>
      </div>

      <section className="space-y-3 rounded-card bg-card p-4 shadow-card" aria-busy={logs.isFetching}>
        {logs.isError && (
          <div role="alert" className="space-y-2 rounded-xl bg-danger-soft px-4 py-3 text-sm text-danger-soft-foreground">
            <p>{t("syslogs.loadError")}</p>
            <p>{describeError(logs.error, i18n)}</p>
          </div>
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
