"use client";

import "@/i18n/id/apps";
import "@/i18n/id/unit";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Play, RefreshCw, RotateCw, Square } from "lucide-react";
import dynamic from "next/dynamic";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import type { MessageKey } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import { queryKeys } from "@/lib/query";
import { cn } from "@/lib/utils";

// Heavy dialogs are fetched only when first opened (Q-20).
const LogsDialog = dynamic(() => import("./LogsDialog").then((m) => m.LogsDialog), { ssr: false });
const ServiceUnitDialog = dynamic(() => import("./ServiceUnitDialog").then((m) => m.ServiceUnitDialog), { ssr: false });

const KNOWN_STATES = new Set(["active", "inactive", "failed", "activating", "deactivating", "reloading"]);

function stateTone(state: string): string {
  if (state === "active") return "bg-success-soft text-success-soft-foreground";
  if (state === "failed") return "bg-danger-soft text-danger-soft-foreground";
  if (state === "activating" || state === "reloading" || state === "deactivating") return "bg-info-soft text-info-soft-foreground";
  return "bg-secondary text-secondary-foreground";
}

interface Props {
  appId: string;
  appName: string;
  onMessage: (kind: "success" | "error", text: string) => void;
}

/** Systemd state of the app's service + start/stop/restart, logs and unit generation. */
export function ServiceControls({ appId, appName, onMessage }: Props) {
  const i18n = useI18n();
  const { t } = i18n;
  const client = useQueryClient();
  const [logsOpen, setLogsOpen] = useState(false);
  const [unitOpen, setUnitOpen] = useState(false);

  const status = useQuery({
    queryKey: queryKeys.serviceStatus(appId),
    queryFn: ({ signal }) => api.getServiceStatus(appId, { signal }),
    retry: false,
  });
  const raw = status.data?.status ?? "";
  const state = status.isError ? "unknown" : KNOWN_STATES.has(raw) ? raw : raw === "" ? "…" : "unknown";

  const control = useMutation({
    mutationFn: (action: "start" | "stop" | "restart") => api.controlService(appId, action),
    onSuccess: (res, action) => {
      client.setQueryData(queryKeys.serviceStatus(appId), { status: res.status });
      onMessage("success", t("apps.service.done", { action: t(`apps.service.${action}` as MessageKey) }));
    },
    onError: (err) => {
      onMessage("error", describeError(err, i18n));
      void client.invalidateQueries({ queryKey: queryKeys.serviceStatus(appId) });
    },
  });
  const busy = control.isPending;

  return (
    <div className="space-y-3" data-testid={`service-${appId}`}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs font-medium text-muted-foreground">{t("apps.service.label")}</span>
        <span
          data-testid={`service-status-${appId}`}
          data-state={state}
          className={cn("inline-flex items-center rounded-pill px-2.5 py-0.5 text-xs font-medium", stateTone(state))}
        >
          {state === "…" ? t("common.loading") : t(`apps.service.state.${state}` as MessageKey)}
        </span>
        <Button type="button" variant="ghost" size="icon" className="size-8" aria-label={t("apps.service.refresh")} onClick={() => void status.refetch()} disabled={status.isFetching}>
          <RefreshCw aria-hidden="true" className={cn(status.isFetching && "motion-safe:animate-spin")} />
        </Button>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" size="sm" disabled={busy} onClick={() => control.mutate("start")}>
          <Play aria-hidden="true" />
          {t("apps.service.start")}
        </Button>
        <Button type="button" variant="outline" size="sm" disabled={busy} onClick={() => control.mutate("stop")}>
          <Square aria-hidden="true" />
          {t("apps.service.stop")}
        </Button>
        <Button type="button" variant="outline" size="sm" disabled={busy} onClick={() => control.mutate("restart")}>
          <RotateCw aria-hidden="true" />
          {t("apps.service.restart")}
        </Button>
        <Button type="button" variant="secondary" size="sm" onClick={() => setLogsOpen(true)}>
          {t("apps.logs")}
        </Button>
        <Button type="button" variant="secondary" size="sm" onClick={() => setUnitOpen(true)}>
          {t("apps.unit")}
        </Button>
      </div>

      {logsOpen && (
        <LogsDialog
          open
          onOpenChange={setLogsOpen}
          live
          title={`${t("logs.app.title")} · ${appName}`}
          description={t("logs.app.desc")}
          fetchLog={(lines, signal) => api.getAppLogs(appId, lines, { signal })}
        />
      )}
      {unitOpen && (
        <ServiceUnitDialog
          appId={appId}
          open
          onOpenChange={setUnitOpen}
          onApplied={() => {
            onMessage("success", t("apps.unit.done"));
            void client.invalidateQueries({ queryKey: queryKeys.serviceStatus(appId) });
          }}
        />
      )}
    </div>
  );
}
