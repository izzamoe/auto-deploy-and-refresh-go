// 'login' messages (Indonesian): the login page. Importing this module registers them (side effect),
// so a component that uses these keys must import it: namespaces.test.ts checks that.
// Moved out of core (002 T072, FR-029): no route carries them in its initial JS.
import { registerEnLoader, registerMessages } from "../registry";

export const idLogin = {
  "login.title": "Masuk ke auto-deploy",
  "login.username": "Nama pengguna",
  "login.password": "Kata sandi",
  "login.submit": "Masuk",
  "login.submitting": "Masuk…",
  "login.failed": "Nama pengguna atau kata sandi salah.",
  "login.expired": "Sesi berakhir. Silakan masuk lagi.",
  "login.rateLimited": "Terlalu banyak percobaan masuk. Coba lagi dalam {wait}.",
  "login.waitSeconds": "{n} detik",
  "login.waitMinutes": "{n} menit",
  "login.logo": "Logo auto-deploy",
} as const;

registerMessages("id", idLogin);
// English only when the English UI is needed (002 T072, FR-029).
registerEnLoader("login", () => import("../en/login").then((m) => m.enLogin), idLogin);
