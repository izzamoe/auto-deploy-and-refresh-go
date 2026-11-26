"use client";

// Manual deploy: pick a release from GitHub or, when the list is unavailable/empty, type the
// tag. Loaded on demand (next/dynamic); the releases query only runs while the dialog is open.
import "@/i18n/id/apps";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import { useEventsFresh } from "@/lib/events/provider";
import { invalidateAfterJob, queryKeys } from "@/lib/query";

interface Props {
  appId: string;
  appName: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onQueued: (tag: string) => void;
}

export function DeployDialog({ appId, appName, open, onOpenChange, onQueued }: Props) {
  const i18n = useI18n();
  const { t } = i18n;
  const client = useQueryClient();
  const fresh = useEventsFresh();
  const [tag, setTag] = useState("");

  const releases = useQuery({
    queryKey: queryKeys.releases(appId),
    queryFn: ({ signal }) => api.listReleases(appId, { signal }),
    enabled: open,
    retry: false,
  });
  const list = releases.data?.releases ?? [];
  const manual = releases.isError || (releases.isSuccess && list.length === 0);

  const deploy = useMutation({
    mutationFn: (value: string) => api.deployApp(appId, { tag: value }),
    onSuccess: (_res, value) => {
      if (!fresh) invalidateAfterJob(client, appId); // live: job_status{pending} updates the card
      setTag("");
      onQueued(value);
      onOpenChange(false);
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    const value = tag.trim();
    if (value) deploy.mutate(value);
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="rounded-card">
        <DialogHeader>
          <DialogTitle>{t("apps.deployDialog.title", { name: appName })}</DialogTitle>
          <DialogDescription>{manual ? t("apps.deployDialog.manualHint") : t("apps.deployDialog.tag")}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor={`deploy-tag-${appId}`}>{t("apps.deployDialog.tag")}</Label>
            {releases.isPending ? (
              <p className="flex h-9 items-center rounded-md border border-input px-3 text-sm text-muted-foreground">{t("apps.deployDialog.loading")}</p>
            ) : manual ? (
              <Input id={`deploy-tag-${appId}`} required autoFocus placeholder={t("apps.deployDialog.manual")} value={tag} onChange={(e) => setTag(e.target.value)} />
            ) : (
              <select
                id={`deploy-tag-${appId}`}
                required
                value={tag}
                onChange={(e) => setTag(e.target.value)}
                className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm"
              >
                <option value="" disabled>
                  {t("apps.deployDialog.select")}
                </option>
                {list.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </select>
            )}
          </div>
          {deploy.isError && (
            <p role="alert" className="rounded-xl bg-danger-soft px-3 py-2 text-sm text-danger-soft-foreground">
              {describeError(deploy.error, i18n)}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={deploy.isPending || releases.isPending || tag.trim() === ""}>
              {t("apps.deployDialog.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
