"use client";

import "@/i18n/id/form";
import { useI18n } from "@/i18n";
import { PartEditor } from "./PartEditor";
import type { PartStatus } from "./PartEditor";

export function EnvEditor(props: { status: PartStatus; value: string; onChange: (v: string) => void; onRetry?: () => void; fieldError?: string }) {
  const { t } = useI18n();
  return (
    <PartEditor
      id="env-vars"
      label={t("form.env")}
      hint={t("form.env.hint")}
      placeholder={"BOT_TOKEN=123:abc\nLOG_LEVEL=info"}
      rows={4}
      loadingText={t("form.env.loading")}
      errorText={t("form.env.loadError")}
      {...props}
    />
  );
}
