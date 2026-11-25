"use client";

import { LOCALES, useI18n } from "@/i18n";
import type { Locale } from "@/i18n";
import { cn } from "@/lib/utils";

const NAME_KEY = { id: "shell.locale.id", en: "shell.locale.en" } as const;

/** `touch` = buttons at least 44 px tall (the mobile "More" dialog); the rail keeps the compact size. */
export function LocaleToggle({ className, touch = false }: { className?: string; touch?: boolean }) {
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
            touch && "inline-flex min-h-11 min-w-11 items-center justify-center px-4 text-sm",
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
