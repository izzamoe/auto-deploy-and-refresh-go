"use client";

// The notifications dialog of the header bell, loaded on demand (T070, FR-027).
import { X } from "lucide-react";
import type { RefObject } from "react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
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

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The bell: focus returns to it when the dialog closes. */
  trigger: RefObject<HTMLButtonElement | null>;
}

export function NotificationsDialog({ open, onOpenChange, trigger }: Props) {
  const { t, formatDateTime } = useI18n();
  const store = useEventStore();
  const notifications = useNotifications();
  const message = useMessage();
  const count = notifications.length;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        onCloseAutoFocus={(e) => {
          if (trigger.current?.isConnected) {
            e.preventDefault();
            trigger.current.focus();
          }
        }}
      >
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
