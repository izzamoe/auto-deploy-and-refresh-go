"use client";

import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ForcedPasswordChange } from "@/components/shell/ForcedPasswordChange";
import { useI18n } from "@/i18n";
import { ApiError, api, describeError, sanitizeNext } from "@/lib/api/client";
import { LOGO_SRC } from "@/lib/paths";

/** "45 detik" / "2 menit": the wait shown for a 429 (rounded up, so it is never too short). */
export function formatWait(seconds: number, t: ReturnType<typeof useI18n>["t"]): string {
  if (seconds < 60) return t("login.waitSeconds", { n: seconds });
  return t("login.waitMinutes", { n: Math.ceil(seconds / 60) });
}

/**
 * Login (FR-015, FR-008): a real form, no Basic-auth popup. `next` is only honoured when it
 * is an internal path under /admin/ (sanitizeNext); a 429 shows the wait from Retry-After
 * and keeps the button disabled while it counts down; a session that still carries the
 * initial password goes straight into the forced change.
 */
export function LoginForm({ next }: { next: string | null }) {
  const i18n = useI18n();
  const { t } = i18n;
  const target = sanitizeNext(next);
  const redirected = next !== null && next !== "";

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [wait, setWait] = useState(0); // seconds left of a 429 lockout
  const [forced, setForced] = useState(false);

  useEffect(() => {
    if (wait <= 0) return;
    const timer = window.setInterval(() => setWait((w) => Math.max(0, w - 1)), 1000);
    return () => window.clearInterval(timer);
  }, [wait > 0]); // eslint-disable-line react-hooks/exhaustive-deps -- (re)start only when the lockout starts/ends

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy || wait > 0) return;
    setError("");
    setBusy(true);
    try {
      const session = await api.login({ username, password });
      if (session.mustChangePassword) {
        setForced(true);
        return;
      }
      window.location.assign(target);
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) {
        setWait(err.retryAfterSeconds ?? 0);
        setError(err.retryAfterSeconds ? "" : describeError(err, i18n));
      } else if (err instanceof ApiError && err.status === 401) {
        setError(t("login.failed"));
      } else {
        setError(describeError(err, i18n));
      }
      setPassword("");
    } finally {
      setBusy(false);
    }
  }

  if (forced) return <ForcedPasswordChange onDone={() => window.location.assign(target)} />;

  return (
    <div className="grid min-h-dvh place-items-center bg-background p-4">
      <main data-testid="login" className="w-full max-w-sm rounded-card bg-card p-8 shadow-card">
        <div className="mb-6 flex items-center gap-3">
          {/* eslint-disable-next-line @next/next/no-img-element -- static export, unoptimized images */}
          <img src={LOGO_SRC} alt="" width={40} height={40} className="size-10" />
          <div>
            <p className="text-sm text-muted-foreground">{t("app.name")}</p>
            <h1 className="text-balance text-xl font-semibold">{t("login.title")}</h1>
          </div>
        </div>

        {redirected && !error && wait === 0 && (
          <p role="status" className="mb-4 rounded-xl bg-info-soft px-3 py-2 text-sm text-info-soft-foreground">
            {t("login.expired")}
          </p>
        )}

        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="login-username">{t("login.username")}</Label>
            <Input
              id="login-username"
              name="username"
              type="text"
              autoComplete="username"
              autoCapitalize="none"
              spellCheck={false}
              required
              autoFocus
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="login-password">{t("login.password")}</Label>
            <Input
              id="login-password"
              name="password"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>

          <div aria-live="assertive" role="alert" className="empty:hidden">
            {wait > 0 ? (
              <p className="rounded-xl bg-warning-soft px-3 py-2 text-sm text-warning-soft-foreground">
                {t("login.rateLimited", { wait: formatWait(wait, t) })}
              </p>
            ) : error ? (
              <p className="rounded-xl bg-danger-soft px-3 py-2 text-sm text-danger-soft-foreground">{error}</p>
            ) : null}
          </div>

          <Button type="submit" size="lg" className="w-full" disabled={busy || wait > 0}>
            {busy ? t("login.submitting") : t("login.submit")}
          </Button>
        </form>
      </main>
    </div>
  );
}
