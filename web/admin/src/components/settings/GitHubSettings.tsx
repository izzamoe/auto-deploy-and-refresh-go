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
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import { queryKeys } from "@/lib/query";

/**
 * GitHub token (B27, FR-029). The server only ever returns a MASKED token; it is shown as
 * status text and NEVER put back into the input or sent back. An empty input keeps the stored
 * token; "remove" is an explicit, confirmed `clear: true`.
 */
export function GitHubSettings() {
  const i18n = useI18n();
  const { t } = i18n;
  const client = useQueryClient();
  const { notice, success, error, clear } = useNotice();
  const [token, setToken] = useState("");
  const [fieldError, setFieldError] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);

  const config = useQuery({ queryKey: queryKeys.github, queryFn: ({ signal }) => api.getGitHub({ signal }), refetchOnWindowFocus: false });

  const save = useMutation({
    mutationFn: (body: { token?: string; clear?: boolean }) => api.saveGitHub(body),
    onSuccess: (res, body) => {
      client.setQueryData(queryKeys.github, { config: res.config });
      setToken("");
      setConfirmOpen(false);
      success(t(body.clear ? "github.cleared" : "github.saved"));
    },
    onError: (err) => {
      setConfirmOpen(false);
      error(describeError(err, i18n));
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    clear();
    if (token.trim() === "") {
      setFieldError(t("github.tokenRequired"));
      return;
    }
    setFieldError("");
    save.mutate({ token: token.trim() });
  }

  const cfg = config.data?.config;

  return (
    <div className="space-y-4" data-testid="github-settings">
      <div className="rounded-card bg-card p-5 shadow-card">
        <h1 className="text-2xl font-semibold tracking-tight">{t("github.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("github.subtitle")}</p>
      </div>

      <Notice notice={notice} onDismiss={clear} />

      {config.isPending ? (
        <Skeleton className="h-48 rounded-card" />
      ) : config.isError ? (
        <div role="alert" className="space-y-3 rounded-card bg-card p-6 shadow-card">
          <p>{t("github.loadError")}</p>
          <p className="text-sm text-muted-foreground">{describeError(config.error, i18n)}</p>
          <Button type="button" onClick={() => void config.refetch()}>
            {t("common.retry")}
          </Button>
        </div>
      ) : (
        <form onSubmit={submit} className="space-y-5 rounded-card bg-card p-6 shadow-card">
          <div className="rounded-2xl bg-secondary p-4 text-sm" data-testid="github-status">
            <p className="font-medium">{cfg?.hasToken ? t("github.status.set", { masked: cfg.token }) : t("github.status.none")}</p>
            {cfg?.source && <p className="text-muted-foreground">{t("github.source", { source: cfg.source })}</p>}
          </div>

          <div className="grid gap-1.5">
            <Label htmlFor="gh-token">{t("github.token")}</Label>
            <Input
              id="gh-token"
              name="token"
              type="password"
              autoComplete="off"
              spellCheck={false}
              value={token}
              onChange={(e) => setToken(e.target.value)}
              aria-invalid={fieldError ? true : undefined}
              aria-describedby={fieldError ? "gh-token-error gh-token-hint" : "gh-token-hint"}
            />
            {fieldError && (
              <p id="gh-token-error" role="alert" className="text-sm text-danger-soft-foreground">
                {fieldError}
              </p>
            )}
            <p id="gh-token-hint" className="text-sm text-muted-foreground">
              {t("github.token.hint")}
            </p>
          </div>

          <div className="flex flex-col-reverse gap-3 sm:flex-row sm:justify-between">
            <Button type="button" variant="destructive" disabled={!cfg?.hasToken || save.isPending} onClick={() => setConfirmOpen(true)}>
              {t("github.clear")}
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {t("github.save")}
            </Button>
          </div>
        </form>
      )}

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t("github.clear.title")}
        body={t("github.clear.body")}
        confirmLabel={t("github.clear.confirm")}
        busy={save.isPending}
        onConfirm={() => save.mutate({ clear: true })}
      />
    </div>
  );
}
