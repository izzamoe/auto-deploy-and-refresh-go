// Per-route message namespaces (bundle budget Q-20): the shared bundle only carries the core
// messages; a page/component imports the namespace module of the text it uses. These tests
// make a forgotten import a failing test instead of a route that shows raw keys.
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { id } from "./id";

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
