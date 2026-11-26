"use client";

// Below 768 px: the "More" dialog of the bottom bar, loaded on demand (T070, FR-027).
import { LogOut, Moon, Sun } from "lucide-react";
import Link from "next/link";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";
import type { RefObject } from "react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import { LocaleToggle } from "./LocaleToggle";
import { NAV_ITEMS, ROW } from "./nav";

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  pathname: string;
  onLogout: () => void;
  logoutPending: boolean;
  logoutFailed: boolean;
  /** The "More" button: focus returns to it when the dialog closes. */
  trigger: RefObject<HTMLButtonElement | null>;
}

export function MoreMenuDialog({ open, onOpenChange, pathname, onLogout, logoutPending, logoutFailed, trigger }: Props) {
  const { t } = useI18n();
  const { resolvedTheme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);
  const dark = mounted && resolvedTheme === "dark";
  const secondary = NAV_ITEMS.filter((i) => !i.primary);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-w-sm"
        onCloseAutoFocus={(e) => {
          if (trigger.current?.isConnected) {
            e.preventDefault();
            trigger.current.focus();
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>{t("shell.more.title")}</DialogTitle>
          <DialogDescription className="sr-only">{t("shell.more.title")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-1 rounded-3xl bg-rail p-2" data-testid="more-menu">
          {secondary.map(({ href, label, icon: Icon, match }) => (
            <Link
              key={href}
              href={href}
              onClick={() => onOpenChange(false)}
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
          <div className="px-4 py-1">
            <LocaleToggle touch />
          </div>
          <button type="button" onClick={onLogout} disabled={logoutPending} className={ROW}>
            <LogOut className="size-5" aria-hidden="true" />
            {t("shell.logout")}
          </button>
        </div>
        {logoutFailed && (
          <p role="alert" className="rounded-xl bg-danger-soft px-3 py-2 text-xs text-danger-soft-foreground">
            {t("shell.logout.failed")}
          </p>
        )}
      </DialogContent>
    </Dialog>
  );
}
