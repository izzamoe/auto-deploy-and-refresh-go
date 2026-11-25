"use client";

import "@/i18n/id/settings";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { Notice, useNotice } from "@/components/common/Notice";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import type { Body, Schemas } from "@/lib/api/client";
import { queryKeys } from "@/lib/query";

type Config = Schemas["TelegramConfig"];
type Update = Body<"saveTelegramConfig">;
type Secret = "appHash" | "botToken";

function SecretField({ id, label, masked, value, onChange, error, onClear, clearLabel }: { id: string; label: string; masked: string; value: string; onChange: (v: string) => void; error?: string; onClear: () => void; clearLabel: string }) {
  const { t } = useI18n();
  const stored = masked !== "";
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <p className="text-sm font-medium" data-testid={`${id}-status`}>
        {stored ? t("telegram.secret.stored", { masked }) : t("telegram.secret.none")}
      </p>
      <Input
        id={id}
        name={id}
        type="password"
        autoComplete="off"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={error ? true : undefined}
        aria-describedby={`${id}-hint${error ? ` ${id}-error` : ""}`}
      />
      {error && (
        <p id={`${id}-error`} role="alert" className="text-sm text-danger-soft-foreground">
          {error}
        </p>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p id={`${id}-hint`} className="text-sm text-muted-foreground">
          {t("telegram.secret.newHint")}
        </p>
        {stored && (
          <Button type="button" size="sm" variant="outline" onClick={onClear}>
            {clearLabel}
          </Button>
        )}
      </div>
    </div>
  );
}

function TelegramForm({ cfg }: { cfg: Config }) {
  const i18n = useI18n();
  const { t } = i18n;
  const client = useQueryClient();
  const { notice, success, error, clear } = useNotice();

  // Initial values are taken ONCE from the loaded config; secrets start empty (= keep).
  const [appId, setAppId] = useState(cfg.appId ? String(cfg.appId) : "");
  const [chat, setChat] = useState(cfg.chatUsername);
  const [enabled, setEnabled] = useState(cfg.enabled);
  const [appHash, setAppHash] = useState("");
  const [botToken, setBotToken] = useState("");
  const [errors, setErrors] = useState<Partial<Record<"appId" | "appHash" | "botToken" | "chat", string>>>({});
  const [confirm, setConfirm] = useState<Secret | null>(null);

  const save = useMutation({
    mutationFn: (body: Update) => api.saveTelegram(body),
    onSuccess: (res, body) => {
      client.setQueryData(queryKeys.telegram, { config: res.config });
      setAppHash("");
      setBotToken("");
      setAppId(res.config.appId ? String(res.config.appId) : "");
      setChat(res.config.chatUsername);
      setEnabled(res.config.enabled);
      setConfirm(null);
      if (body.clearAppHash || body.clearBotToken) success(t("telegram.cleared", { what: t(body.clearAppHash ? "telegram.what.appHash" : "telegram.what.botToken") }));
      else success(t("telegram.saved"));
    },
    onError: (err) => {
      setConfirm(null);
      error(describeError(err, i18n));
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    clear();
    const next: typeof errors = {};
    const id = Number(appId);
    if (enabled) {
      const required = t("telegram.required");
      if (!(id > 0)) next.appId = required;
      if (appHash.trim() === "" && cfg.appHash === "") next.appHash = required;
      if (botToken.trim() === "" && cfg.botToken === "") next.botToken = required;
      if (chat.trim() === "") next.chat = required;
    }
    setErrors(next);
    if (Object.keys(next).length > 0) return;
    // appId / chatUsername / enabled are always sent (the server overwrites them); a secret is
    // sent ONLY when a new value was typed (empty = keep). The masked value never goes back.
    save.mutate({
      appId: Number.isFinite(id) && id > 0 ? id : 0,
      chatUsername: chat.trim(),
      enabled,
      clearAppHash: false,
      clearBotToken: false,
      ...(appHash.trim() ? { appHash: appHash.trim() } : {}),
      ...(botToken.trim() ? { botToken: botToken.trim() } : {}),
    });
  }

  function confirmClear() {
    if (!confirm) return;
    // Uses the SAVED non-secret values (no silent save of other pending edits) and turns the
    // notifications off, because they cannot run without the removed secret.
    save.mutate({
      appId: cfg.appId,
      chatUsername: cfg.chatUsername,
      enabled: false,
      clearAppHash: confirm === "appHash",
      clearBotToken: confirm === "botToken",
    });
  }

  return (
    <div className="space-y-4" data-testid="telegram-settings">
      <div className="rounded-card bg-card p-5 shadow-card">
        <h1 className="text-2xl font-semibold tracking-tight">{t("telegram.title")}</h1>
        <p className="max-w-prose text-sm text-muted-foreground">{t("telegram.subtitle")}</p>
      </div>

      <Notice notice={notice} onDismiss={clear} />

      <form onSubmit={submit} noValidate className="grid gap-5 rounded-card bg-card p-6 shadow-card">
        <div className="grid gap-5 sm:grid-cols-2">
          <div className="grid gap-1.5">
            <Label htmlFor="tg-app-id">{t("telegram.appId")}</Label>
            <Input id="tg-app-id" name="appId" type="number" inputMode="numeric" min={0} autoComplete="off" value={appId} onChange={(e) => setAppId(e.target.value)} aria-invalid={errors.appId ? true : undefined} aria-describedby={errors.appId ? "tg-app-id-error" : undefined} />
            {errors.appId && (
              <p id="tg-app-id-error" role="alert" className="text-sm text-danger-soft-foreground">
                {errors.appId}
              </p>
            )}
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="tg-chat">{t("telegram.chatUsername")}</Label>
            <Input id="tg-chat" name="chatUsername" autoComplete="off" autoCapitalize="none" value={chat} onChange={(e) => setChat(e.target.value)} aria-invalid={errors.chat ? true : undefined} aria-describedby={errors.chat ? "tg-chat-error tg-chat-hint" : "tg-chat-hint"} />
            {errors.chat && (
              <p id="tg-chat-error" role="alert" className="text-sm text-danger-soft-foreground">
                {errors.chat}
              </p>
            )}
            <p id="tg-chat-hint" className="text-sm text-muted-foreground">
              {t("telegram.chatUsername.hint")}
            </p>
          </div>
        </div>

        <SecretField id="tg-app-hash" label={t("telegram.appHash")} clearLabel={t("telegram.clearAppHash")} masked={cfg.appHash} value={appHash} onChange={setAppHash} error={errors.appHash} onClear={() => setConfirm("appHash")} />
        <SecretField id="tg-bot-token" label={t("telegram.botToken")} clearLabel={t("telegram.clearBotToken")} masked={cfg.botToken} value={botToken} onChange={setBotToken} error={errors.botToken} onClear={() => setConfirm("botToken")} />

        <div className="flex items-center justify-between gap-4 rounded-2xl bg-secondary p-4">
          <div className="space-y-0.5">
            <Label htmlFor="tg-enabled">{t("telegram.enabled")}</Label>
            <p id="tg-enabled-hint" className="text-sm text-muted-foreground">
              {t("telegram.enabled.hint")}
            </p>
          </div>
          <Switch id="tg-enabled" checked={enabled} onCheckedChange={setEnabled} aria-describedby="tg-enabled-hint" />
        </div>

        <div className="flex justify-end">
          <Button type="submit" disabled={save.isPending}>
            {t("telegram.save")}
          </Button>
        </div>
      </form>

      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(open) => {
          if (!open) setConfirm(null);
        }}
        title={t("telegram.clear.title", { what: t(confirm === "appHash" ? "telegram.what.appHash" : "telegram.what.botToken") })}
        body={t("telegram.clear.body")}
        confirmLabel={t("telegram.clear.confirm")}
        busy={save.isPending}
        onConfirm={confirmClear}
      />
    </div>
  );
}

export function TelegramSettings() {
  const i18n = useI18n();
  const { t } = i18n;
  const config = useQuery({ queryKey: queryKeys.telegram, queryFn: ({ signal }) => api.getTelegram({ signal }), staleTime: Infinity, refetchOnWindowFocus: false });

  if (config.isPending) return <Skeleton className="h-96 rounded-card" />;
  if (config.isError) {
    return (
      <div role="alert" className="space-y-3 rounded-card bg-card p-6 shadow-card">
        <p>{t("telegram.loadError")}</p>
        <p className="text-sm text-muted-foreground">{describeError(config.error, i18n)}</p>
        <Button type="button" onClick={() => void config.refetch()}>
          {t("common.retry")}
        </Button>
      </div>
    );
  }
  return <TelegramForm cfg={config.data.config} />;
}
