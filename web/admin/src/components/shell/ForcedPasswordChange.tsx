"use client";

import "@/i18n/id/password";
import { useState } from "react";
import type { FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";

/** Blocks the whole UI until the initial password is replaced (FR-070, FR-005). */
export function ForcedPasswordChange({ onDone }: { onDone: () => void }) {
  const i18n = useI18n();
  const { t } = i18n;
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    if (next !== again) {
      setError(t("account.password.mismatch"));
      return;
    }
    setBusy(true);
    try {
      await api.changePassword({ currentPassword: current, newPassword: next });
      onDone();
    } catch (err) {
      setError(describeError(err, i18n));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="mx-auto flex min-h-dvh max-w-md items-center p-4" data-testid="forced-password-change">
      <form onSubmit={submit} className="w-full space-y-4 rounded-card bg-card p-6 shadow-card">
        <h1 className="text-xl font-semibold">{t("account.forced.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("account.forced.body")}</p>
        <div className="space-y-1.5">
          <Label htmlFor="pw-current">{t("account.password.current")}</Label>
          <Input id="pw-current" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="pw-new">{t("account.password.new")}</Label>
          <Input id="pw-new" type="password" autoComplete="new-password" required value={next} onChange={(e) => setNext(e.target.value)} />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="pw-again">{t("account.password.confirm")}</Label>
          <Input id="pw-again" type="password" autoComplete="new-password" required value={again} onChange={(e) => setAgain(e.target.value)} />
        </div>
        {error && (
          <p role="alert" className="rounded-xl bg-danger-soft px-3 py-2 text-sm text-danger-soft-foreground">
            {error}
          </p>
        )}
        <Button type="submit" className="w-full" disabled={busy}>
          {busy ? t("account.password.submitting") : t("account.password.submit")}
        </Button>
      </form>
    </main>
  );
}
