// @vitest-environment node
//
// Visual review item 5: no text below 12 px anywhere in the UI source (arbitrary sizes such as
// text-[11px] or an inline font-size under 12).
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";

const SRC = resolve(__dirname, "..");

function files(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name);
    if (statSync(full).isDirectory()) files(full, out);
    else if (/\.(tsx|css)$/.test(name) && !/\.test\./.test(name)) out.push(full);
  }
  return out;
}

describe("minimum text size", () => {
  it("has no text-[Npx] utility with N < 12 and no font-size below 12px", () => {
    const offenders: string[] = [];
    for (const file of files(SRC)) {
      const text = readFileSync(file, "utf8");
      for (const m of text.matchAll(/text-\[(\d+(?:\.\d+)?)px\]/g)) if (Number(m[1]) < 12) offenders.push(`${file}: ${m[0]}`);
      for (const m of text.matchAll(/font-size:\s*(\d+(?:\.\d+)?)px/g)) if (Number(m[1]) < 12) offenders.push(`${file}: ${m[0]}`);
      for (const m of text.matchAll(/text-\[(\d+(?:\.\d+)?)rem\]/g)) if (Number(m[1]) < 0.75) offenders.push(`${file}: ${m[0]}`);
    }
    expect(offenders).toEqual([]);
  });
});
