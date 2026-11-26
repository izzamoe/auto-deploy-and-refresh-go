// 'login' messages (English). Loaded only when the English UI is needed: ../id/login.ts
// registers this module's loader (002 T072, FR-029). Same keys as the id namespace (typed).
import type { idLogin } from "../id/login";

export const enLogin: Record<keyof typeof idLogin, string> = {
  "login.title": "Sign in to auto-deploy",
  "login.username": "Username",
  "login.password": "Password",
  "login.submit": "Sign in",
  "login.submitting": "Signing in…",
  "login.failed": "Wrong username or password.",
  "login.expired": "Your session ended. Please sign in again.",
  "login.rateLimited": "Too many sign-in attempts. Try again in {wait}.",
  "login.waitSeconds": "{n} seconds",
  "login.waitMinutes": "{n} minutes",
  "login.logo": "auto-deploy logo",
};
