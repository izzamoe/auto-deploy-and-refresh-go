// Sections of the admin (sidebar, bottom bar and the "More" dialog share them).
import { GitBranch, History, LayoutGrid, Rocket, ScrollText, Send, UserCog } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { MessageKey } from "@/i18n";

export interface NavItem {
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

/** A row of the "More" dialog. */
export const ROW =
  "inline-flex min-h-11 w-full items-center gap-3 rounded-full px-4 text-sm font-medium text-rail-foreground hover:bg-white/10 focus-visible:outline-rail-foreground disabled:opacity-50";
