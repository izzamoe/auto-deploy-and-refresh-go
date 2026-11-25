// @vitest-environment node
//
// T082 (FR-102, FR-103, NFR-009): WCAG 2.x contrast of the design tokens in BOTH themes.
// Ratios are computed from tokens.css itself, so changing a token re-runs the proof.
//   text pairs  >= 4.5:1   (AA, normal text)
//   UI pairs    >= 3:1     (AA, non-text contrast: control boundary, focus ring, glyphs)
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";

const SRC = resolve(__dirname, "..");
const TOKENS = readFileSync(join(__dirname, "tokens.css"), "utf8");

type Theme = "light" | "dark";

function block(css: string, selector: string): string {
  const start = css.indexOf(`${selector} {`);
  if (start < 0) throw new Error(`no ${selector} block in tokens.css`);
  const open = css.indexOf("{", start);
  const close = css.indexOf("}", open);
  return css.slice(open + 1, close);
}

function parseTokens(selector: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of block(TOKENS, selector).matchAll(/--([a-z0-9-]+):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
    out[m[1]] = m[2].toLowerCase();
  }
  return out;
}

const THEMES: Record<Theme, Record<string, string>> = {
  light: parseTokens(":root"),
  dark: parseTokens(".dark"),
};

function channel(v: number): number {
  const c = v / 255;
  return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
}

export function luminance(hex: string): number {
  const n = parseInt(hex.slice(1), 16);
  return 0.2126 * channel((n >> 16) & 255) + 0.7152 * channel((n >> 8) & 255) + 0.0722 * channel(n & 255);
}

export function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

// [foreground token, background token, minimum ratio, what it protects]
const TEXT = 4.5;
const UI = 3;
const PAIRS: Array<[string, string, number, string]> = [
  ["foreground", "background", TEXT, "body text on the frame"],
  ["foreground", "card", TEXT, "body text on a card"],
  ["card-foreground", "card", TEXT, "card text"],
  ["popover-foreground", "popover", TEXT, "popover / dialog text"],
  ["muted-foreground", "background", TEXT, "secondary text on the frame"],
  ["muted-foreground", "card", TEXT, "secondary text on a card"],
  ["muted-foreground", "muted", TEXT, "secondary text on a muted surface"],
  ["secondary-foreground", "secondary", TEXT, "secondary button label"],
  ["primary-foreground", "primary", TEXT, "primary button label"],
  ["primary-text", "card", TEXT, "green text on a card"],
  ["primary-text", "background", TEXT, "green text on the frame"],
  ["gradient-foreground", "gradient-from", TEXT, "accent card text, light stop"],
  ["gradient-foreground", "gradient-to", TEXT, "accent card text, dark stop"],
  ["accent-foreground", "accent", TEXT, "accent pill"],
  ["success-soft-foreground", "success-soft", TEXT, "success pill"],
  ["destructive-foreground", "destructive", TEXT, "destructive button label"],
  ["danger-soft-foreground", "danger-soft", TEXT, "danger pill"],
  ["warning-soft-foreground", "warning-soft", TEXT, "warning pill"],
  ["info-soft-foreground", "info-soft", TEXT, "info pill"],
  ["nav-foreground", "nav", UI, "active navigation icon on its disc"],
  ["input", "card", UI, "input / select / switch boundary on a card"],
  ["ring", "card", UI, "focus ring on a card"],
  ["ring", "background", UI, "focus ring on the frame"],
  ["nav", "card", UI, "active navigation disc on the sidebar pill"],
  ["rail-foreground", "rail", TEXT, "sidebar rail icons / text"],
  ["rail-muted", "rail", TEXT, "inactive sidebar icons and language labels"],
  ["rail-active-foreground", "rail-active", TEXT, "active sidebar item"],
  ["rail-active", "rail", UI, "active sidebar disc against the rail"],
  ["track", "card", UI, "gauge track against a card"],
  ["primary", "card", UI, "gauge arc / chart line against a card"],
  ["primary", "background", UI, "chart line / chip against the frame"],
  ["primary-foreground", "primary", TEXT, "icon or label on a green chip"],
  ["danger-chart", "card", TEXT, "failed figure / red chart line on a card"],
  ["danger-chart", "background", TEXT, "red figure on the frame"],
];

const report: string[] = [];

describe.each(["light", "dark"] as Theme[])("%s theme contrast (WCAG 2.x AA)", (theme) => {
  const t = THEMES[theme];

  it.each(PAIRS)("%s on %s >= %s:1 (%s)", (fg, bg, min, why) => {
    expect(t[fg], `token --${fg} missing in ${theme}`).toBeDefined();
    expect(t[bg], `token --${bg} missing in ${theme}`).toBeDefined();
    const ratio = contrast(t[fg], t[bg]);
    report.push(`${theme.padEnd(5)} ${fg.padEnd(26)} on ${bg.padEnd(14)} ${t[fg]} / ${t[bg]}  ${ratio.toFixed(2)}:1  (min ${min}) ${why}`);
    expect(ratio, `${theme}: --${fg} on --${bg} (${why}) is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(min);
  });
});

describe("token files", () => {
  it("defines the same tokens in the light and dark themes", () => {
    const light = Object.keys(THEMES.light).sort();
    const dark = Object.keys(THEMES.dark).sort();
    // The dark block may omit nothing that the light block defines.
    expect(dark).toEqual(light);
  });

  it("matches the reference accents that passed the note 06 audit", () => {
    expect(THEMES.light.primary).toBe("#4e7358");
    expect(contrast("#ffffff", THEMES.light.primary)).toBeGreaterThanOrEqual(5.3);
  });

  it("the logo glyph is readable on the brand disc", () => {
    const logo = readFileSync(resolve(SRC, "..", "public", "logo.svg"), "utf8");
    expect(logo).toContain('fill="#4E7358"');
    expect(contrast("#ffffff", "#4e7358")).toBeGreaterThanOrEqual(UI);
  });

  // T101-pre: the translucent boxes of the "Tambah aplikasi" illustration (white at the
  // opacity written in AddAppCard.tsx) are graphics: >= 3:1 on BOTH gradient stops, both themes.
  it.each(["light", "dark"] as Theme[])("the add-app illustration boxes are >= 3:1 on the gradient (%s)", (theme) => {
    const src = readFileSync(resolve(SRC, "components", "dashboard", "AddAppCard.tsx"), "utf8");
    const opacities = [...src.matchAll(/fillOpacity="([0-9.]+)"/g)].map((m) => parseFloat(m[1]));
    expect(opacities.length).toBe(2);
    const mixHex = (bg: string, o: number) => {
      const n = parseInt(bg.slice(1), 16);
      const ch = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((c) => Math.round(c * (1 - o) + 255 * o));
      return `#${ch.map((c) => c.toString(16).padStart(2, "0")).join("")}`;
    };
    for (const stop of ["gradient-from", "gradient-to"]) {
      for (const o of opacities) {
        const r = contrast(mixHex(THEMES[theme][stop], o), THEMES[theme][stop]);
        console.log(`${theme} add-app box opacity ${o} on --${stop}: ${r.toFixed(2)}:1`);
        expect(r, `${theme}: opacity ${o} on --${stop}`).toBeGreaterThanOrEqual(UI);
      }
    }
  });

  // The three colours measured and rejected in note 06 §4 must never come back as a
  // text or background colour. Built from parts so this file does not contain them.
  const FORBIDDEN = [["5F", "8567"], ["64", "7f70"], ["8E", "8E8A"]].map(([a, b]) => `#${a}${b}`.toLowerCase());

  it("no source file uses the rejected low-contrast colours", () => {
    const offenders: string[] = [];
    const walk = (dir: string) => {
      for (const name of readdirSync(dir)) {
        const full = join(dir, name);
        if (statSync(full).isDirectory()) {
          if (name !== "node_modules") walk(full);
        } else if (/\.(css|ts|tsx|svg)$/.test(name)) {
          const text = readFileSync(full, "utf8").toLowerCase();
          for (const hex of FORBIDDEN) if (text.includes(hex)) offenders.push(`${full}: ${hex}`);
        }
      }
    };
    walk(SRC);
    walk(resolve(SRC, "..", "public"));
    expect(offenders).toEqual([]);
  });

  it("prints the measured ratios (kept in the phase log)", () => {
    console.log(`\nWCAG contrast ratios\n${report.sort().join("\n")}`);
    expect(report.length).toBeGreaterThan(0);
  });
});
