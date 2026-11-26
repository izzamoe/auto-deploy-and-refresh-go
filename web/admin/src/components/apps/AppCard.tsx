"use client";

import "@/i18n/id/apps";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import dynamic from "next/dynamic";
import Link from "next/link";
import { useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { ConfirmDialog, confirmIntent } from "@/components/common/ConfirmDialog";
import { renderNothing } from "@/components/common/lazy";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import type { Schemas } from "@/lib/api/client";
import { effectiveStatus } from "@/lib/events/store";
import { announce } from "@/components/common/LiveAnnouncer";
import type { MessageKey } from "@/i18n";
import { useEventStore, useEventsFresh, useJobProgress, useServiceState, useTopicFrames } from "@/lib/events/provider";
import { invalidateAfterJob, queryKeys, removeAppFromCache, upsertAppInCache } from "@/lib/query";
import { CARD_TITLE } from "@/components/common/cardStyles";
import { cn } from "@/lib/utils";
import { ProgressBadge } from "./ProgressBadge";
import { ServiceControls } from "./ServiceControls";

const DeployDialog = dynamic(() => import("./DeployDialog").then((m) => m.DeployDialog).catch(renderNothing), { ssr: false });

const ACTIVE = new Set(["pending", "in_progress", "cancel_requested"]);

export type AppItem = Schemas["App"];

interface Props {
  app: AppItem;
  onMessage: (kind: "success" | "error", text: string) => void;
}

export function AppCard({ app, onMessage }: Props) {
  const i18n = useI18n();
  const { t, formatRelative } = i18n;
  const client = useQueryClient();
  const live = useJobProgress(app.lastJobId);
  const fresh = useEventsFresh();

  // FR-022: changes of THIS app made in another session are announced politely (T061).
  const store = useEventStore();
  const service = useServiceState(app.id);
  const shown = useRef({ enabled: app.enabled, service });
  shown.current = { enabled: app.enabled, service };
  useTopicFrames((f) => {
    if (!("appId" in f) || f.appId !== app.id) return;
    // The ref follows every frame too: two quick events must compare with each other, not with
    // the last render (the cache notifies its observers asynchronously).
    const before = shown.current;
    if (f.kind === "service.status") {
      shown.current = { ...before, service: f.state };
      if (f.state !== before.service) announce(t("live.service", { name: app.name, state: t(`apps.service.state.${f.state}` as MessageKey) }));
      return;
    }
    if (f.kind === "app.upserted") shown.current = { ...before, enabled: f.app.enabled };
    if (store.isOwnOrigin(f.origin)) return; // this tab did it and already said so
    if (f.kind === "app.upserted" && f.app.enabled !== before.enabled) announce(t(f.app.enabled ? "live.app.enabled" : "live.app.disabled", { name: f.app.name }));
    else if (f.kind === "app.deleted") announce(t("live.app.deleted", { name: app.name }));
  });
  const status = effectiveStatus(app.lastJobStatus, app.lastDeployTime, live);
  const active = status !== undefined && ACTIVE.has(status);

  const [deployOpen, setDeployOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [cancelOpen, setCancelOpen] = useState(false);
  const [removeUnit, setRemoveUnit] = useState(false);

  const fail = (err: unknown) => onMessage("error", describeError(err, i18n));

  const toggle = useMutation({
    mutationFn: (enabled: boolean) => api.toggleApp(app.id, enabled),
    onSuccess: (res, enabled) => {
      onMessage("success", t(enabled ? "apps.enabled" : "apps.disabled"));
      upsertAppInCache(client, res.app); // the response is the new row: no refetch (002)
    },
    onError: fail,
  });
  const remove = useMutation({
    mutationFn: () => api.deleteApp(app.id, removeUnit),
    onSuccess: () => {
      onMessage("success", t("apps.delete.done"));
      setDeleteOpen(false);
      removeAppFromCache(client, app.id);
      void client.invalidateQueries({ queryKey: queryKeys.stats, refetchType: "none" });
    },
    onError: (err) => {
      setDeleteOpen(false);
      fail(err);
    },
  });
  const cancel = useMutation({
    mutationFn: () => api.cancelAppJobs(app.id),
    onSuccess: () => {
      onMessage("success", t("apps.cancel.done"));
      setCancelOpen(false);
      if (!fresh) invalidateAfterJob(client, app.id); // live: cancel_requested / job_status update the card
    },
    onError: (err) => {
      setCancelOpen(false);
      fail(err);
    },
  });

  const fields: [string, string][] = [
    [t("apps.field.repo"), app.githubRepo],
    [t("apps.field.binary"), app.binaryPath],
    [t("apps.field.artifact"), app.artifactName],
  ];

  return (
    <article
      aria-label={app.name}
      data-testid={`app-card-${app.id}`}
      data-app-id={app.id}
      className="flex flex-col gap-4 rounded-card bg-card p-5 shadow-card"
    >
      <header className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className={`truncate leading-tight ${CARD_TITLE}`}>{app.name}</h2>
          <p className="truncate text-sm text-muted-foreground">{app.serviceName}</p>
        </div>
        <span
          className={cn(
            "shrink-0 rounded-pill px-2.5 py-0.5 text-xs font-medium",
            app.enabled ? "bg-success-soft text-success-soft-foreground" : "bg-secondary text-secondary-foreground",
          )}
        >
          {app.enabled ? t("common.enabled") : t("common.disabled")}
        </span>
      </header>

      <dl className="grid gap-1.5 text-sm">
        {fields.map(([label, value]) => (
          <div key={label} className="flex gap-2">
            <dt className="w-16 shrink-0 text-muted-foreground">{label}</dt>
            <dd className="min-w-0 break-all font-medium">{value}</dd>
          </div>
        ))}
        {app.lastDeployTag && (
          <div className="flex gap-2">
            <dt className="w-16 shrink-0 text-muted-foreground">{t("apps.field.lastDeploy")}</dt>
            <dd className="min-w-0 font-medium">
              {app.lastDeployTag}
              {app.lastDeployTime ? <span className="font-normal text-muted-foreground"> · {formatRelative(app.lastDeployTime)}</span> : null}
            </dd>
          </div>
        )}
      </dl>

      <ProgressBadge progress={live} status={status} message={app.lastDeployStatus} />

      <div role="group" aria-label={t("apps.actionsLabel", { name: app.name })} className="flex flex-wrap gap-2">
        {app.enabled && (
          <Button type="button" size="sm" onClick={() => setDeployOpen(true)}>
            {t("apps.deploy")}
          </Button>
        )}
        <Button asChild variant="outline" size="sm">
          <Link href={`/apps/edit/?id=${encodeURIComponent(app.id)}`}>{t("apps.edit")}</Link>
        </Button>
        <Button asChild variant="outline" size="sm">
          <Link href={`/history/?appId=${encodeURIComponent(app.id)}`}>{t("apps.history")}</Link>
        </Button>
        <Button type="button" variant="secondary" size="sm" disabled={toggle.isPending} onClick={() => toggle.mutate(!app.enabled)}>
          {app.enabled ? t("apps.disable") : t("apps.enable")}
        </Button>
        {active && (
          <Button type="button" variant="outline" size="sm" {...confirmIntent} onClick={() => setCancelOpen(true)}>
            {t("apps.cancelDeploy")}
          </Button>
        )}
        <Button type="button" variant="destructive" size="sm" {...confirmIntent} onClick={() => setDeleteOpen(true)}>
          {t("common.delete")}
        </Button>
      </div>

      <ServiceControls appId={app.id} appName={app.name} onMessage={onMessage} />

      {deployOpen && <DeployDialog appId={app.id} appName={app.name} open onOpenChange={setDeployOpen} onQueued={(tag) => onMessage("success", t("apps.deployDialog.queued", { tag }))} />}

      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title={t("apps.delete.title")}
        body={t("apps.delete.body", { name: app.name })}
        cancelLabel={t("apps.delete.keep")}
        confirmLabel={t("apps.delete.confirm")}
        busy={remove.isPending}
        onConfirm={() => remove.mutate()}
      >
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={removeUnit} onChange={(e) => setRemoveUnit(e.target.checked)} className="size-4 accent-primary" />
          {t("apps.delete.removeUnit")}
        </label>
      </ConfirmDialog>

      <ConfirmDialog
        open={cancelOpen}
        onOpenChange={setCancelOpen}
        title={t("apps.cancel.title")}
        body={t("apps.cancel.body", { name: app.name })}
        cancelLabel={t("apps.cancel.keep")}
        confirmLabel={t("apps.cancel.confirm")}
        busy={cancel.isPending}
        onConfirm={() => cancel.mutate()}
      />
    </article>
  );
}
