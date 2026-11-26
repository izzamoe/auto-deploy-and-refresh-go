// 'history' messages (Indonesian). Importing this module registers them (side effect), so a
// component that uses these keys must import it: namespaces.test.ts checks that.
import { registerEnLoader, registerMessages } from "../registry";

export const idHistory = {
  "history.title": "Riwayat",
  "history.subtitle": "Tinjau percobaan deploy, status langsung, dan aksi pemulihan.",
  "history.filteredBy": "Menampilkan riwayat {app}",
  "history.showAll": "Tampilkan semua aplikasi",
  "history.empty.title": "Belum ada riwayat deploy",
  "history.empty.body": "Deploy akan muncul di sini setelah webhook atau deploy manual diantrekan.",
  "history.loadError": "Riwayat tidak dapat dimuat.",
  "history.notFound": "Aplikasi untuk riwayat ini tidak ditemukan.",
  "history.table": "Riwayat deploy",
  "history.col.app": "Aplikasi",
  "history.col.tag": "Tag",
  "history.col.status": "Status",
  "history.col.trigger": "Pemicu",
  "history.col.error": "Galat",
  "history.col.created": "Dibuat",
  "history.col.updated": "Diperbarui",
  "history.col.actions": "Aksi",
  "history.retry": "Ulangi deploy",
  "history.retryFor": "Ulangi deploy {tag}",
  "history.logs": "Lihat log",
  "history.logsFor": "Lihat log deploy {tag}",
  "history.cancel": "Batalkan",
  "history.cancelFor": "Batalkan deploy {tag}",
  "history.retryQueued": "Deploy ulang diantrekan.",
  "history.cancelRequested": "Pembatalan diminta.",
  "history.logs.title": "Log deploy {tag}",
  "history.logs.desc": "Log service yang disimpan saat deploy ini selesai.",
  "history.page": "Halaman {page} dari {pages} · {total} deploy",
  "history.page.one": "Halaman {page} dari {pages} · {total} deploy",
  "history.prev": "Sebelumnya",
  "history.next": "Berikutnya",
  "history.pagination": "Halaman riwayat",
  "history.trigger.webhook": "Webhook",
  "history.trigger.manual": "Manual",
  "history.trigger.manual_retry": "Ulang manual",
  "history.rolledBack": "Dikembalikan",
} as const;

registerMessages("id", idHistory);
// English only when the English UI is needed (002 T072, FR-029).
registerEnLoader("history", () => import("../en/history").then((m) => m.enHistory), idHistory);
