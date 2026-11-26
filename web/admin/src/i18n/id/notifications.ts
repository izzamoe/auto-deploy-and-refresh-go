// 'notifications' messages (Indonesian): the notifications dialog of the header (loaded on demand). Importing this module registers them (side effect),
// so a component that uses these keys must import it: namespaces.test.ts checks that.
// Moved out of core (002 T072, FR-029): no route carries them in its initial JS.
import { registerEnLoader, registerMessages } from "../registry";

export const idNotifications = {
  "shell.notifications.empty": "Belum ada notifikasi pada sesi ini.",
  "shell.notifications.clear": "Hapus semua",
  "shell.notifications.dismiss": "Hapus notifikasi",
  "notification.deploySucceeded": "Deploy {tag} berhasil",
  "notification.deployFailed": "Deploy {tag} gagal",
  "notification.deployCanceled": "Deploy {tag} dibatalkan",
  "notification.passwordChangeRequired": "Ganti kata sandi bawaan sebelum melanjutkan.",
  "notification.noticeFallback": "Pemberitahuan sistem",
} as const;

registerMessages("id", idNotifications);
// English only when the English UI is needed (002 T072, FR-029).
registerEnLoader("notifications", () => import("../en/notifications").then((m) => m.enNotifications), idNotifications);
