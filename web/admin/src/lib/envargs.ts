// Text <-> structure helpers for the env and args editors (ported from the old api.ts, which
// is removed in T093). Pure functions; covered by envargs.test.ts.
import type { Schemas } from "@/lib/api/client";

export type EnvVar = Schemas["EnvVar"];

export const ENV_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** "NAME=VALUE" per line; blank lines and `#` comments are skipped; the value keeps later "=". */
export function parseEnvText(text: string): EnvVar[] {
  const vars: EnvVar[] = [];
  for (const rawLine of text.split("\n")) {
    const line = rawLine.trim();
    if (line === "" || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq === -1) {
      vars.push({ name: line, value: "" });
      continue;
    }
    vars.push({ name: line.slice(0, eq).trim(), value: line.slice(eq + 1) });
  }
  return vars;
}

export function envVarsToText(vars: readonly EnvVar[]): string {
  return vars.map((v) => `${v.name}=${v.value}`).join("\n");
}

/** First variable whose name the server would reject (so we can say which line), if any. */
export function firstInvalidEnvName(vars: readonly EnvVar[]): string | undefined {
  return vars.find((v) => !ENV_NAME_RE.test(v.name))?.name;
}

/** Normal form used to decide "did the user change this": parse then print. */
export const normalizeEnvText = (text: string) => envVarsToText(parseEnvText(text));

/** Splits like a shell: quotes and backslashes honoured, `#` comment lines skipped. */
export function parseArgsText(text: string): string[] {
  const args: string[] = [];
  let current = "";
  let hasToken = false;
  let quote: "'" | '"' | null = null;

  const lines = text.split("\n");
  for (let lineIdx = 0; lineIdx < lines.length; lineIdx++) {
    const line = lines[lineIdx];
    if (quote === null && /^\s*#/.test(line)) continue;
    for (let i = 0; i < line.length; i++) {
      const ch = line[i];
      if (quote === "'") {
        if (ch === "'") quote = null;
        else current += ch;
        continue;
      }
      if (quote === '"') {
        if (ch === '"') quote = null;
        else if (ch === "\\" && i + 1 < line.length) current += line[++i];
        else current += ch;
        continue;
      }
      if (ch === "'" || ch === '"') {
        quote = ch;
        hasToken = true;
        continue;
      }
      if (ch === "\\" && i + 1 < line.length) {
        current += line[++i];
        hasToken = true;
        continue;
      }
      if (/\s/.test(ch)) {
        if (hasToken) {
          args.push(current);
          current = "";
          hasToken = false;
        }
        continue;
      }
      current += ch;
      hasToken = true;
    }
    if (quote !== null && lineIdx < lines.length - 1) {
      current += "\n"; // keep the newline consumed by split() inside an open quote
    } else if (quote === null && hasToken) {
      args.push(current);
      current = "";
      hasToken = false;
    }
  }
  if (hasToken) args.push(current); // an unterminated quote is closed by end of input
  return args;
}

/** One arg per line, quoted when needed so that parseArgsText(argsToText(x)) round-trips. */
export function argsToText(args: readonly string[]): string {
  return args
    .map((arg) => {
      if (arg === "" || arg.startsWith("#") || /[\s'"\\]/.test(arg)) {
        return `"${arg.replace(/[\\"]/g, (c) => `\\${c}`)}"`;
      }
      return arg;
    })
    .join("\n");
}

export const normalizeArgsText = (text: string) => argsToText(parseArgsText(text));
