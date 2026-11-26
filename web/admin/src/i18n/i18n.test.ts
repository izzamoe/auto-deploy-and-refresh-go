// @vitest-environment jsdom
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { createElement } from "react";
import type { ReactNode } from "react";
import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { en } from "./en";
import { id } from "./id";
import {
  DICTIONARIES,
  I18nProvider,
  LOCALE_STORAGE_KEY,
  detectLocale,
  loadDictionary,
  translate,
  translateError,
  useI18n,
} from "./index";
import type { MessageKey } from "./index";

const SRC = resolve(__dirname, "..");
const SCHEMA = readFileSync(resolve(SRC, "lib", "api", "schema.gen.ts"), "utf8");

function unionValues(name: string): string[] {
  const line = SCHEMA.split("\n").find((l) => l.trim().startsWith(`${name}:`));
  if (!line) throw new Error(`${name} not found in schema.gen.ts`);
  return [...line.matchAll(/"([a-z_]+)"/g)].map((m) => m[1]);
}

const placeholders = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();

describe("dictionaries", () => {
  it("id and en have identical keys", () => {
    expect(Object.keys(en).sort()).toEqual(Object.keys(id).sort());
  });

  it("no message is empty and placeholders match between languages", () => {
    for (const key of Object.keys(id) as MessageKey[]) {
      expect(id[key].trim(), `id ${key}`).not.toBe("");
      expect(en[key].trim(), `en ${key}`).not.toBe("");
      expect(placeholders(en[key]), `placeholders of ${key}`).toEqual(placeholders(id[key]));
    }
  });

  it("maps every OpenAPI ErrorCode in both languages", () => {
    const codes = unionValues("ErrorCode");
    expect(codes.length).toBeGreaterThanOrEqual(20);
    for (const code of codes) {
      for (const [name, dict] of Object.entries(DICTIONARIES)) {
        expect(`error.${code}` in dict, `${name}: error.${code}`).toBe(true);
      }
    }
  });

  it("has no error.* entry for a code the API does not define (besides the client-side ones)", () => {
    const known = new Set([...unionValues("ErrorCode"), "network", "notJson", "unknown", "http"]);
    for (const key of Object.keys(id)) {
      if (key.startsWith("error.")) expect(known.has(key.slice("error.".length)), key).toBe(true);
    }
  });

  it("labels every job status", () => {
    for (const status of unionValues("JobStatus")) {
      expect(`status.${status}` in id, `status.${status}`).toBe(true);
    }
  });
});

describe("translate", () => {
  it("interpolates placeholders and leaves unknown ones visible", () => {
    expect(translate("id", "shell.greeting", { name: "Budi" })).toBe("Halo, Budi");
    expect(translate("en", "shell.subtitle", { active: 3, running: 1 })).toBe("3 active apps · 1 running");
    expect(translate("en", "shell.greeting")).toBe("Hello, {name}");
  });

  // Visual review item 6: "1 active apps" / "of 1 apps" must be singular.
  it("English picks the singular form for 1 and the plural for 0 and 2 (Intl.PluralRules)", () => {
    const sub = (n: number) => translate("en", "shell.subtitle", { active: n, running: 0 });
    expect(sub(0)).toBe("0 active apps · 0 running");
    expect(sub(1)).toBe("1 active app · 0 running");
    expect(sub(2)).toBe("2 active apps · 0 running");
    const of = (n: number) => translate("en", "dashboard.apps.hint", { total: n });
    expect([of(0), of(1), of(2)]).toEqual(["of 0 apps", "of 1 app", "of 2 apps"]);
    const tokens = (n: number) => translate("en", "dashboard.security.tokens", { n });
    expect([tokens(0), tokens(1), tokens(2)]).toEqual(["0 active API tokens", "1 active API token", "2 active API tokens"]);
    const failed = (n: number) => translate("en", "dashboard.failed24h.hint", { total: n });
    expect([failed(0), failed(1), failed(2)]).toEqual(["of 0 deploys", "of 1 deploy", "of 2 deploys"]);
    const unread = (n: number) => translate("en", "shell.notifications.count", { count: n });
    expect([unread(0), unread(1), unread(2)]).toEqual(["0 unread notifications", "1 unread notification", "2 unread notifications"]);
    const up = (n: number) => translate("en", "dashboard.delta.up", { n });
    expect([up(1), up(2)]).toEqual(["up 1 point", "up 2 points"]);
  });

  it("Indonesian has no plural: 0, 1 and 2 use the same form", () => {
    const sub = (n: number) => translate("id", "shell.subtitle", { active: n, running: 0 });
    expect([sub(0), sub(1), sub(2)]).toEqual(["0 aplikasi aktif · 0 berjalan", "1 aplikasi aktif · 0 berjalan", "2 aplikasi aktif · 0 berjalan"]);
    expect(translate("id", "dashboard.apps.hint", { total: 1 })).toBe("dari 1 aplikasi");
  });

  it("every `.one` message has the same placeholders as its base message and a base sibling", () => {
    for (const [name, dict] of [["id", id], ["en", en]] as const) {
      for (const key of Object.keys(dict)) {
        if (!key.endsWith(".one")) continue;
        const base = key.slice(0, -".one".length) as MessageKey;
        expect(base in dict, `${name}: ${key} needs ${base}`).toBe(true);
        expect(placeholders((dict as Record<string, string>)[key]), `${name}: ${key}`).toEqual(placeholders(dict[base]));
      }
    }
  });

  it("maps API codes to localized text and never echoes unknown server text", () => {
    expect(translateError("id", "rate_limited")).toBe(id["error.rate_limited"]);
    expect(translateError("en", "rate_limited")).toBe(en["error.rate_limited"]);
    expect(translateError("en", "totally_new_code")).toBe(en["error.unknown"]);
    expect(translateError("en", undefined, 502)).toBe("The server answered with status 502.");
  });
});

describe("detectLocale (Q-9)", () => {
  it("a stored choice wins over the browser language", () => {
    expect(detectLocale("en", ["id-ID"])).toBe("en");
    expect(detectLocale("id", ["en-US"])).toBe("id");
  });

  it("id for an Indonesian browser, en for anything else, id when nothing is known", () => {
    expect(detectLocale(null, ["id-ID", "en-US"])).toBe("id");
    expect(detectLocale(null, ["in"])).toBe("id");
    expect(detectLocale(null, ["en-US"])).toBe("en");
    expect(detectLocale(null, ["fr-FR"])).toBe("en");
    expect(detectLocale(null, [])).toBe("id");
    expect(detectLocale("de", ["en-US"])).toBe("en");
  });
});

describe("I18nProvider", () => {
  const wrapper = ({ children }: { children: ReactNode }) => createElement(I18nProvider, null, children);

  function stubLanguages(languages: string[]) {
    Object.defineProperty(window.navigator, "languages", { value: languages, configurable: true });
    Object.defineProperty(window.navigator, "language", { value: languages[0], configurable: true });
  }

  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.lang = "";
    stubLanguages(["id-ID"]);
  });
  afterEach(cleanup);

  it("starts as id, becomes ready after reading the locale and sets <html lang>", () => {
    const { result } = renderHook(() => useI18n(), { wrapper });
    expect(result.current.ready).toBe(true);
    expect(result.current.locale).toBe("id");
    expect(document.documentElement.lang).toBe("id");
    expect(result.current.t("nav.apps")).toBe("Aplikasi");
  });

  it("uses en for a non-Indonesian browser without storing it", () => {
    stubLanguages(["en-US"]);
    const { result } = renderHook(() => useI18n(), { wrapper });
    expect(result.current.locale).toBe("en");
    expect(document.documentElement.lang).toBe("en");
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull();
  });

  it("restores a stored locale", () => {
    window.localStorage.setItem(LOCALE_STORAGE_KEY, "en");
    const { result } = renderHook(() => useI18n(), { wrapper });
    expect(result.current.locale).toBe("en");
    expect(result.current.t("nav.apps")).toBe("Apps");
  });

  it("setLocale switches the text, persists the choice and updates <html lang>", () => {
    const { result } = renderHook(() => useI18n(), { wrapper });
    act(() => result.current.setLocale("en"));
    expect(result.current.locale).toBe("en");
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("en");
    expect(document.documentElement.lang).toBe("en");
    act(() => result.current.setLocale("id"));
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("id");
    expect(document.documentElement.lang).toBe("id");
  });

  it("formats numbers and dates per locale", () => {
    const { result } = renderHook(() => useI18n(), { wrapper });
    expect(result.current.formatNumber(1234.5)).toBe("1.234,5");
    act(() => result.current.setLocale("en"));
    expect(result.current.formatNumber(1234.5)).toBe("1,234.5");
    expect(result.current.formatDateTime("2026-01-02T03:04:00Z")).toMatch(/2026/);
  });

  it("falls back to the browser language when storage is blocked", () => {
    const original = Storage.prototype.getItem;
    Storage.prototype.getItem = () => {
      throw new Error("blocked");
    };
    try {
      stubLanguages(["en-GB"]);
      const { result } = renderHook(() => useI18n(), { wrapper });
      expect(result.current.locale).toBe("en");
    } finally {
      Storage.prototype.getItem = original;
    }
  });
});

// SC-010 / FR-104: components never carry user-visible literals; every string goes through
// the dictionary. Scans new-code .tsx files for JSX text and for text attributes. A line may
// opt out with `i18n-ignore` plus a reason (technical identifiers only).
describe("no literal UI strings in components", () => {
  const SCANNED = ["app", "components"];
  const TEXT_ATTRS = /\b(aria-label|aria-description|placeholder|title|alt|label)=("[^"{}]*[A-Za-z]{2,}[^"{}]*")/;
  const JSX_TEXT = />\s*([^<>{}\n]*[A-Za-z]{2,}[^<>{}\n]*?)\s*</;

  function tsxFiles(dir: string): string[] {
    const out: string[] = [];
    for (const name of readdirSync(dir)) {
      const full = join(dir, name);
      if (statSync(full).isDirectory()) out.push(...tsxFiles(full));
      else if (name.endsWith(".tsx") && !name.endsWith(".test.tsx")) out.push(full);
    }
    return out;
  }

  it("the scanner itself flags literals and ignores translated or generic code", () => {
    expect(JSX_TEXT.exec("<p>Hello world</p>")).not.toBeNull();
    expect(JSX_TEXT.exec('<span className="x">Close</span>')).not.toBeNull();
    expect(TEXT_ATTRS.exec('<button aria-label="Close menu">')).not.toBeNull();
    expect(TEXT_ATTRS.exec('<input placeholder="Search" />')).not.toBeNull();
    expect(JSX_TEXT.exec('<p>{t("nav.apps")}</p>')).toBeNull();
    expect(TEXT_ATTRS.exec('<button aria-label={t("shell.menu.open")}>')).toBeNull();
  });

  it("finds none in src/app and src/components", () => {
    const offenders: string[] = [];
    for (const top of SCANNED) {
      const dir = join(SRC, top);
      for (const file of tsxFiles(dir)) {
        readFileSync(file, "utf8")
          .split("\n")
          .forEach((line, i) => {
            const code = line.replace(/\/\/.*$/, "");
            if (line.includes("i18n-ignore") || /^\s*(import|export type|\*|\/\*)/.test(line)) return;
            const jsx = JSX_TEXT.exec(code);
            // `> & VariantProps<…` is a TypeScript generic, not JSX text: operators lead it.
            const m = TEXT_ATTRS.exec(code) ?? (jsx && !/^[&|=,;:)\]]/.test(jsx[1]) ? jsx : null);
            if (m) offenders.push(`${file.slice(SRC.length + 1)}:${i + 1}: ${m[0].trim()}`);
          });
      }
    }
    expect(offenders).toEqual([]);
  });
});

describe("002 realtime messages (T060, FR-023, NFR-010)", () => {
  const REALTIME_KEYS = [
    "shell.connection.online", "shell.connection.reconnecting", "shell.connection.resyncing", "shell.connection.offline", "shell.connection.label",
    "shell.summary.pending",
    "live.region", "live.app.enabled", "live.app.disabled", "live.app.added", "live.app.deleted", "live.service", "live.connection.lost", "live.connection.back",
    "form.conflict.changed", "form.conflict.config", "form.conflict.deleted", "form.conflict.reload",
    "settings.conflict.changed", "settings.conflict.reload",
    "syslogs.live", "syslogs.live.on", "syslogs.pause", "syslogs.resume", "syslogs.paused", "syslogs.newLines", "syslogs.newLines.one", "syslogs.gap", "syslogs.unavailable",
  ] as const;

  it("every realtime message exists in id and en, non-empty, with the same placeholders", () => {
    for (const key of REALTIME_KEYS) {
      expect(id[key as MessageKey], `id ${key}`).toBeTruthy();
      expect(en[key as MessageKey], `en ${key}`).toBeTruthy();
      expect(placeholders(en[key as MessageKey]), key).toEqual(placeholders(id[key as MessageKey]));
    }
  });

  it("the four connection states read differently in both languages (text, not colour alone)", () => {
    for (const dict of [id, en]) {
      const texts = ["shell.connection.online", "shell.connection.reconnecting", "shell.connection.resyncing", "shell.connection.offline"].map((k) => dict[k as MessageKey]);
      expect(new Set(texts).size).toBe(4);
    }
  });

  it("announcements name the app and the service state", () => {
    expect(translate("id", "live.app.disabled", { name: "web" })).toBe("Aplikasi web dinonaktifkan di sesi lain.");
    expect(translate("en", "live.app.disabled", { name: "web" })).toBe("App web was disabled in another session.");
    expect(placeholders(id["live.service"])).toEqual(["name", "state"]);
  });

  it("the messages of the removed polling UI are gone (no auto-refresh, no /stats error in the header)", () => {
    expect(Object.keys(id)).not.toContain("syslogs.autoRefresh");
    expect(Object.keys(id)).not.toContain("shell.stats.error");
  });
});

describe("lazy English dictionary (bundle budget Q-20)", () => {
  it("the en dictionary is fetched on demand and then used; before that translate falls back to id", async () => {
    const loaded = DICTIONARIES.en;
    delete DICTIONARIES.en;
    try {
      expect(translate("en", "common.save")).toBe(id["common.save"]); // not loaded yet: default language
      await loadDictionary("en");
      expect(DICTIONARIES.en?.["common.save"]).toBe(en["common.save"]);
      expect(translate("en", "common.save")).toBe(en["common.save"]);
    } finally {
      DICTIONARIES.en = loaded;
    }
  });
});
