"use client";

import "@/i18n/id/account";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { CopyableSnippet } from "@/components/common/CopyableSnippet";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { useI18n } from "@/i18n";
import { api, describeError } from "@/lib/api/client";
import type { Schemas } from "@/lib/api/client";
import { queryKeys } from "@/lib/query";

type Token = Schemas["ApiToken"];

/**
 * API tokens (FR-014). The full value (`adt_...`) exists only in this component's state right
 * after creating: it is shown once with a copy button and dropped when dismissed (and the
 * mutation result is reset), so the list - and the query cache - only ever hold the prefix.
 */
export function TokensCard({ onMessage }: { onMessage: (kind: "success" | "error", text: string) => void }) {
  const i18n = useI18n();
  const { t, formatDateTime } = i18n;
  const client = useQueryClient();
  const [name, setName] = useState("");
  const [fresh, setFresh] = useState<{ name: string; token: string } | null>(null);
  const [revoking, setRevoking] = useState<Token | null>(null);

  const list = useQuery({ queryKey: queryKeys.tokens, queryFn: ({ signal }) => api.listTokens({ signal }), refetchOnWindowFocus: false });

  const create = useMutation({
    mutationFn: (value: string) => api.createToken({ name: value }),
    onSuccess: (res) => {
      setFresh({ name: res.name, token: res.token });
      setName("");
      onMessage("success", t("account.tokens.created"));
      void client.invalidateQueries({ queryKey: queryKeys.tokens });
    },
    onError: (err) => onMessage("error", describeError(err, i18n)),
  });
  const revoke = useMutation({
    mutationFn: (tok: Token) => api.revokeToken(tok.id),
    onSuccess: () => {
      setRevoking(null);
      onMessage("success", t("account.tokens.revokedDone"));
      void client.invalidateQueries({ queryKey: queryKeys.tokens });
    },
    onError: (err) => {
      setRevoking(null);
      onMessage("error", describeError(err, i18n));
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    const value = name.trim();
    if (value) create.mutate(value);
  }
  function dismiss() {
    setFresh(null);
    create.reset(); // drop the full token from the mutation state as well
  }

  const tokens = list.data?.tokens ?? [];

  return (
    <section aria-labelledby="tok-title" className="space-y-4 rounded-card bg-card p-6 shadow-card" data-testid="tokens-card">
      <div>
        <h2 id="tok-title" className="text-lg font-semibold">
          {t("account.tokens.title")}
        </h2>
        <p className="text-sm text-muted-foreground">{t("account.tokens.body")}</p>
      </div>

      {fresh && (
        <div className="space-y-3 rounded-2xl bg-warning-soft p-4" data-testid="token-fresh">
          <p role="alert" className="text-sm text-warning-soft-foreground">
            {t("account.tokens.once")}
          </p>
          <CopyableSnippet label={`${t("account.tokens.value")} · ${fresh.name}`} code={fresh.token} testId="token-fresh-value" />
          <Button type="button" onClick={dismiss}>
            {t("account.tokens.dismiss")}
          </Button>
        </div>
      )}

      <form onSubmit={submit} className="flex flex-wrap items-end gap-3">
        <div className="grid min-w-48 flex-1 gap-1.5">
          <Label htmlFor="tok-name">{t("account.tokens.name")}</Label>
          <Input id="tok-name" autoComplete="off" maxLength={64} required value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <Button type="submit" disabled={create.isPending || name.trim() === ""}>
          {t("account.tokens.create")}
        </Button>
      </form>

      {list.isPending ? (
        <Skeleton className="h-16" />
      ) : list.isError ? (
        <div role="alert" className="space-y-2 text-sm">
          <p>{t("account.tokens.loadError")}</p>
          <p className="text-muted-foreground">{describeError(list.error, i18n)}</p>
          <Button type="button" size="sm" onClick={() => void list.refetch()}>
            {t("common.retry")}
          </Button>
        </div>
      ) : tokens.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="tokens-empty">
          {t("account.tokens.empty")}
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[40rem] border-collapse text-sm" data-testid="tokens-table">
            <caption className="sr-only">{t("account.tokens.list")}</caption>
            <thead>
              <tr className="text-left text-muted-foreground">
                {(["name", "prefix", "created", "lastUsed", "status", "actions"] as const).map((c) => (
                  <th key={c} scope="col" className="px-3 py-2 font-medium">
                    {t(`account.tokens.col.${c}`)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {tokens.map((tok) => (
                <tr key={tok.id} className="border-t border-border" data-testid={`token-${tok.id}`}>
                  <th scope="row" className="px-3 py-3 text-left font-medium">
                    {tok.name}
                  </th>
                  <td className="px-3 py-3 font-mono">{tok.prefix}…</td>
                  <td className="whitespace-nowrap px-3 py-3">{formatDateTime(tok.createdAt)}</td>
                  <td className="whitespace-nowrap px-3 py-3">{tok.lastUsedAt ? formatDateTime(tok.lastUsedAt) : t("account.tokens.never")}</td>
                  <td className="px-3 py-3">{tok.revokedAt ? t("account.tokens.revokedAt", { time: formatDateTime(tok.revokedAt) }) : t("account.tokens.active")}</td>
                  <td className="px-3 py-3">
                    {!tok.revokedAt && (
                      <Button type="button" size="sm" variant="outline" aria-label={t("account.tokens.revokeFor", { name: tok.name })} onClick={() => setRevoking(tok)}>
                        {t("account.tokens.revoke")}
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(open) => {
          if (!open) setRevoking(null);
        }}
        title={t("account.tokens.revokeTitle", { name: revoking?.name ?? "" })}
        body={t("account.tokens.revokeBody")}
        confirmLabel={t("account.tokens.revokeConfirm")}
        busy={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking)}
      />
    </section>
  );
}
