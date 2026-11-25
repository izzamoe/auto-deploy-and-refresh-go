"use client";

import { useI18n } from "@/i18n";

// Becomes 404.html in the export; the Go handler serves it with status 404 (D1).
export default function NotFound() {
  const { t } = useI18n();
  return (
    <main data-testid="not-found" className="mx-auto max-w-md p-8 text-center">
      <h1 className="text-2xl font-semibold">{t("notfound.title")}</h1>
      <p className="mt-2 text-muted-foreground">{t("notfound.body")}</p>
      <a className="mt-6 inline-block text-primary-text underline" href="/admin/">
        {t("notfound.home")}
      </a>
    </main>
  );
}
