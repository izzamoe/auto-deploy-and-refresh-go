"use client";

import { Bell } from "lucide-react";
import { useRef, useState } from "react";
import { lazyModule, useLazyModule } from "@/components/common/lazy";
import { useI18n } from "@/i18n";
import { useNotifications } from "@/lib/events/provider";

// T070 (FR-027): the dialog (Radix) is loaded on intent (pointerenter/focus) and opened on click.
const dialogModule = lazyModule(() => import("./NotificationsDialog"));
const preload = () => void dialogModule.load();

/** The bell of the header: this session's job results and notices (not persisted). */
export function NotificationsPanel() {
  const { t } = useI18n();
  const notifications = useNotifications();
  const [open, setOpen] = useState(false);
  const mod = useLazyModule(dialogModule, open, () => setOpen(false));
  const trigger = useRef<HTMLButtonElement>(null);
  const count = notifications.length;

  return (
    <>
      <button
        ref={trigger}
        type="button"
        aria-label={count > 0 ? `${t("shell.notifications")}: ${t("shell.notifications.count", { count })}` : t("shell.notifications")}
        aria-haspopup="dialog"
        aria-expanded={open}
        data-state={open ? "open" : "closed"}
        onPointerEnter={preload}
        onFocus={preload}
        onClick={() => setOpen(true)}
        className="relative inline-flex size-12 items-center justify-center rounded-full bg-card text-foreground shadow-card hover:bg-muted"
      >
        <Bell className="size-5" aria-hidden="true" />
        {count > 0 && (
          <span aria-hidden="true" className="absolute -right-0.5 -top-0.5 flex min-w-5 items-center justify-center rounded-full bg-destructive px-1 text-xs font-semibold text-destructive-foreground">
            {count > 9 ? "9+" : count}
          </span>
        )}
      </button>
      {mod && <mod.NotificationsDialog open={open} onOpenChange={setOpen} trigger={trigger} />}
    </>
  );
}
