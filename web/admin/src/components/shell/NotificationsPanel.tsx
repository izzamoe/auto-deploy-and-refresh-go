"use client";

import { Bell, X } from "lucide-react";
import { useState } from "react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { useI18n } from "@/i18n";
import { useEventStore, useNotifications } from "@/lib/events/provider";
import type { AppNotification } from "@/lib/events/store";
import { cn } from "@/lib/utils";

const LEVEL_STYLE: Record<AppNotification["level"], string> = {
  success: "bg-success-soft text-success-soft-foreground",
  error: "bg-danger-soft text-danger-soft-foreground",
  warning: "bg-warning-soft text-warning-soft-foreground",
  info: "bg-info-soft text-info-soft-foreground",
};

function useMessage() {
  const { t } = useI18n();
  return (n: AppNotification): string => {
    switch (n.kind) {
      case "job_succeeded":
        return t("notification.deploySucceeded", { tag: n.tag ?? "" });
      case "job_failed":
        return t("notification.deployFailed", { tag: n.tag ?? "" });
      case "job_canceled":
        return t("notification.deployCanceled", { tag: n.tag ?? "" });
      case "password_change_required":
        return t("notification.passwordChangeRequired");
      case "events_stale":
        return t("notification.eventsStale");
      case "notice":
        return n.message ?? t("notification.noticeFallback");
    }
  };
}

/** The bell of the header: this session's job results and notices (not persisted). */
export function NotificationsPanel() {
  const { t, formatDateTime } = useI18n();
  const store = useEventStore();
  const notifications = useNotifications();
  const message = useMessage();
  const [open, setOpen] = useState(false);
  const count = notifications.length;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button
          type="button"
          aria-label={count > 0 ? `${t("shell.notifications")}: ${t("shell.notifications.count", { count })}` : t("shell.notifications")}
          className="relative inline-flex size-12 items-center justify-center rounded-full bg-card text-foreground shadow-card hover:bg-muted"
        >
          <Bell className="size-5" aria-hidden="true" />
          {count > 0 && (
            <span aria-hidden="true" className="absolute -right-0.5 -top-0.5 flex min-w-5 items-center justify-center rounded-full bg-destructive px-1 text-xs font-semibold text-destructive-foreground">
              {count > 9 ? "9+" : count}
            </span>
          )}
        </button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("shell.notifications")}</DialogTitle>
          <DialogDescription className="sr-only">{t("shell.notifications")}</DialogDescription>
        </DialogHeader>
        {count === 0 ? (
          <p className="text-sm text-muted-foreground">{t("shell.notifications.empty")}</p>
        ) : (
          <>
            <ul className="max-h-96 space-y-2 overflow-auto" data-testid="notification-list">
              {notifications.map((n) => (
                <li key={n.id} className={cn("flex items-start justify-between gap-3 rounded-xl px-3 py-2 text-sm", LEVEL_STYLE[n.level])}>
                  <div>
                    <p className="font-medium">{message(n)}</p>
                    <p className="text-xs opacity-80">{formatDateTime(n.ts)}</p>
                  </div>
                  <button
                    type="button"
                    aria-label={t("shell.notifications.dismiss")}
                    onClick={() => store.dismissNotification(n.id)}
                    className="rounded-full p-1 hover:bg-black/10"
                  >
                    <X className="size-4" aria-hidden="true" />
                  </button>
                </li>
              ))}
            </ul>
            <button type="button" onClick={() => store.clearNotifications()} className="self-start text-sm font-medium text-primary-text underline">
              {t("shell.notifications.clear")}
            </button>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
