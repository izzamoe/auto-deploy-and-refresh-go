import { describe, expect, it } from "vitest";
import { argsToText, firstInvalidEnvName, normalizeArgsText, normalizeEnvText, parseArgsText, parseEnvText, envVarsToText } from "./envargs";

describe("env text", () => {
  it("parses NAME=VALUE lines, skipping blanks and comments, keeping later '='", () => {
    expect(parseEnvText("A=1\n\n# note\nB=x=y\nC")).toEqual([
      { name: "A", value: "1" },
      { name: "B", value: "x=y" },
      { name: "C", value: "" },
    ]);
  });
  it("round-trips and normalizes so an untouched editor compares equal", () => {
    expect(envVarsToText(parseEnvText("A=1\nB=2"))).toBe("A=1\nB=2");
    expect(normalizeEnvText("A=1\n\n\n# c\nB=2\n")).toBe(normalizeEnvText("A=1\nB=2"));
  });
  it("finds a name the server would reject", () => {
    expect(firstInvalidEnvName([{ name: "OK_1", value: "" }])).toBeUndefined();
    expect(firstInvalidEnvName([{ name: "1BAD", value: "" }, { name: "A B", value: "" }])).toBe("1BAD");
  });
});

describe("args text", () => {
  it("splits like a shell", () => {
    expect(parseArgsText('--port 8080 --msg "hello world"\n--x=\'a b\'')).toEqual(["--port", "8080", "--msg", "hello world", "--x=a b"]);
    expect(parseArgsText("# comment\n--a")).toEqual(["--a"]);
  });
  it("round-trips awkward values", () => {
    const args = ["--a", "hello world", "#not-a-comment", "", 'q"uote', "back\\slash"];
    expect(parseArgsText(argsToText(args))).toEqual(args);
    expect(normalizeArgsText("--a   --b")).toBe("--a\n--b");
  });
});
