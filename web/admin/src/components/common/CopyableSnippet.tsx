"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";

export function CopyableSnippet({ label, code, testId }: { label: string; code: string; testId: string }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopied(false); // clipboard blocked: the text stays selectable in the <pre>
    }
  }
  return (
    <div className="grid gap-2">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm font-medium">{label}</p>
        <Button type="button" size="sm" variant="outline" onClick={() => void copy()}>
          {copied ? t("common.copied") : t("common.copy")}
        </Button>
      </div>
      <pre data-testid={testId} tabIndex={0} className="max-h-64 overflow-x-auto whitespace-pre-wrap break-all rounded-xl bg-muted p-3 text-xs">
        {code}
      </pre>
    </div>
  );
}
