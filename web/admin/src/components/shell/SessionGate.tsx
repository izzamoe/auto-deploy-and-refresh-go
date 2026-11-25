"use client";

// FR-071 / B50 / K2: nothing that talks to the data API (and no event socket) is mounted
// until GET /session answered. While it is pending the user sees a status line, on a 401
// the API client already redirects to the login page, on any other failure there is a retry,
// and a forced password change replaces the whole UI.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import { ApiError, api, describeError, onAuthEvent } from "@/lib/api/client";
import type { Schemas } from "@/lib/api/client";
import { AdminEventProvider } from "@/lib/events/provider";
import { queryKeys } from "@/lib/query";
import { ForcedPasswordChange } from "./ForcedPasswordChange";

export type Session = Schemas["Session"];

const SessionContext = createContext<Session | null>(null);

/** The signed-in session. Only valid under <SessionGate>. */
export function useSession(): Session {
  const s = useContext(SessionContext);
  if (!s) throw new Error("useSession must be used inside <SessionGate>");
  return s;
}

function Screen({ children }: { children: ReactNode }) {
  return (
    <main className="flex min-h-dvh items-center justify-center p-6 text-center text-muted-foreground" data-testid="session-gate-screen">
      <div role="status" aria-live="polite" className="space-y-3">
        {children}
      </div>
    </main>
  );
}

export function SessionGate({ children }: { children: ReactNode }) {
  const i18n = useI18n();
  const { t } = i18n;
  const client = useQueryClient();
  const [forced, setForced] = useState(false);

  const session = useQuery({
    queryKey: queryKeys.session,
    queryFn: ({ signal }) => api.getSession({ signal }),
  });

  // A 403 password_change_required from any data call also lands here.
  useEffect(() => onAuthEvent("password_change_required", () => setForced(true)), []);

  if (session.isPending) return <Screen>{t("shell.session.checking")}</Screen>;

  if (session.isError) {
    if (session.error instanceof ApiError && session.error.status === 401) {
      return <Screen>{t("shell.session.redirecting")}</Screen>; // the client is navigating to /login/
    }
    return (
      <Screen>
        <p>{t("shell.session.error")}</p>
        <p className="text-sm">{describeError(session.error, i18n)}</p>
        <Button type="button" onClick={() => void session.refetch()}>
          {t("common.retry")}
        </Button>
      </Screen>
    );
  }

  if (session.data.mustChangePassword || forced) {
    return (
      <ForcedPasswordChange
        onDone={() => {
          setForced(false);
          void client.invalidateQueries({ queryKey: queryKeys.session });
        }}
      />
    );
  }

  return (
    <SessionContext.Provider value={session.data}>
      <AdminEventProvider>{children}</AdminEventProvider>
    </SessionContext.Provider>
  );
}
