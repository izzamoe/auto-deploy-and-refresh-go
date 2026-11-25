"use client";

import { AlertTriangle, CheckCircle2, CircleSlash, Clock, Loader2, XCircle } from "lucide-react";
import type { ComponentType } from "react";
import { useI18n } from "@/i18n";
import type { MessageKey } from "@/i18n";
import { cn } from "@/lib/utils";

type Tone = { cls: string; Icon: ComponentType<{ className?: string; "aria-hidden"?: boolean }> };

// Status = icon + text + colour (never colour alone). Tokens only, so both themes pass AA.
const TONES: Record<string, Tone> = {
  succeeded: { cls: "bg-success-soft text-success-soft-foreground", Icon: CheckCircle2 },
  failed: { cls: "bg-danger-soft text-danger-soft-foreground", Icon: XCircle },
  in_progress: { cls: "bg-info-soft text-info-soft-foreground", Icon: Loader2 },
  pending: { cls: "bg-info-soft text-info-soft-foreground", Icon: Clock },
  cancel_requested: { cls: "bg-warning-soft text-warning-soft-foreground", Icon: AlertTriangle },
  canceled: { cls: "bg-secondary text-secondary-foreground", Icon: CircleSlash },
};

export function StatusPill({ status, className }: { status: string | undefined; className?: string }) {
  const { t } = useI18n();
  const key = (status && status in TONES ? status : "unknown") as string;
  const tone = TONES[key] ?? { cls: "bg-secondary text-secondary-foreground", Icon: CircleSlash };
  const label = t(`status.${key}` as MessageKey);
  return (
    <span className={cn("inline-flex items-center gap-1 rounded-pill px-2.5 py-0.5 text-xs font-medium", tone.cls, className)} data-status={key}>
      <tone.Icon aria-hidden className={cn("size-3.5", key === "in_progress" && "motion-safe:animate-spin")} />
      {label}
    </span>
  );
}
