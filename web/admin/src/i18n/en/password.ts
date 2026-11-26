// 'password' messages (English). Loaded only when the English UI is needed: ../id/password.ts
// registers this module's loader (002 T072, FR-029). Same keys as the id namespace (typed).
import type { idPassword } from "../id/password";

export const enPassword: Record<keyof typeof idPassword, string> = {
  "account.forced.title": "Change the default password",
  "account.forced.body": "For your security, change the password before using the admin panel.",
  "account.password.current": "Current password",
  "account.password.new": "New password",
  "account.password.confirm": "Repeat the new password",
  "account.password.mismatch": "The new password and its repetition do not match.",
  "account.password.submit": "Change password",
  "account.password.submitting": "Saving…",
};
