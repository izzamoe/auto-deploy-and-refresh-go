"use client";

import { LOCALES, useI18n } from "@/i18n";
import type { Locale } from "@/i18n";
import { cn } from "@/lib/utils";

const NAME_KEY = { id: "shell.locale.id", en: "shell.locale.en" } as const;

export function LocaleToggle({ className }: { className?: string }) {
  const { t, locale, setLocale } = useI18n();
  return (
    <div role="group" aria-label={t("shell.locale.label")} className={cn("inline-flex rounded-full bg-muted p-0.5", className)}>
      {LOCALES.map((l: Locale) => (
        <button
          key={l}
          type="button"
          lang={l}
          aria-pressed={locale === l}
          aria-label={t(NAME_KEY[l])}
          onClick={() => setLocale(l)}
          className={cn(
            "rounded-full px-2.5 py-1 text-xs font-semibold",
            locale === l ? "bg-nav text-nav-foreground" : "text-muted-foreground hover:text-foreground",
          )}
        >
          {l.toUpperCase()}
        </button>
      ))}
    </div>
  );
}
