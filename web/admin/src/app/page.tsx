"use client";

import { useI18n } from "@/i18n";

// Placeholder root route so the export has an index.html; replaced by the dashboard
// inside the app shell (T086).
export default function Home() {
  const { t } = useI18n();
  return <main data-testid="home">{t("app.name")}</main>;
}
