// Runtime message registry (no React): dictionaries per locale, filled by the core file and by
// the page-namespace modules as they are imported. Kept separate from index.tsx so the
// namespace files can register without importing the provider (no import cycle).
//
// 002 T072 (FR-029): English is split the same way. Each namespace module registers the loader
// of its en/<ns>.ts next to its Indonesian messages; loaders run only while English is wanted
// (`loadEnglish`), and a namespace imported later (another route) loads its English part right
// away and tells the provider to re-render (`subscribeDictionaries`).
import type { MessageKey } from "./id";

export type Locale = "id" | "en";
export type Dictionary = Partial<Record<MessageKey, string>>;

export const DICTIONARIES: Partial<Record<Locale, Dictionary>> = {};

const listeners = new Set<() => void>();

export function registerMessages(locale: Locale, messages: Dictionary): void {
  DICTIONARIES[locale] = Object.assign(DICTIONARIES[locale] ?? {}, messages);
}

/** Called after messages arrived asynchronously (a late English namespace). */
export function subscribeDictionaries(listener: () => void): () => void {
  listeners.add(listener);
  return () => void listeners.delete(listener);
}

type Loader = () => Promise<Dictionary>;
const enLoaders = new Map<string, Loader>();
/** One key of each namespace: its English part is present when that key has an en message. */
const enProbes = new Map<string, string>();
const enLoading = new Map<string, Promise<Dictionary>>();
let englishWanted = false;

function loadEnNamespace(ns: string): Promise<Dictionary> {
  let p = enLoading.get(ns);
  if (!p) {
    p = enLoaders.get(ns)!().then(
      (dict) => {
        registerMessages("en", dict);
        return dict;
      },
      (err: unknown) => {
        enLoading.delete(ns); // retried the next time English is wanted
        throw err;
      },
    );
    enLoading.set(ns, p);
  }
  return p;
}

const hasEnglish = (ns: string) => {
  const probe = enProbes.get(ns);
  return probe !== undefined && DICTIONARIES.en?.[probe as MessageKey] !== undefined;
};

/** True when a namespace imported so far has no English messages yet. */
export function englishMissing(): boolean {
  return [...enLoaders.keys()].some((ns) => !hasEnglish(ns));
}

/**
 * Registers the loader of the English messages of namespace `ns` (once per namespace).
 * `idMessages` are the namespace's Indonesian messages (same keys): used to tell whether its
 * English part is present already.
 */
export function registerEnLoader(ns: string, loader: Loader, idMessages: Dictionary): void {
  if (enLoaders.has(ns)) return;
  enLoaders.set(ns, loader);
  const probe = Object.keys(idMessages)[0];
  if (probe) enProbes.set(ns, probe);
  if (englishWanted && !hasEnglish(ns)) {
    loadEnNamespace(ns).then(
      () => listeners.forEach((l) => l()),
      () => undefined, // chunk failed: these messages stay in the default language
    );
  }
}

/** Whether namespaces imported from now on load their English part right away. */
export function setEnglishWanted(wanted: boolean): void {
  englishWanted = wanted;
}

/** Loads the English part of every namespace registered so far (each module fetched once). */
export async function loadEnglish(): Promise<void> {
  englishWanted = true;
  const dicts = await Promise.all([...enLoaders.keys()].map(loadEnNamespace));
  for (const d of dicts) registerMessages("en", d); // also after the dictionary was dropped
}

/** Namespaces with a registered English loader (tests). */
export function registeredEnNamespaces(): string[] {
  return [...enLoaders.keys()];
}
