"use client";

import "@/i18n/id/dashboard";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { AppWindow, BarChart3, XCircle } from "lucide-react";
import dynamic from "next/dynamic";
import { useState } from "react";
import { MiniBars } from "@/components/charts/MiniBars";
import { AccountCard } from "@/components/dashboard/AccountCard";
import { ActiveDeployCard } from "@/components/dashboard/ActiveDeployCard";
import { AddAppCard } from "@/components/dashboard/AddAppCard";
import type { StatsRange } from "@/components/dashboard/DeployActivityChart";
import { RecentDeploys } from "@/components/dashboard/RecentDeploys";
import { SecurityCard } from "@/components/dashboard/SecurityCard";
import { StatCard } from "@/components/dashboard/StatCard";
import { SuccessGauge } from "@/components/dashboard/SuccessGauge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import { queryKeys } from "@/lib/query";
import { cn } from "@/lib/utils";

// The chart card is loaded on demand so it never weighs on the shared bundle (Q-20).
const DeployActivityChart = dynamic(() => import("@/components/dashboard/DeployActivityChart").then((m) => m.DeployActivityChart), {
  ssr: false,
  loading: () => <Skeleton className="h-80 rounded-card" />,
});

/** Dashboard (FR-100, FR-026): auto-deploy content in the reference's card style. */
export default function DashboardPage() {
  const i18n = useI18n();
  const { t, formatNumber } = i18n;
  const [range, setRange] = useState<StatsRange>("7d");

  const stats = useQuery({
    queryKey: queryKeys.statsRange(range),
    queryFn: ({ signal }) => api.getStats({ range }, { signal }),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  });

  if (stats.isPending) {
    return (
      <div data-testid="dashboard-loading" aria-busy="true" className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        <h1 className="sr-only">{t("dashboard.title")}</h1>
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-36 rounded-card" />
        ))}
      </div>
    );
  }

  if (stats.isError && !stats.data) {
    return (
      <div role="alert" data-testid="dashboard-error" className="space-y-3 rounded-card bg-card p-6 shadow-card">
        <h1 className="text-xl font-semibold">{t("dashboard.title")}</h1>
        <p>{t("dashboard.error")}</p>
        <p className="text-sm text-muted-foreground">{describeError(stats.error, i18n)}</p>
        <Button type="button" onClick={() => void stats.refetch()}>
          {t("common.retry")}
        </Button>
      </div>
    );
  }

  const s = stats.data!;
  const hourly = s.deploys24h.hourly ?? [];

  return (
    <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_20rem]" data-testid="dashboard">
      <h1 className="sr-only">{t("dashboard.title")}</h1>
      <div className={cn("flex min-w-0 flex-col gap-4", s.recent.length > 0 && "xl:[&>:last-child]:flex-1")}>
        <div className="grid gap-4 sm:grid-cols-2 2xl:grid-cols-4">
          <StatCard testId="stat-deploys" title={t("dashboard.deploys24h")} value={formatNumber(s.deploys24h.total)} hint={t("dashboard.deploys24h.hint", { n: s.deploys24h.succeeded })} icon={<BarChart3 />} tone={s.deploys24h.succeeded > 0 ? "success" : "neutral"}>
            {hourly.length > 0 && <MiniBars values={hourly} className="mt-1 h-8 w-full" />}
          </StatCard>
          <StatCard testId="stat-apps" title={t("dashboard.apps")} value={`${formatNumber(s.apps.enabled)}/${formatNumber(s.apps.total)}`} hint={t("dashboard.apps.hint", { total: s.apps.total })} icon={<AppWindow />} tone={s.apps.enabled > 0 ? "success" : "neutral"}>
            {s.apps.total > 0 && (
              <div aria-hidden="true" data-testid="apps-bar" className="mt-1 h-2 overflow-hidden rounded-pill bg-track/30">
                <div className="h-full rounded-pill bg-primary" style={{ width: `${Math.round((s.apps.enabled / s.apps.total) * 100)}%` }} />
              </div>
            )}
          </StatCard>
          <StatCard testId="stat-failed" title={t("dashboard.failed24h")} value={formatNumber(s.deploys24h.failed)} hint={t("dashboard.failed24h.hint", { total: s.deploys24h.total })} icon={<XCircle />} iconTone="danger" tone={s.deploys24h.failed > 0 ? "danger" : "neutral"} />
          <ActiveDeployCard running={s.active.running} queued={s.active.queued} />
        </div>
        <DeployActivityChart stats={s} range={range} onRangeChange={setRange} />
        <RecentDeploys jobs={s.recent} />
      </div>
      <div className="grid content-start gap-4 sm:grid-cols-2 xl:flex xl:flex-col xl:[&>:last-child]:flex-1">
        <SuccessGauge rate={s.successRate} range={t(`dashboard.range.${range}`)} />
        <AccountCard />
        <AddAppCard />
        <SecurityCard />
      </div>
    </div>
  );
}
