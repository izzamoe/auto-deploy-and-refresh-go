"use client";

import "@/i18n/id/account";
import "@/i18n/id/password";
import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";

/** Change password (FR-005). The server re-issues the current session; other sessions end. */
export function PasswordForm({ onMessage }: { onMessage: (kind: "success" | "error", text: string) => void }) {
  const i18n = useI18n();
  const { t } = i18n;
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [mismatch, setMismatch] = useState(false);

  const change = useMutation({
    mutationFn: () => api.changePassword({ currentPassword: current, newPassword: next }),
    onSuccess: () => {
      setCurrent("");
      setNext("");
      setAgain("");
      onMessage("success", t("account.password.done"));
    },
    onError: (err) => onMessage("error", describeError(err, i18n)),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    if (next !== again) {
      setMismatch(true);
      return;
    }
    setMismatch(false);
    change.mutate();
  }

  return (
    <form onSubmit={submit} aria-labelledby="pw-title" className="space-y-4 rounded-card bg-card p-6 shadow-card" data-testid="password-form">
      <h2 id="pw-title" className="text-lg font-semibold">
        {t("account.password.title")}
      </h2>
      <div className="grid gap-1.5">
        <Label htmlFor="acc-pw-current">{t("account.password.current")}</Label>
        <Input id="acc-pw-current" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="acc-pw-new">{t("account.password.new")}</Label>
        <Input id="acc-pw-new" type="password" autoComplete="new-password" required value={next} onChange={(e) => setNext(e.target.value)} />
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="acc-pw-again">{t("account.password.confirm")}</Label>
        <Input id="acc-pw-again" type="password" autoComplete="new-password" required value={again} onChange={(e) => setAgain(e.target.value)} aria-invalid={mismatch ? true : undefined} aria-describedby={mismatch ? "acc-pw-mismatch" : undefined} />
        {mismatch && (
          <p id="acc-pw-mismatch" role="alert" className="text-sm text-danger-soft-foreground">
            {t("account.password.mismatch")}
          </p>
        )}
      </div>
      <Button type="submit" disabled={change.isPending}>
        {change.isPending ? t("account.password.submitting") : t("account.password.submit")}
      </Button>
    </form>
  );
}
