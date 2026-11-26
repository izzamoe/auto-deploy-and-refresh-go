"use client";

import { QueryClientProvider } from "@tanstack/react-query";
import { ThemeProvider } from "next-themes";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { I18nProvider, useI18n } from "@/i18n";
import { createQueryClient } from "@/lib/query";
import { AppShell } from "./AppShell";
import { SessionGate } from "./SessionGate";

/**
 * Old bookmarks (`/admin/#/apps/x/edit`) keep working (LegacyHashRedirect). The mapper is
 * loaded only when such a hash is present, so no route carries it in its initial JS (T072).
 */
function useLegacyHashRedirect() {
  useEffect(() => {
    if (!window.location.hash.startsWith("#/")) return;
    void import("./LegacyHashRedirect")
      .then(({ mapLegacyHash }) => {
        const target = mapLegacyHash(window.location.hash);
        if (target) window.location.replace(target);
      })
      .catch(() => undefined); // chunk gone (old tab): the plain page is shown
  }, []);
}

// The login page is public and has no shell; every other page sits behind the session gate.
export function isBarePath(pathname: string | null): boolean {
  return pathname === "/login" || (pathname ?? "").startsWith("/login/");
}

function AppFrame({ children }: { children: ReactNode }) {
  const { ready } = useI18n();
  const pathname = usePathname();
  useLegacyHashRedirect();
  // Render after the stored locale was read, so the wrong language never blinks up first.
  return !ready ? null : isBarePath(pathname) ? children : (
    <SessionGate>
      <AppShell>{children}</AppShell>
    </SessionGate>
  );
}

export function Providers({ children }: { children: ReactNode }) {
  const [client] = useState(createQueryClient);
  return (
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
      <I18nProvider>
        <QueryClientProvider client={client}>
          <AppFrame>{children}</AppFrame>
        </QueryClientProvider>
      </I18nProvider>
    </ThemeProvider>
  );
}
