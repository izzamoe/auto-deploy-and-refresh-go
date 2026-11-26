"use client";

import "@/i18n/id/apps";
import "@/i18n/id/unit";
import { useMutation } from "@tanstack/react-query";
import { Play, RefreshCw, RotateCw, Square } from "lucide-react";
import dynamic from "next/dynamic";
import { useEffect, useRef, useState } from "react";
import { renderNothing } from "@/components/common/lazy";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import type { MessageKey } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import { useEventStore, useEventsFallback, useServiceState, useTopicConfirmed } from "@/lib/events/provider";
import { SERVICE_STATES } from "@/lib/events/types";
import type { ServiceState } from "@/lib/events/types";
import { cn } from "@/lib/utils";

// Heavy dialogs are fetched only when first opened (Q-20).
const LogsDialog = dynamic(() => import("./LogsDialog").then((m) => m.LogsDialog).catch(renderNothing), { ssr: false });
const ServiceUnitDialog = dynamic(() => import("./ServiceUnitDialog").then((m) => m.ServiceUnitDialog).catch(renderNothing), { ssr: false });

const KNOWN_STATES = new Set(["active", "inactive", "failed", "activating", "deactivating", "reloading"]);
const toState = (raw: string | undefined): ServiceState => ((SERVICE_STATES as readonly string[]).includes(raw ?? "") ? (raw as ServiceState) : "unknown");

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

/**
 * Systemd state of the app's service + start/stop/restart, logs and unit generation.
 * 002 (FR-007, SC-005): the state comes from topic `service` (the subscribed ack, then only
 * changes); no request per card. A card whose app has no state yet (an app created while the
 * shared watcher was already running: its first observation is not a change) asks ONCE with
 * GET /service/status per appearance; later changes still arrive as events. The refresh button
 * stays a REST call.
 */
export function ServiceControls({ appId, appName, onMessage }: Props) {
  const i18n = useI18n();
  const { t } = i18n;
  const store = useEventStore();
  const [logsOpen, setLogsOpen] = useState(false);
  const [unitOpen, setUnitOpen] = useState(false);

  const live = useServiceState(appId);
  const confirmed = useTopicConfirmed("service");
  const fallback = useEventsFallback();
  const [fetching, setFetching] = useState(false);
  const [failed, setFailed] = useState(false);
  const asked = useRef(false);

  const refresh = () => {
    setFetching(true);
    api
      .getServiceStatus(appId)
      .then((res) => {
        setFailed(false);
        store.setService(appId, toState(res.status));
      })
      .catch(() => setFailed(true))
      .finally(() => setFetching(false));
  };

  // One targeted request per appearance when the topic cannot tell (no state for this app once
  // the ack arrived, or the socket has not been live for a while: Q-4 fallback).
  const known = live !== undefined;
  const mayAsk = !known && (confirmed || fallback);
  useEffect(() => {
    if (!mayAsk || asked.current) return;
    asked.current = true;
    refresh();
  }, [mayAsk]); // eslint-disable-line react-hooks/exhaustive-deps

  const raw = live ?? "";
  const state = live === undefined && failed ? "unknown" : KNOWN_STATES.has(raw) ? raw : raw === "" ? "…" : "unknown";

  const control = useMutation({
    mutationFn: (action: "start" | "stop" | "restart") => api.controlService(appId, action),
    onSuccess: (res, action) => {
      store.setService(appId, toState(res.status));
      onMessage("success", t("apps.service.done", { action: t(`apps.service.${action}` as MessageKey) }));
    },
    onError: (err) => {
      onMessage("error", describeError(err, i18n));
      refresh();
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
        <Button type="button" variant="ghost" size="icon" className="size-8" aria-label={t("apps.service.refresh")} onClick={refresh} disabled={fetching}>
          <RefreshCw aria-hidden="true" className={cn(fetching && "motion-safe:animate-spin")} />
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
            if (fallback) refresh(); // live: the server refreshes the watcher and pushes service.status
          }}
        />
      )}
    </div>
  );
}
