"use client";

import "@/i18n/id/dashboard";
import { useId } from "react";
import { Gauge } from "@/components/charts/Gauge";
import { CARD_TITLE } from "@/components/common/cardStyles";
import { useI18n } from "@/i18n";
import type { Schemas } from "@/lib/api/client";
import { DeltaPill } from "./DeltaPill";

/** Success rate as a half-donut; `rate = null` (no deploys) says so in words. The ONLY place that shows it. */
export function SuccessGauge({ rate, range }: { rate: Schemas["Stats"]["successRate"]; range: string }) {
  const { t, formatNumber } = useI18n();
  const id = useId();
  const has = rate.rate !== null && rate.rate !== undefined;
  const percent = has ? formatNumber(rate.rate as number, { style: "percent", maximumFractionDigits: 0 }) : null;

  return (
    <section aria-labelledby={id} data-testid="success-gauge" className="rounded-card bg-card p-5 shadow-card">
      <h2 id={id} className={CARD_TITLE}>
        {t("dashboard.successRate.title")} · {range}
      </h2>
      <div className="relative mx-auto mt-2 w-full max-w-56">
        <Gauge value={has ? (rate.rate as number) : null} label={percent ? t("dashboard.gauge.aria", { percent }) : t("dashboard.successRate.nodata")} />
        <p className="absolute inset-x-0 bottom-1 text-center text-2xl font-semibold text-primary-text" data-testid="gauge-value">
          {percent ?? <span className="text-base font-medium text-muted-foreground">{t("dashboard.successRate.nodata")}</span>}
        </p>
      </div>
      {has && (
        <p className="mt-2 flex flex-wrap items-center justify-center gap-2 text-sm text-muted-foreground">
          {t("dashboard.successRate.of", { succeeded: rate.succeeded, total: rate.total })}
          <DeltaPill rate={rate.rate} previous={rate.previousRate} />
        </p>
      )}
    </section>
  );
}
