"use client";

import "@/i18n/id/history";
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, RefreshCw } from "lucide-react";
import { IntentLink } from "@/components/common/IntentLink";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { Notice, useNotice } from "@/components/common/Notice";
import { HistoryTable } from "@/components/history/HistoryTable";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useI18n } from "@/i18n";
import { ApiError, api, describeError } from "@/lib/api/client";
import { useTopic } from "@/lib/events/provider";
import { liveQueryOptions, queryKeys } from "@/lib/query";
import { cn } from "@/lib/utils";

export const HISTORY_PAGE_SIZE = 20;
const JOBS_PROGRESS = { progress: true };

function HistoryRoute() {
  const i18n = useI18n();
  const { t, formatNumber } = i18n;
  const appId = useSearchParams().get("appId") || undefined;
  const { notice, success, error, clear } = useNotice();

  // The page belongs to ONE app filter: changing the filter falls back to page 1 without an
  // effect, and the old request is aborted because its query key (and observer) goes away (B56).
  const [pageFor, setPageFor] = useState<{ app: string | undefined; page: number }>({ app: appId, page: 1 });
  const page = pageFor.app === appId ? pageFor.page : 1;

  // 002 T058: refetched only when topic jobs says a job changed (debounced), never polled.
  const jobsFresh = useTopic("jobs", JOBS_PROGRESS);
  useTopic("apps");
  const history = useQuery({
    ...liveQueryOptions(jobsFresh),
    queryKey: queryKeys.historyPage(appId, page, HISTORY_PAGE_SIZE),
    queryFn: ({ signal }) => api.listHistory({ appId, page, pageSize: HISTORY_PAGE_SIZE }, { signal }),
    // Keep the previous page visible while paging, but never another app's rows after a filter change.
    placeholderData: (previous, previousQuery) => ((previousQuery?.queryKey[1] as { appId?: string } | undefined)?.appId === appId ? previous : undefined),
  });

  const data = history.data;
  const total = data?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / HISTORY_PAGE_SIZE));
  const onMessage = (kind: "success" | "error", text: string) => (kind === "success" ? success(text) : error(text));
  const go = (next: number) => setPageFor({ app: appId, page: Math.min(pages, Math.max(1, next)) });

  return (
    <div className="space-y-4" data-testid="history-page">
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-card bg-card p-5 shadow-card">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("history.title")}</h1>
          <p className="text-sm text-muted-foreground">{t("history.subtitle")}</p>
        </div>
        <Button type="button" variant="outline" onClick={() => void history.refetch()} disabled={history.isFetching}>
          <RefreshCw aria-hidden="true" className={cn(history.isFetching && "motion-safe:animate-spin")} />
          {t("common.refresh")}
        </Button>
      </div>

      {appId && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-2xl bg-accent px-4 py-3 text-sm text-accent-foreground" data-testid="history-filter">
          <span>{t("history.filteredBy", { app: data?.app?.name ?? appId })}</span>
          <IntentLink href="/history/" className="rounded-pill px-3 py-1 font-medium underline underline-offset-4">
            {t("history.showAll")}
          </IntentLink>
        </div>
      )}

      <Notice notice={notice} onDismiss={clear} />

      <section className="rounded-card bg-card p-4 shadow-card" aria-busy={history.isFetching}>
        {history.isPending ? (
          <div className="space-y-2">
            <Skeleton className="h-10" />
            <Skeleton className="h-10" />
            <Skeleton className="h-10" />
          </div>
        ) : history.isError && !data ? (
          <div role="alert" className="space-y-3 p-2">
            <p>{history.error instanceof ApiError && history.error.status === 404 ? t("history.notFound") : t("history.loadError")}</p>
            <p className="text-sm text-muted-foreground">{describeError(history.error, i18n)}</p>
            <Button type="button" onClick={() => void history.refetch()}>
              {t("common.retry")}
            </Button>
          </div>
        ) : data && data.history.length === 0 ? (
          <div className="space-y-1 p-2" data-testid="history-empty">
            <p className="font-medium">{t("history.empty.title")}</p>
            <p className="text-sm text-muted-foreground">{t("history.empty.body")}</p>
          </div>
        ) : data ? (
          <HistoryTable jobs={data.history} showApp={!appId} onMessage={onMessage} />
        ) : null}

        {data && total > 0 && (
          <nav aria-label={t("history.pagination")} data-testid="history-pagination" className="flex flex-col items-center justify-between gap-3 pt-4 sm:flex-row">
            <p className="text-sm text-muted-foreground" aria-live="polite">
              {t("history.page", { page: formatNumber(page), pages: formatNumber(pages), total: formatNumber(total) })}
            </p>
            <div className="flex gap-2">
              <Button type="button" variant="outline" size="sm" disabled={page <= 1} onClick={() => go(page - 1)} data-testid="history-prev">
                <ChevronLeft aria-hidden="true" />
                {t("history.prev")}
              </Button>
              <Button type="button" variant="outline" size="sm" disabled={page >= pages} onClick={() => go(page + 1)} data-testid="history-next">
                {t("history.next")}
                <ChevronRight aria-hidden="true" />
              </Button>
            </div>
          </nav>
        )}
      </section>
    </div>
  );
}

export default function HistoryPage() {
  return (
    <Suspense fallback={null}>
      <HistoryRoute />
    </Suspense>
  );
}
