"use client";

import "@/i18n/id/dashboard";
import { IntentLink } from "@/components/common/IntentLink";
import { useId } from "react";
import { CARD_TITLE } from "@/components/common/cardStyles";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";

/** Own illustration (a stack of boxes), not a copy of any reference asset. */
function Boxes() {
  return (
    <svg aria-hidden="true" focusable="false" viewBox="0 0 96 64" className="h-14 w-20">
      <rect x="6" y="36" width="38" height="24" rx="6" fill="currentColor" fillOpacity="0.7" />
      <rect x="50" y="36" width="38" height="24" rx="6" fill="currentColor" fillOpacity="0.85" />
      <rect x="28" y="8" width="38" height="24" rx="6" fill="currentColor" />
      <path d="M47 14v12M41 20h12" strokeWidth="2.5" strokeLinecap="round" className="stroke-gradient-from" />
    </svg>
  );
}

/** Green feature card: same title style and left alignment as every other card. */
export function AddAppCard() {
  const { t } = useI18n();
  const id = useId();
  return (
    <section
      aria-labelledby={id}
      data-testid="add-app-card"
      className="rounded-card bg-gradient-to-br from-gradient-from to-gradient-to p-5 text-gradient-foreground shadow-card"
    >
      <Boxes />
      <h2 id={id} className={`mt-3 ${CARD_TITLE}`}>
        {t("dashboard.addApp.title")}
      </h2>
      <p className="mt-1 max-w-[32ch] text-balance text-sm">{t("dashboard.addApp.body")}</p>
      <Button asChild size="lg" className="mt-4 w-full bg-card text-primary-text hover:bg-card/90">
        <IntentLink href="/apps/new/">{t("dashboard.addApp.action")}</IntentLink>
      </Button>
    </section>
  );
}
