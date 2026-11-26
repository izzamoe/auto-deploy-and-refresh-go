// Per-route message namespaces (bundle budget Q-20): the shared bundle only carries the core
// messages; a page/component imports the namespace module of the text it uses. These tests
// make a forgotten import a failing test instead of a route that shows raw keys.
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";
import { id } from "./id";
import type { Dictionary } from "./registry";

const SRC = resolve(__dirname, "..");

function nsKeys(file: string): string[] {
  return [...readFileSync(join(SRC, "i18n", "id", `${file}.ts`), "utf8").matchAll(/^ {2}"([^"]+)":/gm)].map((m) => m[1]);
}

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) sourceFiles(p, out);
    else if (/\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name)) out.push(p);
  }
  return out;
}

const NAMESPACES = readdirSync(join(SRC, "i18n", "id"))
  .map((f) => f.replace(/\.ts$/, ""))
  .filter((f) => f !== "core");

describe("message namespaces", () => {
  it("id.ts is exactly core + every namespace (no key defined twice)", () => {
    const all = ["core", ...NAMESPACES].flatMap(nsKeys);
    expect(new Set(all).size).toBe(all.length);
    expect([...all].sort()).toEqual(Object.keys(id).sort());
  });

  it("every file that uses a namespaced key imports that namespace", () => {
    const offenders: string[] = [];
    const files = [...sourceFiles(join(SRC, "app")), ...sourceFiles(join(SRC, "components"))];
    for (const ns of NAMESPACES) {
      const keys = nsKeys(ns);
      for (const file of files) {
        const text = readFileSync(file, "utf8");
        const exact = [...text.matchAll(/["'`]([a-z][A-Za-z]*(?:\.[A-Za-z0-9_]+)+)["'`]/g)].map((m) => m[1]);
        const prefixes = [...text.matchAll(/`([a-z][A-Za-z]*(?:\.[A-Za-z0-9_]+)*\.)\$\{/g)].map((m) => m[1]);
        const uses = exact.some((k) => keys.includes(k)) || prefixes.some((p) => keys.some((k) => k.startsWith(p)));
        if (uses && !text.includes(`@/i18n/id/${ns}"`)) offenders.push(`${file.slice(SRC.length + 1)} uses '${ns}' keys without importing @/i18n/id/${ns}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("the shared runtime (index.tsx) imports only the core messages, never the full id.ts", () => {
    const index = readFileSync(join(SRC, "i18n", "index.tsx"), "utf8");
    expect(index).toMatch(/from "\.\/id\/core"/);
    expect(index).not.toMatch(/^(import|export) (?!type)[^\n]*from "\.\/id"/m); // type-only imports are erased
  });
});

// 002 T072 (FR-029): English follows the same namespaces. Each id namespace registers the loader
// of its en/<ns>.ts; loaders run only while the English UI is wanted, so an English user fetches
// the English text of the routes they open, not one chunk with the whole admin.
function enKeys(file: string): string[] {
  return [...readFileSync(join(SRC, "i18n", "en", `${file}.ts`), "utf8").matchAll(/^ {2}"([^"]+)":/gm)].map((m) => m[1]);
}

describe("English per namespace (T072)", () => {
  const ALL = ["core", ...NAMESPACES];

  it("every namespace has en/<ns>.ts with exactly the keys of id/<ns>.ts", () => {
    for (const ns of ALL) expect(enKeys(ns).sort(), ns).toEqual(nsKeys(ns).sort());
    expect(readdirSync(join(SRC, "i18n", "en")).map((f) => f.replace(/\.ts$/, "")).sort()).toEqual([...ALL].sort());
  });

  it("every namespace registers the loader of its own English file (core: index.tsx)", () => {
    for (const ns of NAMESPACES) {
      const text = readFileSync(join(SRC, "i18n", "id", `${ns}.ts`), "utf8");
      expect(text, ns).toContain(`registerEnLoader("${ns}", () => import("../en/${ns}")`);
    }
    const index = readFileSync(join(SRC, "i18n", "index.tsx"), "utf8");
    expect(index).toContain('registerEnLoader("core", () => import("./en/core")');
  });

  it("no runtime file imports the complete en.ts (only the tests and the type check do)", () => {
    const offenders = [...sourceFiles(join(SRC, "app")), ...sourceFiles(join(SRC, "components")), ...sourceFiles(join(SRC, "lib")), ...sourceFiles(join(SRC, "i18n"))]
      .filter((f) => !f.endsWith(join("i18n", "en.ts")))
      .filter((f) => /["'](?:\.\.?\/en|@\/i18n\/en)["']/.test(readFileSync(f, "utf8")));
    expect(offenders.map((f) => f.slice(SRC.length + 1))).toEqual([]);
  });

  it("a namespace's English loader runs only while English is wanted; a later namespace loads at once and notifies", async () => {
    const registry = await import("./registry");
    const notified = vi.fn();
    const off = registry.subscribeDictionaries(notified);
    const asDict = (o: Record<string, string>) => o as unknown as Dictionary;
    const idle = vi.fn(async () => asDict({ "zz.idle": "Idle" }));
    const late = vi.fn(async () => asDict({ "zz.late": "Late" }));
    try {
      registry.setEnglishWanted(false);
      registry.registerEnLoader("zz-idle", idle, asDict({ "zz.idle": "Diam" }));
      expect(idle).not.toHaveBeenCalled(); // Indonesian UI: no English fetched

      registry.setEnglishWanted(true);
      registry.registerEnLoader("zz-late", late, asDict({ "zz.late": "Telat" }));
      expect(late).toHaveBeenCalledTimes(1); // English UI: a namespace imported now loads at once
      await vi.waitFor(() => expect(notified).toHaveBeenCalled());
      expect((registry.DICTIONARIES.en as Record<string, string>)["zz.late"]).toBe("Late");
      expect(registry.englishMissing()).toBe(true); // zz-idle was never loaded

      await registry.loadEnglish(); // switching to English loads what is missing, once per namespace
      expect(idle).toHaveBeenCalledTimes(1);
      expect(late).toHaveBeenCalledTimes(1);
      expect(registry.englishMissing()).toBe(false);
    } finally {
      off();
      registry.setEnglishWanted(false);
      const en = registry.DICTIONARIES.en as Record<string, string> | undefined;
      if (en) {
        delete en["zz.idle"];
        delete en["zz.late"];
      }
    }
  });
});
