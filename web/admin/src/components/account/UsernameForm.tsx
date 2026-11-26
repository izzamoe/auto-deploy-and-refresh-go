"use client";

import "@/i18n/id/account";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import { useTopic } from "@/lib/events/provider";
import { queryKeys } from "@/lib/query";

/** Change username (FR-005, B3): the server re-issues the session cookie; the header greeting follows. */
export function UsernameForm({ onMessage }: { onMessage: (kind: "success" | "error", text: string) => void }) {
  const i18n = useI18n();
  const { t } = i18n;
  const client = useQueryClient();
  useTopic("account"); // account.changed{profile} from another session refreshes the session name
  const [password, setPassword] = useState("");
  const [username, setUsername] = useState("");

  const change = useMutation({
    mutationFn: () => api.changeUsername({ currentPassword: password, newUsername: username.trim() }),
    onSuccess: () => {
      setPassword("");
      setUsername("");
      onMessage("success", t("account.username.done"));
      void client.invalidateQueries({ queryKey: queryKeys.session });
    },
    onError: (err) => onMessage("error", describeError(err, i18n)),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    if (username.trim() !== "") change.mutate();
  }

  return (
    <form onSubmit={submit} aria-labelledby="un-title" className="space-y-4 rounded-card bg-card p-6 shadow-card" data-testid="username-form">
      <h2 id="un-title" className="text-lg font-semibold">
        {t("account.username.title")}
      </h2>
      <div className="grid gap-1.5">
        <Label htmlFor="acc-un-new">{t("account.username.new")}</Label>
        <Input id="acc-un-new" autoComplete="username" autoCapitalize="none" spellCheck={false} required maxLength={64} value={username} onChange={(e) => setUsername(e.target.value)} />
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="acc-un-password">{t("account.username.current")}</Label>
        <Input id="acc-un-password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
      </div>
      <Button type="submit" disabled={change.isPending}>
        {t("account.username.submit")}
      </Button>
    </form>
  );
}
