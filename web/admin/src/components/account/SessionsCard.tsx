"use client";

import "@/i18n/id/account";
import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";

export function SessionsCard({ onMessage }: { onMessage: (kind: "success" | "error", text: string) => void }) {
  const i18n = useI18n();
  const { t } = i18n;
  const [open, setOpen] = useState(false);
  const revoke = useMutation({
    mutationFn: () => api.revokeAllSessions(),
    onSuccess: () => {
      setOpen(false);
      onMessage("success", t("account.sessions.done"));
    },
    onError: (err) => {
      setOpen(false);
      onMessage("error", describeError(err, i18n));
    },
  });
  return (
    <section aria-labelledby="sess-title" className="space-y-3 rounded-card bg-card p-6 shadow-card" data-testid="sessions-card">
      <h2 id="sess-title" className="text-lg font-semibold">
        {t("account.sessions.title")}
      </h2>
      <p className="text-sm text-muted-foreground">{t("account.sessions.body")}</p>
      <Button type="button" variant="outline" onClick={() => setOpen(true)}>
        {t("account.sessions.revoke")}
      </Button>
      <ConfirmDialog open={open} onOpenChange={setOpen} title={t("account.sessions.confirmTitle")} body={t("account.sessions.confirmBody")} confirmLabel={t("account.sessions.confirm")} busy={revoke.isPending} onConfirm={() => revoke.mutate()} />
    </section>
  );
}
