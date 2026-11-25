"use client";

// i18n runtime (D14): typed dictionaries, `{placeholder}` interpolation, Intl formatting,
// the locale stored in localStorage["ad.locale"] and mirrored to <html lang>. No locale in
// the URL: the static export builds one set of pages. The prerendered HTML is `lang="id"`
// (Q-9); the real locale is read after hydration, so `ready` tells the shell when to render
// translated content (no flash of the wrong language).
import { createContext, createElement, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { PropsWithChildren } from "react";
import { en } from "./en";
import { id } from "./id";
import type { MessageKey } from "./id";

export type { MessageKey } from "./id";

export type Locale = "id" | "en";
export const LOCALES: readonly Locale[] = ["id", "en"];
export const DEFAULT_LOCALE: Locale = "id";
export const LOCALE_STORAGE_KEY = "ad.locale";

export const DICTIONARIES: Record<Locale, Record<MessageKey, string>> = { id, en };

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

export function translate(locale: Locale, key: MessageKey, params?: Params): string {
  const template = DICTIONARIES[locale][key] ?? DICTIONARIES[DEFAULT_LOCALE][key] ?? key;
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (whole, name: string) => (name in params ? String(params[name]) : whole));
}

// Maps an API `code` (OpenAPI ErrorCode) to a localized message; unknown codes fall back
// to the generic message instead of echoing server text.
export function translateError(locale: Locale, code: string | undefined, status?: number): string {
  const key = `error.${code ?? ""}` as MessageKey;
  if (code && key in DICTIONARIES[locale]) return translate(locale, key);
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
    setLocaleState(detectLocale(readStored(), languages));
    setReady(true);
  }, []);

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const setLocale = useCallback((next: Locale) => {
    setLocaleState(next);
    try {
      window.localStorage.setItem(LOCALE_STORAGE_KEY, next);
    } catch {
      /* not persisted; still applied for this page */
    }
  }, []);

  const value = useMemo(() => build(locale, ready, setLocale), [locale, ready, setLocale]);
  return createElement(I18nContext.Provider, { value }, children);
}
