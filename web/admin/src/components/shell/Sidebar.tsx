"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Ellipsis, GitBranch, History, LayoutGrid, LogOut, Moon, Rocket, ScrollText, Send, Sun, UserCog } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { useI18n } from "@/i18n";
import type { MessageKey } from "@/i18n";
import { LOGIN_PATH, api } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { LocaleToggle } from "./LocaleToggle";
import { RailTip } from "./RailTip";
import { ThemeToggle } from "./ThemeToggle";

interface NavItem {
  href: string;
  label: MessageKey;
  icon: LucideIcon;
  /** True when `path` (no trailing slash, no basePath) belongs to this item. */
  match: (path: string) => boolean;
  /** Shown in the bottom bar below 768 px; the others move into the "More" dialog there. */
  primary?: boolean;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { href: "/", label: "nav.dashboard", icon: LayoutGrid, match: (p) => p === "", primary: true },
  { href: "/apps/", label: "nav.apps", icon: Rocket, match: (p) => p.startsWith("/apps"), primary: true },
  { href: "/history/", label: "nav.history", icon: History, match: (p) => p.startsWith("/history"), primary: true },
  { href: "/settings/github/", label: "nav.github", icon: GitBranch, match: (p) => p.startsWith("/settings/github") },
  { href: "/settings/telegram/", label: "nav.telegram", icon: Send, match: (p) => p.startsWith("/settings/telegram") },
  { href: "/logs/", label: "nav.logs", icon: ScrollText, match: (p) => p.startsWith("/logs"), primary: true },
  { href: "/account/", label: "nav.account", icon: UserCog, match: (p) => p.startsWith("/account") },
];

function useLogout() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: () => api.logout(),
    onSuccess: () => {
      client.clear(); // nothing of this session may be shown to the next user
      window.location.assign(LOGIN_PATH);
    },
  });
}

const ROW =
  "inline-flex min-h-11 w-full items-center gap-3 rounded-full px-4 text-sm font-medium text-rail-foreground hover:bg-white/10 focus-visible:outline-rail-foreground disabled:opacity-50";

/** Below 768 px: the sections that do not fit the bottom bar, theme, language and sign out. */
function MoreMenu({ pathname, logout }: { pathname: string; logout: ReturnType<typeof useLogout> }) {
  const { t } = useI18n();
  const { resolvedTheme, setTheme } = useTheme();
  const [open, setOpen] = useState(false);
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);
  const dark = mounted && resolvedTheme === "dark";
  const secondary = NAV_ITEMS.filter((i) => !i.primary);
  const active = secondary.some((i) => i.match(pathname));

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button
          type="button"
          aria-label={t("shell.more")}
          className={cn(
            "inline-flex size-11 shrink-0 items-center justify-center rounded-full focus-visible:outline-rail-foreground md:hidden",
            active ? "bg-rail-active text-rail-active-foreground" : "text-rail-muted hover:bg-white/10 hover:text-rail-foreground",
          )}
        >
          <Ellipsis className="size-5" aria-hidden="true" />
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>{t("shell.more.title")}</DialogTitle>
          <DialogDescription className="sr-only">{t("shell.more.title")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-1 rounded-3xl bg-rail p-2" data-testid="more-menu">
          {secondary.map(({ href, label, icon: Icon, match }) => (
            <Link
              key={href}
              href={href}
              onClick={() => setOpen(false)}
              aria-current={match(pathname) ? "page" : undefined}
              className={cn(ROW, match(pathname) && "bg-rail-active text-rail-active-foreground hover:bg-rail-active")}
            >
              <Icon className="size-5" aria-hidden="true" />
              {t(label)}
            </Link>
          ))}
          <button type="button" onClick={() => setTheme(dark ? "light" : "dark")} className={ROW}>
            {dark ? <Sun className="size-5" aria-hidden="true" /> : <Moon className="size-5" aria-hidden="true" />}
            {t("shell.theme.toggle")}
          </button>
          <div className="px-2 py-1">
            <LocaleToggle />
          </div>
          <button type="button" onClick={() => logout.mutate()} disabled={logout.isPending} className={ROW}>
            <LogOut className="size-5" aria-hidden="true" />
            {t("shell.logout")}
          </button>
        </div>
        {logout.isError && (
          <p role="alert" className="rounded-xl bg-danger-soft px-3 py-2 text-xs text-danger-soft-foreground">
            {t("shell.logout.failed")}
          </p>
        )}
      </DialogContent>
    </Dialog>
  );
}

/** Vertical pill on md+, bottom bar below 768 px (plan §E "Responsif"). */
export function Sidebar() {
  const { t } = useI18n();
  const pathname = (usePathname() ?? "/").replace(/\/+$/, "");
  const logout = useLogout();

  return (
    <aside
      data-testid="admin-sidebar"
      className="fixed inset-x-3 bottom-3 z-30 md:sticky md:inset-auto md:top-6 md:h-[calc(100dvh-3rem)] md:w-[4.75rem] md:shrink-0"
    >
      <nav
        aria-label={t("nav.label")}
        className="flex items-center justify-around gap-1 overflow-x-auto rounded-pill bg-rail p-2 shadow-card md:h-full md:flex-col md:justify-start md:overflow-y-auto md:overflow-x-hidden md:py-4 md:[scrollbar-width:none]"
      >
        {NAV_ITEMS.map(({ href, label, icon: Icon, match, primary }) => {
          const active = match(pathname);
          return (
            <RailTip key={href} label={t(label)} className={primary ? "inline-flex" : "hidden md:inline-flex"}>
              <Link
                href={href}
                aria-label={t(label)}
                aria-current={active ? "page" : undefined}
                className={cn(
                  "inline-flex size-11 shrink-0 items-center justify-center rounded-full focus-visible:outline-rail-foreground",
                  active ? "bg-rail-active text-rail-active-foreground" : "text-rail-muted hover:bg-white/10 hover:text-rail-foreground",
                )}
              >
                <Icon className="size-5" aria-hidden="true" />
              </Link>
            </RailTip>
          );
        })}

        <MoreMenu pathname={pathname} logout={logout} />

        <div className="ml-auto hidden items-center gap-1 md:ml-0 md:mt-auto md:flex md:flex-col">
          <ThemeToggle />
          <LocaleToggle className="md:flex-col" />
          <RailTip label={t("shell.logout")}>
            <button
              type="button"
              onClick={() => logout.mutate()}
              disabled={logout.isPending}
              aria-label={t("shell.logout")}
              className="inline-flex size-11 shrink-0 items-center justify-center rounded-full text-rail-muted hover:bg-white/10 hover:text-rail-foreground focus-visible:outline-rail-foreground disabled:opacity-50"
            >
              <LogOut className="size-5" aria-hidden="true" />
            </button>
          </RailTip>
        </div>
      </nav>
      {logout.isError && (
        <p role="alert" className="mt-2 hidden rounded-xl bg-danger-soft px-3 py-2 text-xs text-danger-soft-foreground md:absolute md:block md:left-full md:ml-3 md:w-56">
          {t("shell.logout.failed")}
        </p>
      )}
    </aside>
  );
}
