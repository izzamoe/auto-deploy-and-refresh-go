"use client";

// Inline result notice (replaces the old `?flash=` banner, B59): lives in component state,
// is announced to screen readers (role=status / alert), never read from the URL.
import { X } from "lucide-react";
import { useCallback, useState } from "react";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";

export interface NoticeState {
  kind: "success" | "error";
  text: string;
}

export function useNotice() {
  const [notice, setNotice] = useState<NoticeState | null>(null);
  const success = useCallback((text: string) => setNotice({ kind: "success", text }), []);
  const error = useCallback((text: string) => setNotice({ kind: "error", text }), []);
  const clear = useCallback(() => setNotice(null), []);
  return { notice, success, error, clear };
}

export function Notice({ notice, onDismiss, className }: { notice: NoticeState | null; onDismiss: () => void; className?: string }) {
  const { t } = useI18n();
  if (!notice) return null;
  return (
    <div
      role={notice.kind === "error" ? "alert" : "status"}
      data-testid="notice"
      data-kind={notice.kind}
      className={cn(
        "flex items-start justify-between gap-3 rounded-2xl px-4 py-3 text-sm",
        notice.kind === "error" ? "bg-danger-soft text-danger-soft-foreground" : "bg-success-soft text-success-soft-foreground",
        className,
      )}
    >
      <span>{notice.text}</span>
      <button type="button" onClick={onDismiss} aria-label={t("common.dismiss")} className="rounded-full p-1">
        <X className="size-4" aria-hidden="true" />
      </button>
    </div>
  );
}
