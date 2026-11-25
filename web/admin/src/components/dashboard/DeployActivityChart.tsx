"use client";

import "@/i18n/id/dashboard";
import { useId } from "react";
import { AreaChart } from "@/components/charts/AreaChart";
import { CARD_TITLE } from "@/components/common/cardStyles";
import { useI18n } from "@/i18n";
import type { Schemas } from "@/lib/api/client";
import { cn } from "@/lib/utils";

type Stats = Schemas["Stats"];
export type StatsRange = Stats["range"];
export const RANGES: readonly StatsRange[] = ["7d", "30d"];

interface Props {
  stats: Stats;
  range: StatsRange;
  onRangeChange: (range: StatsRange) => void;
}

/**
 * "Aktivitas deploy": daily area chart (succeeded vs failed) and the rollback count. With no
 * deploy in the range NO line is drawn (a flat red line at 0 would read as "zero failures"):
 * only the empty state is shown. The success rate lives in the gauge card, not here.
 */
export function DeployActivityChart({ stats, range, onRangeChange }: Props) {
  const { t, formatDay, formatNumber } = useI18n();
  const id = useId();
  const daily = stats.daily;
  const empty = daily.every((d) => d.succeeded + d.failed + d.canceled === 0);
  const rangeLabel = t(`dashboard.range.${range}`);

  return (
    <section aria-labelledby={id} data-testid="activity" className="flex flex-col rounded-card bg-card p-5 shadow-card">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id={id} className={CARD_TITLE}>
          {t("dashboard.activity.title")}
        </h2>
        <div role="group" aria-label={t("dashboard.activity.range")} className="inline-flex rounded-pill bg-secondary p-1">
          {RANGES.map((r) => (
            <button
              key={r}
              type="button"
              aria-pressed={r === range}
              onClick={() => onRangeChange(r)}
              className={cn(
                "rounded-pill px-3.5 py-1.5 text-sm font-medium transition-colors",
                r === range ? "bg-primary text-primary-foreground" : "text-secondary-foreground hover:bg-card",
              )}
            >
              {t(`dashboard.range.${r}`)}
            </button>
          ))}
        </div>
      </div>

      {empty ? (
        <p data-testid="chart-empty" className="mt-4 flex min-h-16 max-h-24 items-center justify-center overflow-hidden rounded-2xl bg-secondary px-4 py-4 text-center text-sm text-muted-foreground">
          {t("dashboard.chart.empty")}
        </p>
      ) : (
        <AreaChart
          className="mt-4"
          labels={daily.map((d) => formatDay(d.date))}
          series={[
            { key: "succeeded", label: t("dashboard.series.succeeded"), values: daily.map((d) => d.succeeded), tone: "success" },
            { key: "failed", label: t("dashboard.series.failed"), values: daily.map((d) => d.failed), tone: "danger" },
          ]}
          extraColumns={[{ key: "canceled", label: t("dashboard.series.canceled"), values: daily.map((d) => d.canceled) }]}
          ariaLabel={t("dashboard.chart.aria")}
          tableCaption={t("dashboard.chart.table", { range: rangeLabel })}
          dateHeader={t("dashboard.chart.date")}
        />
      )}

      <p className="mt-4 flex flex-wrap items-center gap-x-2 text-sm text-muted-foreground" data-testid="tile-rollbacks">
        <span>{t("dashboard.rollbacks")}:</span>
        <span className="font-semibold text-foreground">{formatNumber(stats.rollbacks.count)}</span>
        <span>{t("dashboard.rollbacks.hint", { range: rangeLabel })}</span>
      </p>
    </section>
  );
}
