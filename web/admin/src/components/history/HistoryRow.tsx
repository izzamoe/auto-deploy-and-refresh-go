"use client";

import "@/i18n/id/history";
import { StatusPill } from "@/components/common/StatusPill";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import type { MessageKey } from "@/i18n";
import type { Schemas } from "@/lib/api/client";
import { effectiveStatus } from "@/lib/events/store";
import { useJobProgress } from "@/lib/events/provider";

const TERMINAL = new Set(["succeeded", "failed", "canceled"]);
const KNOWN_TRIGGERS = new Set(["webhook", "manual", "manual_retry"]);

interface Props {
  job: Schemas["Job"];
  showApp: boolean;
  /** This job was already retried from here: hide the retry button. */
  retried: boolean;
  busy: boolean;
  onRetry: (job: Schemas["Job"]) => void;
  onCancel: (job: Schemas["Job"]) => void;
  onLogs: (job: Schemas["Job"]) => void;
}

export function HistoryRow({ job, showApp, retried, busy, onRetry, onCancel, onLogs }: Props) {
  const { t, formatDateTime } = useI18n();
  const live = useJobProgress(job.id);
  const status = effectiveStatus(job.status, job.updatedAt, live) ?? job.status;
  const terminal = TERMINAL.has(status);
  const canRetry = (status === "succeeded" || status === "failed") && !retried;
  const trigger = KNOWN_TRIGGERS.has(job.trigger) ? t(`history.trigger.${job.trigger}` as MessageKey) : job.trigger;

  return (
    <tr data-testid={`history-row-${job.id}`} data-status={status} data-job-id={job.id} className="border-t border-border align-top">
      {showApp && <td className="px-3 py-3 font-medium">{job.appName ?? t("common.unknownApp")}</td>}
      <th scope="row" className="px-3 py-3 text-left font-medium">
        {job.tag}
      </th>
      <td className="px-3 py-3">
        <div className="flex flex-wrap items-center gap-1.5">
          <StatusPill status={status} />
          {job.rolledBack && <span className="rounded-pill bg-warning-soft px-2 py-0.5 text-xs font-medium text-warning-soft-foreground">{t("history.rolledBack")}</span>}
        </div>
      </td>
      <td className="px-3 py-3">{trigger}</td>
      <td className="max-w-xs px-3 py-3 text-muted-foreground">{job.errorMsg}</td>
      <td className="whitespace-nowrap px-3 py-3">{formatDateTime(job.createdAt)}</td>
      <td className="whitespace-nowrap px-3 py-3">{formatDateTime(job.updatedAt)}</td>
      <td className="px-3 py-3">
        <div className="flex flex-wrap gap-2">
          {canRetry && (
            <Button type="button" size="sm" variant="outline" disabled={busy} aria-label={t("history.retryFor", { tag: job.tag })} data-testid={`retry-${job.id}`} onClick={() => onRetry(job)}>
              {t("history.retry")}
            </Button>
          )}
          <Button type="button" size="sm" variant="secondary" aria-label={t("history.logsFor", { tag: job.tag })} data-testid={`logs-${job.id}`} onClick={() => onLogs(job)}>
            {t("history.logs")}
          </Button>
          {!terminal && (
            <Button type="button" size="sm" variant="destructive" disabled={busy} aria-label={t("history.cancelFor", { tag: job.tag })} onClick={() => onCancel(job)}>
              {t("history.cancel")}
            </Button>
          )}
        </div>
      </td>
    </tr>
  );
}
