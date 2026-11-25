"use client";

// Preview-then-confirm for generating the systemd unit (it writes /etc/systemd/system as
// root, so applying is deliberately a second explicit step). Shared with the app form (T089).
import { useMutation, useQuery } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";

interface Props {
  appId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onApplied: () => void;
}

export function ServiceUnitDialog({ appId, open, onOpenChange, onApplied }: Props) {
  const i18n = useI18n();
  const { t } = i18n;
  const preview = useQuery({
    queryKey: ["service-unit-preview", appId],
    queryFn: ({ signal }) => api.previewServiceUnit(appId, { signal }),
    enabled: open,
    retry: false,
    gcTime: 0, // always preview the current env/args, never a cached unit
  });
  const apply = useMutation({
    mutationFn: () => api.applyServiceUnit(appId),
    onSuccess: () => {
      onApplied();
      onOpenChange(false);
    },
  });
  const error = preview.isError ? preview.error : apply.isError ? apply.error : undefined;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl rounded-card">
        <DialogHeader>
          <DialogTitle>{t("apps.unit.title")}</DialogTitle>
          <DialogDescription>{t("apps.unit.body")}</DialogDescription>
        </DialogHeader>
        {error !== undefined && (
          <p role="alert" className="rounded-xl bg-danger-soft px-3 py-2 text-sm text-danger-soft-foreground">
            {describeError(error, i18n)}
          </p>
        )}
        <pre tabIndex={0} className="max-h-96 overflow-auto whitespace-pre-wrap rounded-xl bg-muted p-4 text-xs" data-testid="unit-preview">
          {preview.isPending ? t("common.loading") : (preview.data?.unit ?? "")}
        </pre>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button type="button" variant="destructive" disabled={!preview.isSuccess || apply.isPending} onClick={() => apply.mutate()}>
            {apply.isPending ? t("apps.unit.applying") : t("apps.unit.apply")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
