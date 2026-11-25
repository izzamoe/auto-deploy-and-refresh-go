"use client";

import "@/i18n/id/dashboard";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";

const SIGN: Record<number, string> = { 1: "+", [-1]: "−" };

/** Percentage-point delta pill vs the previous period; hidden when there is no previous rate. */
export function DeltaPill({ rate, previous }: { rate: number | null | undefined; previous: number | null | undefined }) {
  const { t, formatNumber } = useI18n();
  if (rate === null || rate === undefined || previous === null || previous === undefined) return null;
  const points = Math.round((rate - previous) * 100);
  const abs = Math.abs(points);
  const n = formatNumber(abs);
  // `n` is a number so the message can pick its singular/plural form ("up 1 point" / "up 2 points").
  const text = points > 0 ? t("dashboard.delta.up", { n: abs }) : points < 0 ? t("dashboard.delta.down", { n: abs }) : t("dashboard.delta.flat");
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
