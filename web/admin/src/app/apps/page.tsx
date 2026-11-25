"use client";

import "@/i18n/id/apps";
import { useQuery } from "@tanstack/react-query";
import { Plus, RefreshCw } from "lucide-react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { AppCard } from "@/components/apps/AppCard";
import { Notice, useNotice } from "@/components/common/Notice";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import { queryKeys } from "@/lib/query";
import { cn } from "@/lib/utils";

function AppsRoute() {
  const i18n = useI18n();
  const { t } = i18n;
  const q = (useSearchParams().get("q") ?? "").trim();
  const { notice, success, error, clear } = useNotice();

  const apps = useQuery({ queryKey: queryKeys.apps, queryFn: ({ signal }) => api.listApps({ signal }) });
  const all = apps.data?.apps ?? [];
  const needle = q.toLowerCase();
  const shown = needle ? all.filter((a) => [a.name, a.serviceName, a.githubRepo].some((v) => v.toLowerCase().includes(needle))) : all;
  const onMessage = (kind: "success" | "error", text: string) => (kind === "success" ? success(text) : error(text));

  return (
    <div className="space-y-4" data-testid="apps-page">
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-card bg-card p-5 shadow-card">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("apps.title")}</h1>
          <p className="text-sm text-muted-foreground">{t("apps.subtitle")}</p>
        </div>
        <div className="flex gap-2">
          <Button type="button" variant="outline" onClick={() => void apps.refetch()} disabled={apps.isFetching}>
            <RefreshCw aria-hidden="true" className={cn(apps.isFetching && "motion-safe:animate-spin")} />
            {t("common.refresh")}
          </Button>
          <Button asChild>
            <Link href="/apps/new/">
              <Plus aria-hidden="true" />
              {t("apps.add")}
            </Link>
          </Button>
        </div>
      </div>

      <Notice notice={notice} onDismiss={clear} />

      {apps.isPending ? (
        <div className="grid gap-4 lg:grid-cols-2" aria-busy="true">
          <Skeleton className="h-72 rounded-card" />
          <Skeleton className="h-72 rounded-card" />
        </div>
      ) : apps.isError ? (
        <div role="alert" className="space-y-3 rounded-card bg-card p-6 shadow-card">
          <p>{t("apps.loadError")}</p>
          <p className="text-sm text-muted-foreground">{describeError(apps.error, i18n)}</p>
          <Button type="button" onClick={() => void apps.refetch()}>
            {t("common.retry")}
          </Button>
        </div>
      ) : shown.length === 0 ? (
        <p className="rounded-card bg-card p-6 text-muted-foreground shadow-card" data-testid="apps-empty">
          {q ? t("apps.noMatch", { q }) : t("apps.empty")}
        </p>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2 2xl:grid-cols-3">
          {shown.map((app) => (
            <AppCard key={app.id} app={app} onMessage={onMessage} />
          ))}
        </div>
      )}
    </div>
  );
}

export default function AppsPage() {
  return (
    <Suspense fallback={null}>
      <AppsRoute />
    </Suspense>
  );
}
