"use client";

import "@/i18n/id/dashboard";
import Link from "next/link";
import { useId } from "react";
import { StatusPill } from "@/components/common/StatusPill";
import { useI18n } from "@/i18n";
import type { Schemas } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { CARD_TITLE } from "@/components/common/cardStyles";

const BAR: Record<string, string> = {
  succeeded: "bg-primary",
  failed: "bg-destructive",
  in_progress: "bg-nav",
  pending: "bg-nav",
  cancel_requested: "bg-muted-foreground",
  canceled: "bg-muted-foreground",
};

/** Latest deploys: left accent bar by status, app, relative time, tag + status pill. */
export function RecentDeploys({ jobs }: { jobs: Schemas["Job"][] }) {
  const { t, formatRelative, formatDateTime } = useI18n();
  const id = useId();
  return (
    <section aria-labelledby={id} data-testid="recent-deploys" className="rounded-card bg-card p-5 shadow-card">
      <div className="flex items-center justify-between gap-2">
        <h2 id={id} className={CARD_TITLE}>
          {t("dashboard.recent.title")}
        </h2>
        <Link href="/history/" className="rounded-pill px-3 py-1 text-sm font-medium text-primary-text underline-offset-4 hover:underline">
          {t("dashboard.recent.all")}
        </Link>
      </div>
      {jobs.length === 0 ? (
        <p className="mt-3 text-sm text-muted-foreground">{t("dashboard.recent.empty")}</p>
      ) : (
        <ul className="mt-3 space-y-2">
          {jobs.map((job) => {
            const app = job.appName ?? t("common.unknownApp");
            return (
              <li key={job.id}>
                <Link
                  href={`/history/?appId=${encodeURIComponent(job.appId)}`}
                  aria-label={t("dashboard.recent.open", { app })}
                  className="flex items-center gap-3 rounded-2xl bg-secondary px-4 py-3 transition-colors hover:bg-accent"
                >
                  <span aria-hidden="true" className={cn("h-9 w-1.5 shrink-0 rounded-pill", BAR[job.status] ?? "bg-muted-foreground")} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-medium">{app}</span>
                    <time dateTime={job.createdAt} title={formatDateTime(job.createdAt)} className="block text-sm text-muted-foreground">
                      {formatRelative(job.createdAt)}
                    </time>
                  </span>
                  <span className="flex shrink-0 flex-col items-end gap-1">
                    <span className="max-w-28 truncate rounded-pill bg-card px-2.5 py-0.5 text-xs font-medium">{job.tag}</span>
                    <StatusPill status={job.status} />
                  </span>
                </Link>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
