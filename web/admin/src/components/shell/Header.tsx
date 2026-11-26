"use client";

import { Search } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import type { FormEvent } from "react";
import { useI18n } from "@/i18n";
import { useConnection, useSummary, useTopic } from "@/lib/events/provider";
import { indicatorOf } from "@/lib/events/store";
import type { ConnectionIndicator } from "@/lib/events/store";
import { LOGO_SRC } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { NotificationsPanel } from "./NotificationsPanel";
import { useSession } from "./SessionGate";

const CONNECTION_LABEL = {
  live: "shell.connection.online",
  reconnecting: "shell.connection.reconnecting",
  resyncing: "shell.connection.resyncing",
  offline: "shell.connection.offline",
} as const satisfies Record<ConnectionIndicator, string>;

const CONNECTION_DOT: Record<ConnectionIndicator, string> = {
  live: "bg-primary",
  reconnecting: "bg-info-soft-foreground motion-safe:animate-pulse",
  resyncing: "bg-info-soft-foreground",
  offline: "bg-muted-foreground/60",
};

/**
 * Greeting with live counts, app search and the notification bell (plan §E). The counts come
 * from topic `summary` (state in the subscribed ack, then pushed): no /stats request, no
 * polling (002 FR-001, FR-002).
 */
export function Header() {
  const { t } = useI18n();
  const session = useSession();
  const router = useRouter();
  const connection = useConnection();
  const [q, setQ] = useState("");

  useTopic("summary");
  const summary = useSummary();

  function search(e: FormEvent) {
    e.preventDefault();
    const term = q.trim();
    router.push(term ? `/apps/?q=${encodeURIComponent(term)}` : "/apps/");
  }

  // FR-024: four states, as text (not colour alone) and as `data-connection` (Playwright).
  const indicator = indicatorOf(connection.status);
  const label = t(CONNECTION_LABEL[indicator]);

  return (
    <header className="flex flex-wrap items-center gap-x-5 gap-y-3 pb-6">
      <img src={LOGO_SRC} alt={t("app.name")} width={44} height={44} />
      <div className="min-w-0 flex-[1_1_12rem]">
        <p className="truncate text-2xl font-semibold tracking-tight" data-testid="greeting">
          {t("shell.greeting", { name: session.username })}
        </p>
        <p className="text-sm text-muted-foreground" data-testid="subtitle">
          {summary ? t("shell.subtitle", { active: summary.appsEnabled, running: summary.running }) : t("shell.summary.pending")}
        </p>
      </div>

      <form role="search" onSubmit={search} className="flex min-w-48 flex-1 items-center rounded-pill bg-card py-1.5 pl-5 pr-1.5 shadow-card md:max-w-sm">
        <input
          type="search"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          aria-label={t("shell.search.label")}
          placeholder={t("shell.search.placeholder")}
          className="min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
        />
        <button
          type="submit"
          aria-label={t("shell.search.submit")}
          className="inline-flex size-10 items-center justify-center rounded-full bg-primary text-primary-foreground"
        >
          <Search className="size-4" aria-hidden="true" />
        </button>
      </form>

      <span role="status" title={label} data-connection={indicator} data-testid="connection-status" className="inline-flex items-center gap-2 text-xs font-medium text-muted-foreground">
        <span aria-hidden="true" className={cn("inline-flex size-2.5 rounded-full", CONNECTION_DOT[indicator])} />
        <span className="sr-only xl:not-sr-only">{label}</span>
      </span>
      <NotificationsPanel />
    </header>
  );
}
