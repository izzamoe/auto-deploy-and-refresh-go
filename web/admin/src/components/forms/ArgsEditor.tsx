"use client";

import "@/i18n/id/form";
import { useI18n } from "@/i18n";
import { PartEditor } from "./PartEditor";
import type { PartStatus } from "./PartEditor";

export function ArgsEditor(props: { status: PartStatus; value: string; onChange: (v: string) => void; onRetry?: () => void; fieldError?: string }) {
  const { t } = useI18n();
  return (
    <PartEditor
      id="service-args"
      label={t("form.args")}
      hint={t("form.args.hint")}
      placeholder={"--port 8080\n--config /etc/myapp.yaml"}
      rows={3}
      loadingText={t("form.args.loading")}
      errorText={t("form.args.loadError")}
      {...props}
    />
  );
}
