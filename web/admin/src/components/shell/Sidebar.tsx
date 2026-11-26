"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Ellipsis, LogOut } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useRef, useState } from "react";
import { lazyModule, useLazyModule } from "@/components/common/lazy";
import { useI18n } from "@/i18n";
import { LOGIN_PATH, api } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { LocaleToggle } from "./LocaleToggle";
import { NAV_ITEMS } from "./nav";
import { RailTip } from "./RailTip";
import { ThemeToggle } from "./ThemeToggle";

export { NAV_ITEMS } from "./nav";
export type { NavItem } from "./nav";

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

// T070 (FR-027): the dialog (Radix) is loaded on intent (pointerenter/focus) and opened on click.
const moreModule = lazyModule(() => import("./MoreMenuDialog"));
const preloadMore = () => void moreModule.load();

/** Below 768 px: the sections that do not fit the bottom bar, theme, language and sign out. */
function MoreMenu({ pathname, logout }: { pathname: string; logout: ReturnType<typeof useLogout> }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const mod = useLazyModule(moreModule, open, () => setOpen(false));
  const trigger = useRef<HTMLButtonElement>(null);
  const active = NAV_ITEMS.some((i) => !i.primary && i.match(pathname));

  return (
    <>
      <button
        ref={trigger}
        type="button"
        aria-label={t("shell.more")}
        aria-expanded={open}
        aria-haspopup="dialog"
        data-state={open ? "open" : "closed"}
        onPointerEnter={preloadMore}
        onFocus={preloadMore}
        onClick={() => setOpen(true)}
        className={cn(
          "inline-flex size-11 shrink-0 items-center justify-center rounded-full focus-visible:outline-rail-foreground md:hidden",
          active || open ? "bg-rail-active text-rail-active-foreground" : "text-rail-muted hover:bg-white/10 hover:text-rail-foreground",
        )}
      >
        <Ellipsis className="size-5" aria-hidden="true" />
      </button>
      {mod && (
        <mod.MoreMenuDialog
          open={open}
          onOpenChange={setOpen}
          pathname={pathname}
          onLogout={() => logout.mutate()}
          logoutPending={logout.isPending}
          logoutFailed={logout.isError}
          trigger={trigger}
        />
      )}
    </>
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
