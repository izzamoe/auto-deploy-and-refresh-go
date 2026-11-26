"use client";

import "@/i18n/id/history";
import dynamic from "next/dynamic";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { renderNothing } from "@/components/common/lazy";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import type { Schemas } from "@/lib/api/client";
import { invalidateAfterJob } from "@/lib/query";
import { HistoryRow } from "./HistoryRow";

const LogsDialog = dynamic(() => import("@/components/apps/LogsDialog").then((m) => m.LogsDialog).catch(renderNothing), { ssr: false });

interface Props {
  jobs: Schemas["Job"][];
  /** Show the app column (the unfiltered history spans all apps). */
  showApp: boolean;
  onMessage: (kind: "success" | "error", text: string) => void;
}

export function HistoryTable({ jobs, showApp, onMessage }: Props) {
  const i18n = useI18n();
  const { t } = i18n;
  const client = useQueryClient();
  const [logsJob, setLogsJob] = useState<Schemas["Job"] | null>(null);
  const [retried, setRetried] = useState<ReadonlySet<string>>(new Set());

  const retry = useMutation({
    mutationFn: (job: Schemas["Job"]) => api.retryJob(job.id),
    onSuccess: (_res, job) => {
      setRetried((prev) => new Set(prev).add(job.id));
      onMessage("success", t("history.retryQueued"));
      invalidateAfterJob(client, job.appId);
    },
    onError: (err) => onMessage("error", describeError(err, i18n)),
  });
  const cancel = useMutation({
    mutationFn: (job: Schemas["Job"]) => api.cancelJob(job.id),
    onSuccess: (_res, job) => {
      onMessage("success", t("history.cancelRequested"));
      invalidateAfterJob(client, job.appId);
    },
    onError: (err) => onMessage("error", describeError(err, i18n)),
  });
  const busy = retry.isPending || cancel.isPending;

  const heads = [...(showApp ? [t("history.col.app")] : []), t("history.col.tag"), t("history.col.status"), t("history.col.trigger"), t("history.col.error"), t("history.col.created"), t("history.col.updated"), t("history.col.actions")];

  return (
    <>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[56rem] border-collapse text-sm" data-testid="history-table">
          <caption className="sr-only">{t("history.table")}</caption>
          <thead>
            <tr className="text-left text-muted-foreground">
              {heads.map((h) => (
                <th key={h} scope="col" className="px-3 py-2 font-medium">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {jobs.map((job) => (
              <HistoryRow key={job.id} job={job} showApp={showApp} retried={retried.has(job.id)} busy={busy} onRetry={(j) => retry.mutate(j)} onCancel={(j) => cancel.mutate(j)} onLogs={setLogsJob} />
            ))}
          </tbody>
        </table>
      </div>
      {logsJob && (
        <LogsDialog
          open
          onOpenChange={(open) => {
            if (!open) setLogsJob(null);
          }}
          title={t("history.logs.title", { tag: logsJob.tag })}
          description={t("history.logs.desc")}
          fetchLog={(_lines, signal) => api.getJobLogs(logsJob.id, { signal })}
        />
      )}
    </>
  );
}
