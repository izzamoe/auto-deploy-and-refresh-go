"use client";

import "@/i18n/id/dashboard";
import Link from "next/link";
import { useId } from "react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";

/** Own illustration (a stack of boxes), not a copy of any reference asset. */
function Boxes() {
  return (
    <svg aria-hidden="true" focusable="false" viewBox="0 0 96 64" className="mx-auto h-16 w-24">
      <rect x="6" y="36" width="38" height="24" rx="6" className="fill-accent" />
      <rect x="50" y="36" width="38" height="24" rx="6" className="fill-primary/60" />
      <rect x="28" y="8" width="38" height="24" rx="6" className="fill-primary" />
      <path d="M47 14v12M41 20h12" strokeWidth="2.5" strokeLinecap="round" className="stroke-primary-foreground" />
    </svg>
  );
}

export function AddAppCard() {
  const { t } = useI18n();
  const id = useId();
  return (
    <section aria-labelledby={id} data-testid="add-app-card" className="rounded-card bg-card p-5 text-center shadow-card">
      <Boxes />
      <h2 id={id} className="mt-3 text-lg font-semibold">
        {t("dashboard.addApp.title")}
      </h2>
      <p className="mx-auto mt-1 max-w-[30ch] text-balance text-sm text-muted-foreground">{t("dashboard.addApp.body")}</p>
      <Button asChild size="lg" className="mt-4 w-full">
        <Link href="/apps/new/">{t("dashboard.addApp.action")}</Link>
      </Button>
    </section>
  );
}
