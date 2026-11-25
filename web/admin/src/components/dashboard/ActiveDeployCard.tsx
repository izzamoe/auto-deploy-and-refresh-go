"use client";

import "@/i18n/id/dashboard";
import { Rocket } from "lucide-react";
import { useId } from "react";
import { useI18n } from "@/i18n";
import { useProgressList } from "@/lib/events/provider";
import { CARD_TITLE } from "@/components/common/cardStyles";

/** Accent (green gradient) card: running + queued deploys and the live progress of one. */
export function ActiveDeployCard({ running, queued }: { running: number; queued: number }) {
  const { t, formatNumber } = useI18n();
  const id = useId();
  const live = useProgressList().filter((p) => p.status === "in_progress");
  const top = live[0];
  const percent = top?.percent !== undefined ? Math.round(top.percent) : undefined;

  return (
    <section
      aria-labelledby={id}
      data-testid="active-deploy"
      className="flex flex-col gap-2 rounded-card bg-gradient-to-br from-gradient-from to-gradient-to p-5 text-gradient-foreground shadow-card"
    >
      <div className="flex items-center justify-between gap-2">
        <h2 id={id} className={CARD_TITLE}>
          {t("dashboard.active")}
        </h2>
        <span aria-hidden="true" className="inline-flex size-9 items-center justify-center rounded-full bg-card text-primary-text">
          <Rocket className="size-4" />
        </span>
      </div>
      <p className="text-3xl font-semibold tracking-tight" data-testid="active-deploy-value">
        {formatNumber(running)}
      </p>
      <p className="text-sm">{running === 0 && queued === 0 ? t("dashboard.active.idle") : t("dashboard.active.queued", { n: queued })}</p>
      {top && (
        <div className="mt-1 space-y-1" data-testid="active-deploy-top">
          <p className="truncate text-sm font-medium">{top.tag ?? top.jobId}</p>
          {percent !== undefined && (
            <>
              <div
                role="progressbar"
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={percent}
                aria-valuetext={t("dashboard.active.progress", { percent })}
                aria-label={t("dashboard.active")}
                className="h-2 overflow-hidden rounded-pill bg-white/25"
              >
                <div className="h-full rounded-pill bg-white" style={{ width: `${percent}%` }} />
              </div>
              <p className="text-xs">{t("dashboard.active.progress", { percent })}</p>
            </>
          )}
        </div>
      )}
    </section>
  );
}
