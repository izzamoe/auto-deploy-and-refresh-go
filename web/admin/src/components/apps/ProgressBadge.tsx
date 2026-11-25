"use client";

import "@/i18n/id/apps";
import { StatusPill } from "@/components/common/StatusPill";
import { Progress } from "@/components/ui/progress";
import { useI18n } from "@/i18n";
import type { MessageKey } from "@/i18n";
import type { JobProgress } from "@/lib/events/types";
import type { JobStatus } from "@/lib/events/types";
import { formatBytes, formatSpeed } from "@/lib/format";

interface Props {
  /** Live progress for the job (WS); undefined when nothing is live. */
  progress?: JobProgress;
  /** Status shown (already resolved with `effectiveStatus`). */
  status?: JobStatus;
  /** Last deploy message from the database (shown when there is no live data). */
  message?: string;
  rolledBack?: boolean;
}

/** Status pill + live bar/bytes/speed + the current activity in words. */
export function ProgressBadge({ progress, status, message, rolledBack }: Props) {
  const { t } = useI18n();
  const shown = progress?.status ?? status;
  const phaseKey = progress?.phase ? (`phase.${progress.phase}` as MessageKey) : undefined;
  const live = progress !== undefined && (shown === "in_progress" || shown === "pending" || shown === "cancel_requested");
  const activity = live && phaseKey ? t(phaseKey) : shown ? t(`status.${shown}` as MessageKey) : t("progress.none");
  const done = formatBytes(progress?.downloadedBytes);
  const total = formatBytes(progress?.totalBytes);
  const speed = formatSpeed(progress?.speedBps);
  const percent = progress?.percent !== undefined ? Math.max(0, Math.min(100, progress.percent)) : undefined;
  const detail = progress?.message ?? (progress ? undefined : message);

  return (
    <div data-testid="progress-badge" className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        {shown ? <StatusPill status={shown} /> : <span className="text-sm text-muted-foreground">{t("progress.none")}</span>}
        {rolledBack && <span className="rounded-pill bg-warning-soft px-2.5 py-0.5 text-xs font-medium text-warning-soft-foreground">{t("progress.rolledBack")}</span>}
      </div>
      {live && (
        <div className="space-y-1.5 text-sm text-muted-foreground" data-testid="progress-live">
          {percent !== undefined && <Progress value={percent} aria-label={t("progress.label")} />}
          {done && total && <p>{t("progress.bytes", { done, total })}{percent !== undefined ? ` (${Math.round(percent)}%)` : ""}</p>}
          {done && !total && <p>{t("progress.indeterminate", { done })}</p>}
          {speed && <p>{speed}</p>}
        </div>
      )}
      {shown && <p className="text-sm text-muted-foreground">{t("progress.activity", { text: activity })}</p>}
      {detail && <p className="text-sm text-muted-foreground">{t("progress.detail", { text: detail })}</p>}
    </div>
  );
}
