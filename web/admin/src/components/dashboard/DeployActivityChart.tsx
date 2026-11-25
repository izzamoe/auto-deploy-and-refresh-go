"use client";

import { useId } from "react";
import { AreaChart } from "@/components/charts/AreaChart";
import { useI18n } from "@/i18n";
import type { Schemas } from "@/lib/api/client";
import { cn } from "@/lib/utils";

type Stats = Schemas["Stats"];
export type StatsRange = Stats["range"];
export const RANGES: readonly StatsRange[] = ["7d", "30d"];

const SIGN: Record<number, string> = { 1: "+", [-1]: "−" };

/** Percentage-point delta pill vs the previous period; hidden when there is no previous rate. */
function DeltaPill({ rate, previous }: { rate: number | null | undefined; previous: number | null | undefined }) {
  const { t, formatNumber } = useI18n();
  if (rate === null || rate === undefined || previous === null || previous === undefined) return null;
  const points = Math.round((rate - previous) * 100);
  const n = formatNumber(Math.abs(points));
  const text = points > 0 ? t("dashboard.delta.up", { n }) : points < 0 ? t("dashboard.delta.down", { n }) : t("dashboard.delta.flat");
  const sign = Math.sign(points);
  const cls = points < 0 ? "bg-danger-soft text-danger-soft-foreground" : "bg-success-soft text-success-soft-foreground";
  return (
    <span
      data-testid="delta-pill"
      aria-label={t("dashboard.delta.label", { delta: text })}
      className={cn("inline-flex items-center rounded-pill px-2.5 py-0.5 text-xs font-medium", cls)}
    >
      {SIGN[sign] ?? ""}
      {n}
    </span>
  );
}

interface Props {
  stats: Stats;
  range: StatsRange;
  onRangeChange: (range: StatsRange) => void;
}

/** "Aktivitas deploy": daily area chart (succeeded vs failed) + success-rate and rollback tiles. */
export function DeployActivityChart({ stats, range, onRangeChange }: Props) {
  const { t, formatDay, formatNumber } = useI18n();
  const id = useId();
  const daily = stats.daily;
  const empty = daily.every((d) => d.succeeded + d.failed + d.canceled === 0);
  const rangeLabel = t(`dashboard.range.${range}`);
  const rate = stats.successRate;

  return (
    <section aria-labelledby={id} data-testid="activity" className="rounded-card bg-card p-5 shadow-card">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id={id} className="text-lg font-semibold">
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
                r === range ? "bg-nav text-nav-foreground" : "text-secondary-foreground hover:bg-card",
              )}
            >
              {t(`dashboard.range.${r}`)}
            </button>
          ))}
        </div>
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-[1fr_12rem]">
        <div>
          {empty && <p className="mb-2 text-sm text-muted-foreground">{t("dashboard.chart.empty")}</p>}
          <AreaChart
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
        </div>
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-1">
          <div className="rounded-2xl bg-secondary p-4" data-testid="tile-success-rate">
            <p className="text-sm text-muted-foreground">{t("dashboard.successRate.title")}</p>
            <p className="mt-1 flex flex-wrap items-center gap-2">
              <span className="text-2xl font-semibold">
                {rate.rate === null || rate.rate === undefined ? t("dashboard.successRate.nodata") : formatNumber(rate.rate, { style: "percent", maximumFractionDigits: 0 })}
              </span>
              <DeltaPill rate={rate.rate} previous={rate.previousRate} />
            </p>
          </div>
          <div className="rounded-2xl bg-secondary p-4" data-testid="tile-rollbacks">
            <p className="text-sm text-muted-foreground">{t("dashboard.rollbacks")}</p>
            <p className="mt-1 text-2xl font-semibold">{formatNumber(stats.rollbacks.count)}</p>
            <p className="text-xs text-muted-foreground">{t("dashboard.rollbacks.hint", { range: rangeLabel })}</p>
          </div>
        </div>
      </div>
    </section>
  );
}
