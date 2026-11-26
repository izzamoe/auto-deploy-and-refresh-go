// 'notifications' messages (English). Loaded only when the English UI is needed: ../id/notifications.ts
// registers this module's loader (002 T072, FR-029). Same keys as the id namespace (typed).
import type { idNotifications } from "../id/notifications";

export const enNotifications: Record<keyof typeof idNotifications, string> = {
  "shell.notifications.empty": "No notifications in this session yet.",
  "shell.notifications.clear": "Clear all",
  "shell.notifications.dismiss": "Dismiss notification",
  "notification.deploySucceeded": "Deploy {tag} succeeded",
  "notification.deployFailed": "Deploy {tag} failed",
  "notification.deployCanceled": "Deploy {tag} was canceled",
  "notification.passwordChangeRequired": "Change the default password before continuing.",
  "notification.noticeFallback": "System notice",
};
