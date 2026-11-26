// 'password' messages (Indonesian): the password forms (forced change, loaded on demand; account page). Importing this module registers them (side effect),
// so a component that uses these keys must import it: namespaces.test.ts checks that.
// Moved out of core (002 T072, FR-029): no route carries them in its initial JS.
import { registerEnLoader, registerMessages } from "../registry";

export const idPassword = {
  "account.forced.title": "Ganti kata sandi bawaan",
  "account.forced.body": "Demi keamanan, ganti kata sandi terlebih dahulu sebelum memakai panel admin.",
  "account.password.current": "Kata sandi saat ini",
  "account.password.new": "Kata sandi baru",
  "account.password.confirm": "Ulangi kata sandi baru",
  "account.password.mismatch": "Kata sandi baru dan pengulangannya tidak sama.",
  "account.password.submit": "Ganti kata sandi",
  "account.password.submitting": "Menyimpan…",
} as const;

registerMessages("id", idPassword);
// English only when the English UI is needed (002 T072, FR-029).
registerEnLoader("password", () => import("../en/password").then((m) => m.enPassword), idPassword);
