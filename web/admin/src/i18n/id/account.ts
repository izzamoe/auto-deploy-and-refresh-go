// 'account' messages (Indonesian). Importing this module registers them (side effect), so a
// component that uses these keys must import it: namespaces.test.ts checks that.
import { registerEnLoader, registerMessages } from "../registry";

export const idAccount = {
  "account.page.title": "Akun",
  "account.page.subtitle": "Kata sandi, nama pengguna, sesi, dan token API.",
  "account.profile.signedInAs": "Masuk sebagai {name}",
  "account.profile.session": "Sesi berlaku hingga {time}; batas maksimum {abs}.",
  "account.password.title": "Ganti kata sandi",
  "account.password.done": "Kata sandi diganti.",
  "account.username.title": "Ganti nama pengguna",
  "account.username.current": "Kata sandi saat ini",
  "account.username.new": "Nama pengguna baru",
  "account.username.submit": "Ganti nama pengguna",
  "account.username.done": "Nama pengguna diganti.",
  "account.sessions.title": "Sesi",
  "account.sessions.body": "Cabut semua sesi lain (perangkat atau browser lain). Sesi ini tetap aktif.",
  "account.sessions.revoke": "Cabut sesi lain",
  "account.sessions.confirmTitle": "Cabut semua sesi lain?",
  "account.sessions.confirmBody": "Semua perangkat dan browser lain harus masuk lagi.",
  "account.sessions.confirm": "Ya, cabut",
  "account.sessions.done": "Sesi lain dicabut.",
  "account.tokens.title": "Token API",
  "account.tokens.body": "Token API memungkinkan skrip memakai API admin tanpa sesi browser. Nilai penuh hanya ditampilkan sekali; setelah itu hanya awalannya.",
  "account.tokens.name": "Nama token",
  "account.tokens.create": "Buat token",
  "account.tokens.created": "Token dibuat.",
  "account.tokens.once": "Salin token ini sekarang; nilai penuhnya tidak akan ditampilkan lagi.",
  "account.tokens.value": "Token baru",
  "account.tokens.dismiss": "Saya sudah menyalinnya",
  "account.tokens.empty": "Belum ada token API.",
  "account.tokens.list": "Daftar token API",
  "account.tokens.col.name": "Nama",
  "account.tokens.col.prefix": "Awalan",
  "account.tokens.col.created": "Dibuat",
  "account.tokens.col.lastUsed": "Terakhir dipakai",
  "account.tokens.col.status": "Status",
  "account.tokens.col.actions": "Aksi",
  "account.tokens.never": "Belum pernah",
  "account.tokens.active": "Aktif",
  "account.tokens.revokedAt": "Dicabut {time}",
  "account.tokens.revoke": "Cabut",
  "account.tokens.revokeFor": "Cabut token {name}",
  "account.tokens.revokeTitle": "Cabut token {name}?",
  "account.tokens.revokeBody": "Skrip yang memakai token ini akan berhenti bekerja.",
  "account.tokens.revokeConfirm": "Ya, cabut",
  "account.tokens.revokedDone": "Token dicabut.",
  "account.tokens.loadError": "Daftar token tidak dapat dimuat.",
} as const;

registerMessages("id", idAccount);
// English only when the English UI is needed (002 T072, FR-029).
registerEnLoader("account", () => import("../en/account").then((m) => m.enAccount), idAccount);
