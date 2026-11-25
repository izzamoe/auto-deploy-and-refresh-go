// Runtime message registry (no React): dictionaries per locale, filled by the core file and by
// the page-namespace modules as they are imported. Kept separate from index.tsx so the
// namespace files can register without importing the provider (no import cycle).
import type { MessageKey } from "./id";

export type Locale = "id" | "en";
export type Dictionary = Partial<Record<MessageKey, string>>;

export const DICTIONARIES: Partial<Record<Locale, Dictionary>> = {};

export function registerMessages(locale: Locale, messages: Dictionary): void {
  DICTIONARIES[locale] = Object.assign(DICTIONARIES[locale] ?? {}, messages);
}
