"use client";

import "@/i18n/id/form";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { useI18n } from "@/i18n";

export type PartStatus = "loading" | "error" | "ready";

interface Props {
  id: string;
  label: string;
  hint: string;
  placeholder: string;
  rows: number;
  status: PartStatus;
  value: string;
  onChange: (value: string) => void;
  onRetry?: () => void;
  loadingText: string;
  errorText: string;
  /** Field error (from the server's errors[] or client-side validation). */
  fieldError?: string;
}

/**
 * Textarea for a part of the app that is loaded separately (env, args). FR-074/B53: while it
 * is not loaded - or when loading failed - the editor is DISABLED and says so, so an empty
 * box can never be mistaken for "no values" and saved over the real ones.
 */
export function PartEditor({ id, label, hint, placeholder, rows, status, value, onChange, onRetry, loadingText, errorText, fieldError }: Props) {
  const { t } = useI18n();
  const ready = status === "ready";
  const described = [`${id}-hint`, status !== "ready" ? `${id}-status` : "", fieldError ? `${id}-error` : ""].filter(Boolean).join(" ");
  return (
    <div className="grid gap-2" data-testid={`${id}-editor`} data-status={status}>
      <Label htmlFor={id}>{label}</Label>
      <textarea
        id={id}
        name={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        rows={rows}
        disabled={!ready}
        spellCheck={false}
        autoComplete="off"
        placeholder={placeholder}
        aria-invalid={fieldError ? true : undefined}
        aria-describedby={described}
        className="flex min-h-[80px] w-full rounded-xl border border-input bg-background px-3 py-2 font-mono text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
      />
      {status === "loading" && (
        <p id={`${id}-status`} role="status" className="text-sm text-muted-foreground">
          {loadingText}
        </p>
      )}
      {status === "error" && (
        <div id={`${id}-status`} role="alert" className="flex flex-wrap items-center gap-3 rounded-xl bg-warning-soft px-3 py-2 text-sm text-warning-soft-foreground">
          <span>{errorText}</span>
          {onRetry && (
            <Button type="button" size="sm" variant="outline" onClick={onRetry}>
              {t("form.reloadPart")}
            </Button>
          )}
        </div>
      )}
      {fieldError && (
        <p id={`${id}-error`} className="text-sm text-danger-soft-foreground" role="alert">
          {fieldError}
        </p>
      )}
      <p id={`${id}-hint`} className="text-sm text-muted-foreground">
        {hint}
      </p>
    </div>
  );
}
