"use client";

// i18n runtime (D14): typed dictionaries, `{placeholder}` interpolation, Intl formatting,
// the locale stored in localStorage["ad.locale"] and mirrored to <html lang>. No locale in
// the URL: the static export builds one set of pages. The prerendered HTML is `lang="id"`
// (Q-9); the real locale is read after hydration, so `ready` tells the shell when to render
// translated content (no flash of the wrong language).
import { createContext, createElement, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { PropsWithChildren } from "react";
import { idCore } from "./id/core";
import { DICTIONARIES, registerMessages } from "./registry";
import type { Dictionary, Locale } from "./registry";
import type { MessageKey } from "./id";

export type { MessageKey } from "./id";

export type { Locale } from "./registry";
export { DICTIONARIES, registerMessages } from "./registry";
export const LOCALES: readonly Locale[] = ["id", "en"];
export const DEFAULT_LOCALE: Locale = "id";
export const LOCALE_STORAGE_KEY = "ad.locale";

// `id` (the default) starts with the CORE messages only; every page/component imports the
// namespace module of the text it uses (see i18n/id/*.ts), so a route ships just its own
// text. `en` is one separate chunk fetched only when the English UI is needed. Both keep the
// initial JS (Q-20) small.
registerMessages("id", idCore);

export function registerDictionary(locale: Locale, dict: Dictionary) {
  registerMessages(locale, dict);
}

/** Resolves once the dictionary of `locale` is available (no-op when already loaded). */
export async function loadDictionary(locale: Locale): Promise<void> {
  if (locale === "en" ? DICTIONARIES.en && Object.keys(DICTIONARIES.en).length > 0 : DICTIONARIES[locale]) return;
  if (locale === "en") registerDictionary("en", (await import("./en")).en);
}

const INTL_TAG: Record<Locale, string> = { id: "id-ID", en: "en-GB" };

export type Params = Record<string, string | number>;

export function isLocale(value: unknown): value is Locale {
  return value === "id" || value === "en";
}

// Q-9: a stored choice wins; otherwise `id` when the browser prefers Indonesian, else `en`.
export function detectLocale(stored: string | null | undefined, languages: readonly string[]): Locale {
  if (isLocale(stored)) return stored;
  const first = languages.find((l) => typeof l === "string" && l.length > 0);
  if (!first) return DEFAULT_LOCALE;
  return /^(id|in)(-|$)/i.test(first) ? "id" : "en";
}

// Plural forms: a message with a `<key>.one` sibling is plural. The CLDR category (Intl.PluralRules)
// of the first numeric param among PLURAL_PARAMS picks `<key>.<category>`; the plain key is the
// "other" form and the fallback. Indonesian has only "other", so its `.one` text is never chosen.
const PLURAL_PARAMS = ["n", "count", "active", "total"] as const;

function pluralKey(locale: Locale, key: MessageKey, params: Params | undefined): MessageKey {
  if (!params) return key;
  const name = PLURAL_PARAMS.find((p) => typeof params[p] === "number");
  if (!name) return key;
  const candidate = `${key}.${new Intl.PluralRules(INTL_TAG[locale]).select(params[name] as number)}` as MessageKey;
  return DICTIONARIES[locale]?.[candidate] !== undefined ? candidate : key;
}

export function translate(locale: Locale, key: MessageKey, params?: Params): string {
  const picked = pluralKey(locale, key, params);
  const template = DICTIONARIES[locale]?.[picked] ?? DICTIONARIES.id?.[key] ?? key;
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (whole, name: string) => (name in params ? String(params[name]) : whole));
}

// Maps an API `code` (OpenAPI ErrorCode) to a localized message; unknown codes fall back
// to the generic message instead of echoing server text.
export function translateError(locale: Locale, code: string | undefined, status?: number): string {
  const key = `error.${code ?? ""}` as MessageKey;
  if (code && key in (DICTIONARIES[locale] ?? DICTIONARIES.id ?? {})) return translate(locale, key);
  if (status !== undefined && status >= 400 && !code) return translate(locale, "error.http", { status });
  return translate(locale, "error.unknown");
}

export interface I18n {
  locale: Locale;
  /** False until the stored/browser locale was read after hydration. */
  ready: boolean;
  setLocale: (locale: Locale) => void;
  t: (key: MessageKey, params?: Params) => string;
  errorMessage: (code: string | undefined, status?: number) => string;
  formatDateTime: (value: Date | string | number) => string;
  formatNumber: (value: number, options?: Intl.NumberFormatOptions) => string;
  /** "25 Nov" for a YYYY-MM-DD bucket date (calendar date, no timezone shift). */
  formatDay: (isoDate: string) => string;
  /** "3 menit lalu" / "in 2 hours" relative to `now` (injectable for tests). */
  formatRelative: (value: Date | string | number, now?: number) => string;
}

const RELATIVE_UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["day", 86_400_000],
  ["hour", 3_600_000],
  ["minute", 60_000],
  ["second", 1_000],
];

export function relativeTime(tag: string, then: number, now: number): string {
  const rtf = new Intl.RelativeTimeFormat(tag, { numeric: "auto" });
  const diff = then - now;
  for (const [unit, ms] of RELATIVE_UNITS) {
    if (Math.abs(diff) >= ms || unit === "second") return rtf.format(Math.round(diff / ms), unit);
  }
  return "";
}

function build(locale: Locale, ready: boolean, setLocale: (l: Locale) => void): I18n {
  const tag = INTL_TAG[locale];
  const dateFormat = new Intl.DateTimeFormat(tag, { dateStyle: "medium", timeStyle: "short" });
  return {
    locale,
    ready,
    setLocale,
    t: (key, params) => translate(locale, key, params),
    errorMessage: (code, status) => translateError(locale, code, status),
    formatDateTime: (value) => dateFormat.format(value instanceof Date ? value : new Date(value)),
    formatNumber: (value, options) => new Intl.NumberFormat(tag, options).format(value),
    formatDay: (isoDate) => new Intl.DateTimeFormat(tag, { day: "numeric", month: "short", timeZone: "UTC" }).format(new Date(`${isoDate}T00:00:00Z`)),
    formatRelative: (value, now = Date.now()) => relativeTime(tag, new Date(value).getTime(), now),
  };
}

const I18nContext = createContext<I18n>(build(DEFAULT_LOCALE, false, () => {}));

export function useI18n(): I18n {
  return useContext(I18nContext);
}

function readStored(): string | null {
  try {
    return window.localStorage.getItem(LOCALE_STORAGE_KEY);
  } catch {
    return null; // storage blocked (private mode): fall back to the browser language
  }
}

export function I18nProvider({ children }: PropsWithChildren) {
  // Same value on the server and on the first client render: no hydration mismatch.
  const [locale, setLocaleState] = useState<Locale>(DEFAULT_LOCALE);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    const languages = navigator.languages?.length ? navigator.languages : [navigator.language];
    const target = detectLocale(readStored(), languages);
    if (DICTIONARIES[target]) {
      setLocaleState(target); // already loaded: apply in the same pass
      setReady(true);
      return;
    }
    let cancelled = false;
    void loadDictionary(target)
      .catch(() => undefined) // chunk failed to load: stay on the default language
      .then(() => {
        if (cancelled) return;
        setLocaleState(DICTIONARIES[target] ? target : DEFAULT_LOCALE);
        setReady(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const setLocale = useCallback((next: Locale) => {
    if (DICTIONARIES[next]) setLocaleState(next);
    else void loadDictionary(next).then(() => setLocaleState(next)).catch(() => undefined);
    try {
      window.localStorage.setItem(LOCALE_STORAGE_KEY, next);
    } catch {
      /* not persisted; still applied for this page */
    }
  }, []);

  const value = useMemo(() => build(locale, ready, setLocale), [locale, ready, setLocale]);
  return createElement(I18nContext.Provider, { value }, children);
}
