"use client";

import Link from "next/link";
import { useId } from "react";
import { useSession } from "@/components/shell/SessionGate";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";

/** Initials avatar (own SVG-free monogram, no photo), username and session expiry. */
export function AccountCard() {
  const { t, formatDateTime } = useI18n();
  const session = useSession();
  const id = useId();
  const initials = session.username.trim().slice(0, 2).toUpperCase() || "?";
  return (
    <section aria-labelledby={id} data-testid="account-card" className="rounded-card bg-card p-5 shadow-card">
      <h2 id={id} className="text-sm font-medium text-muted-foreground">
        {t("dashboard.account.title")}
      </h2>
      <div className="mt-3 flex items-center gap-3">
        <span aria-hidden="true" className="inline-flex size-12 shrink-0 items-center justify-center rounded-full bg-gradient-to-br from-gradient-from to-gradient-to text-lg font-semibold text-gradient-foreground">
          {initials}
        </span>
        <div className="min-w-0">
          <p className="truncate font-semibold" data-testid="account-username">
            {session.username}
          </p>
          <p className="text-sm text-muted-foreground">{t("dashboard.account.session", { time: formatDateTime(session.expiresAt) })}</p>
        </div>
      </div>
      <Button asChild variant="outline" className="mt-4 w-full">
        <Link href="/account/">{t("dashboard.account.manage")}</Link>
      </Button>
    </section>
  );
}
