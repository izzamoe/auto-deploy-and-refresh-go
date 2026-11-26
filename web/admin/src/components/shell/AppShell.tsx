"use client";

import type { ReactNode } from "react";
import { useChunkFailed } from "@/components/common/lazy";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import { useConnection } from "@/lib/events/provider";
import { Header } from "./Header";
import { Sidebar } from "./Sidebar";

/** Frame of every authenticated page: skip link, sidebar, header, banner, main landmark. */
export function AppShell({ children }: { children: ReactNode }) {
  const { t } = useI18n();
  const connection = useConnection();
  // RV-22: a lazily loaded part could not be loaded (an old tab after an update): same banner.
  const chunkFailed = useChunkFailed();

  return (
    <div data-testid="admin-shell" className="min-h-dvh bg-background p-3 pb-24 md:p-6 md:pb-6">
      <a
        href="#main-content"
        className="sr-only rounded-full bg-nav px-4 py-2 text-nav-foreground focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50"
      >
        {t("nav.skip")}
      </a>
      <div className="mx-auto flex max-w-[1600px] gap-5">
        <Sidebar />
        <div className="min-w-0 flex-1">
          <Header />
          {(connection.status === "stale" || chunkFailed) && (
            <div role="alert" className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-2xl bg-warning-soft px-4 py-3 text-sm text-warning-soft-foreground">
              <span>{t("notification.eventsStale")}</span>
              <Button type="button" size="sm" onClick={() => window.location.reload()}>
                {t("shell.reload")}
              </Button>
            </div>
          )}
          <main id="main-content" tabIndex={-1} data-testid="admin-content" className="outline-none">
            {children}
          </main>
        </div>
      </div>
    </div>
  );
}
