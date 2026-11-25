"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { GitBranch, History, LayoutGrid, LogOut, Rocket, ScrollText, Send, UserCog } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useI18n } from "@/i18n";
import type { MessageKey } from "@/i18n";
import { LOGIN_PATH, api } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { LocaleToggle } from "./LocaleToggle";
import { ThemeToggle } from "./ThemeToggle";

interface NavItem {
  href: string;
  label: MessageKey;
  icon: LucideIcon;
  /** True when `path` (no trailing slash, no basePath) belongs to this item. */
  match: (path: string) => boolean;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { href: "/", label: "nav.dashboard", icon: LayoutGrid, match: (p) => p === "" },
  { href: "/apps/", label: "nav.apps", icon: Rocket, match: (p) => p.startsWith("/apps") },
  { href: "/history/", label: "nav.history", icon: History, match: (p) => p.startsWith("/history") },
  { href: "/settings/github/", label: "nav.github", icon: GitBranch, match: (p) => p.startsWith("/settings/github") },
  { href: "/settings/telegram/", label: "nav.telegram", icon: Send, match: (p) => p.startsWith("/settings/telegram") },
  { href: "/logs/", label: "nav.logs", icon: ScrollText, match: (p) => p.startsWith("/logs") },
  { href: "/account/", label: "nav.account", icon: UserCog, match: (p) => p.startsWith("/account") },
];

/** Vertical pill on md+, bottom bar below 768 px (plan §E "Responsif"). */
export function Sidebar() {
  const { t } = useI18n();
  const pathname = (usePathname() ?? "/").replace(/\/+$/, "");
  const client = useQueryClient();

  const logout = useMutation({
    mutationFn: () => api.logout(),
    onSuccess: () => {
      client.clear(); // nothing of this session may be shown to the next user
      window.location.assign(LOGIN_PATH);
    },
  });

  return (
    <aside
      data-testid="admin-sidebar"
      className="fixed inset-x-3 bottom-3 z-30 md:sticky md:inset-auto md:top-6 md:h-[calc(100dvh-3rem)] md:w-[4.75rem] md:shrink-0"
    >
      <nav
        aria-label={t("nav.label")}
        className="flex items-center gap-1 overflow-x-auto rounded-pill bg-card p-2 shadow-card md:h-full md:flex-col md:overflow-visible md:py-4"
      >
        {NAV_ITEMS.map(({ href, label, icon: Icon, match }) => {
          const active = match(pathname);
          return (
            <Link
              key={href}
              href={href}
              aria-label={t(label)}
              title={t(label)}
              aria-current={active ? "page" : undefined}
              className={cn(
                "inline-flex size-11 shrink-0 items-center justify-center rounded-full",
                active ? "bg-nav text-nav-foreground" : "text-muted-foreground hover:bg-muted hover:text-foreground",
              )}
            >
              <Icon className="size-5" aria-hidden="true" />
            </Link>
          );
        })}

        <div className="ml-auto flex items-center gap-1 md:ml-0 md:mt-auto md:flex-col">
          <ThemeToggle />
          <LocaleToggle className="md:flex-col" />
          <button
            type="button"
            onClick={() => logout.mutate()}
            disabled={logout.isPending}
            aria-label={t("shell.logout")}
            title={t("shell.logout")}
            className="inline-flex size-11 items-center justify-center rounded-full text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-50"
          >
            <LogOut className="size-5" aria-hidden="true" />
          </button>
        </div>
      </nav>
      {logout.isError && (
        <p role="alert" className="mt-2 rounded-xl bg-danger-soft px-3 py-2 text-xs text-danger-soft-foreground md:absolute md:left-full md:ml-3 md:w-56">
          {t("shell.logout.failed")}
        </p>
      )}
    </aside>
  );
}
