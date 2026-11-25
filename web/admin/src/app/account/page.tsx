"use client";

import "@/i18n/id/account";
import { Notice, useNotice } from "@/components/common/Notice";
import { PasswordForm } from "@/components/account/PasswordForm";
import { SessionsCard } from "@/components/account/SessionsCard";
import { TokensCard } from "@/components/account/TokensCard";
import { UsernameForm } from "@/components/account/UsernameForm";
import { useSession } from "@/components/shell/SessionGate";
import { useI18n } from "@/i18n";

export default function AccountPage() {
  const { t, formatDateTime } = useI18n();
  const session = useSession();
  const { notice, success, error, clear } = useNotice();
  const onMessage = (kind: "success" | "error", text: string) => (kind === "success" ? success(text) : error(text));

  return (
    <div className="space-y-4" data-testid="account-page">
      <div className="rounded-card bg-card p-5 shadow-card">
        <h1 className="text-2xl font-semibold tracking-tight">{t("account.page.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("account.page.subtitle")}</p>
        <p className="mt-2 text-sm">
          <span className="font-medium">{t("account.profile.signedInAs", { name: session.username })}</span>
          <span className="text-muted-foreground"> · {t("account.profile.session", { time: formatDateTime(session.expiresAt), abs: formatDateTime(session.absoluteExpiresAt) })}</span>
        </p>
      </div>

      <Notice notice={notice} onDismiss={clear} />

      <div className="grid gap-4 lg:grid-cols-2">
        <PasswordForm onMessage={onMessage} />
        <UsernameForm onMessage={onMessage} />
      </div>
      <SessionsCard onMessage={onMessage} />
      <TokensCard onMessage={onMessage} />
    </div>
  );
}
