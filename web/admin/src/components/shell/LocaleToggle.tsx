"use client";

import { LOCALES, useI18n } from "@/i18n";
import type { Locale } from "@/i18n";
import { cn } from "@/lib/utils";

const NAME_KEY = { id: "shell.locale.id", en: "shell.locale.en" } as const;

export function LocaleToggle({ className }: { className?: string }) {
  const { t, locale, setLocale } = useI18n();
  return (
    <div role="group" aria-label={t("shell.locale.label")} className={cn("inline-flex rounded-full bg-black/25 p-0.5", className)}>
      {LOCALES.map((l: Locale) => (
        <button
          key={l}
          type="button"
          lang={l}
          aria-pressed={locale === l}
          onClick={() => setLocale(l)}
          className={cn(
            "rounded-full px-2.5 py-1 text-xs font-semibold focus-visible:outline-rail-foreground",
            locale === l ? "bg-rail-active text-rail-active-foreground" : "text-rail-muted hover:text-rail-foreground",
          )}
        >
          {l.toUpperCase()}{" "}
          {/* WCAG 2.5.3: the accessible name starts with the visible text ("EN English"). */}
          <span className="sr-only">{t(NAME_KEY[l])}</span>
        </button>
      ))}
    </div>
  );
}
