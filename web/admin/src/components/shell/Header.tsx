"use client";

import { useQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import type { FormEvent } from "react";
import { useI18n } from "@/i18n";
import { api } from "@/lib/api/client";
import { useConnection } from "@/lib/events/provider";
import { LOGO_SRC } from "@/lib/paths";
import { queryKeys } from "@/lib/query";
import { cn } from "@/lib/utils";
import { NotificationsPanel } from "./NotificationsPanel";
import { useSession } from "./SessionGate";

/** Greeting with live counts, app search and the notification bell (plan §E). */
export function Header() {
  const { t } = useI18n();
  const session = useSession();
  const router = useRouter();
  const connection = useConnection();
  const [q, setQ] = useState("");

  const stats = useQuery({
    queryKey: queryKeys.stats,
    queryFn: ({ signal }) => api.getStats({}, { signal }),
    refetchInterval: 30_000,
  });

  function search(e: FormEvent) {
    e.preventDefault();
    const term = q.trim();
    router.push(term ? `/apps/?q=${encodeURIComponent(term)}` : "/apps/");
  }

  const online = connection.status === "open";

  return (
    <header className="flex flex-wrap items-center gap-x-5 gap-y-3 pb-6">
      <img src={LOGO_SRC} alt={t("app.name")} width={44} height={44} />
      <div className="min-w-0 flex-1">
        <p className="truncate text-2xl font-semibold tracking-tight" data-testid="greeting">
          {t("shell.greeting", { name: session.username })}
        </p>
        <p className="text-sm text-muted-foreground" data-testid="subtitle">
          {stats.data
            ? t("shell.subtitle", { active: stats.data.apps.enabled, running: stats.data.active.running })
            : stats.isError
              ? t("shell.stats.error")
              : t("common.loading")}
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

      <span role="status" title={online ? t("shell.connection.online") : t("shell.connection.offline")} className="inline-flex items-center gap-2 text-xs font-medium text-muted-foreground">
        <span aria-hidden="true" className={cn("inline-flex size-2.5 rounded-full", online ? "bg-primary" : "bg-muted-foreground/60")} />
        <span className="sr-only xl:not-sr-only">{online ? t("shell.connection.online") : t("shell.connection.offline")}</span>
      </span>
      <NotificationsPanel />
    </header>
  );
}
