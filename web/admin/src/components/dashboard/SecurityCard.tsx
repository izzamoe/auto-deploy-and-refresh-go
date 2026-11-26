"use client";

import "@/i18n/id/dashboard";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, HelpCircle } from "lucide-react";
import Link from "next/link";
import { useId } from "react";
import { useSession } from "@/components/shell/SessionGate";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import { api } from "@/lib/api/client";
import { useTopic } from "@/lib/events/provider";
import { liveQueryOptions, queryKeys } from "@/lib/query";
import { cn } from "@/lib/utils";
import { CARD_TITLE } from "@/components/common/cardStyles";

type State = "ok" | "warn" | "unknown";

function Row({ state, text }: { state: State; text: string }) {
  const { t } = useI18n();
  const Icon = state === "ok" ? CheckCircle2 : state === "warn" ? AlertTriangle : HelpCircle;
  const label = state === "ok" ? t("dashboard.security.ok") : state === "warn" ? t("dashboard.security.warn") : t("dashboard.security.unknown");
  return (
    <li className="flex items-start gap-3" data-state={state}>
      <Icon aria-hidden className={cn("mt-0.5 size-5 shrink-0", state === "ok" ? "text-primary-text" : state === "warn" ? "text-warning-soft-foreground" : "text-muted-foreground")} />
      <span className="text-sm">
        {text}
        <span className="sr-only"> — {label}</span>
      </span>
    </li>
  );
}

/** Security checklist built only from real signals (session, token list, GitHub config, scheme). */
export function SecurityCard() {
  const { t } = useI18n();
  const id = useId();
  const session = useSession();
  // Kept current by topics account (tokens) and settings (GitHub): no refetch on focus while live.
  const tokens = useQuery({ queryKey: queryKeys.tokens, queryFn: ({ signal }) => api.listTokens({ signal }), ...liveQueryOptions(useTopic("account")) });
  const github = useQuery({ queryKey: queryKeys.github, queryFn: ({ signal }) => api.getGitHub({ signal }), ...liveQueryOptions(useTopic("settings")) });

  const activeTokens = tokens.data ? tokens.data.tokens.filter((tok) => !tok.revokedAt).length : undefined;
  const https = typeof window !== "undefined" && window.location.protocol === "https:";

  return (
    <section aria-labelledby={id} data-testid="security-card" className="flex flex-col rounded-card bg-card p-5 shadow-card">
      <h2 id={id} className={CARD_TITLE}>
        {t("dashboard.security.title")}
      </h2>
      <ul className="mb-4 mt-3 space-y-3">
        <Row state={session.mustChangePassword ? "warn" : "ok"} text={t("dashboard.security.password")} />
        <Row
          state={activeTokens === undefined ? "unknown" : "ok"}
          text={activeTokens === undefined ? t("dashboard.security.unknown") : activeTokens > 0 ? t("dashboard.security.tokens", { n: activeTokens }) : t("dashboard.security.tokensNone")}
        />
        <Row
          state={github.data ? (github.data.config.hasToken ? "ok" : "warn") : "unknown"}
          text={github.data ? (github.data.config.hasToken ? t("dashboard.security.github") : t("dashboard.security.githubNone")) : t("dashboard.security.unknown")}
        />
        <Row state={https ? "ok" : "warn"} text={https ? t("dashboard.security.cookie") : t("dashboard.security.cookieNo")} />
      </ul>
      <Button asChild variant="outline" className="mt-auto w-full">
        <Link href="/account/">{t("dashboard.security.manage")}</Link>
      </Button>
    </section>
  );
}
