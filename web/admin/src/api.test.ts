import { describe, expect, it } from "vitest";
import { argsToText, parseArgsText } from "./api";

describe("parseArgsText", () => {
	it("splits on spaces", () => {
		expect(parseArgsText("--port 8080")).toEqual(["--port", "8080"]);
	});

	it("splits on newlines", () => {
		expect(parseArgsText("--port\n8080")).toEqual(["--port", "8080"]);
	});

	it("splits on mixed whitespace (spaces, tabs, newlines)", () => {
		expect(parseArgsText("--port\t8080  --verbose\n--log-level=info")).toEqual([
			"--port",
			"8080",
			"--verbose",
			"--log-level=info",
		]);
	});

	it("keeps double-quoted values with spaces together", () => {
		expect(parseArgsText('--msg "hello world"')).toEqual(["--msg", "hello world"]);
	});

	it("keeps single-quoted values with spaces together", () => {
		expect(parseArgsText("--msg 'hello world'")).toEqual(["--msg", "hello world"]);
	});

	it("strips the quote characters themselves", () => {
		expect(parseArgsText('"--port" \'8080\'')).toEqual(["--port", "8080"]);
	});

	it("handles escaped characters outside quotes", () => {
		expect(parseArgsText("--msg hello\\ world")).toEqual(["--msg", "hello world"]);
		expect(parseArgsText("--path C:\\\\Users")).toEqual(["--path", "C:\\Users"]);
	});

	it("handles escaped characters inside double quotes", () => {
		expect(parseArgsText('--re "a\\"b"')).toEqual(["--re", 'a"b']);
	});

	it("does not interpret backslashes inside single quotes", () => {
		expect(parseArgsText("--path 'C:\\Users'")).toEqual(["--path", "C:\\Users"]);
	});

	it("skips comment lines whose first non-space char is #", () => {
		expect(parseArgsText("--port 8080\n# a comment\n  # indented comment\n--verbose")).toEqual([
			"--port",
			"8080",
			"--verbose",
		]);
	});

	it("produces nothing for blank lines", () => {
		expect(parseArgsText("--port 8080\n\n\n--verbose")).toEqual(["--port", "8080", "--verbose"]);
		expect(parseArgsText("")).toEqual([]);
		expect(parseArgsText("\n\n")).toEqual([]);
	});

	it("preserves an empty quoted token", () => {
		expect(parseArgsText("--empty '' --also-empty \"\"")).toEqual(["--empty", "", "--also-empty", ""]);
	});

	it("tolerates an unterminated quote at end of input", () => {
		expect(parseArgsText('--msg "unterminated')).toEqual(["--msg", "unterminated"]);
		expect(parseArgsText("--msg 'unterminated")).toEqual(["--msg", "unterminated"]);
	});
});

describe("argsToText", () => {
	it("renders one arg per line", () => {
		expect(argsToText(["--port", "8080"])).toBe("--port\n8080");
	});

	it("quotes an arg containing whitespace", () => {
		expect(argsToText(["hello world"])).toBe('"hello world"');
	});

	it("quotes an empty arg", () => {
		expect(argsToText([""])).toBe('""');
	});

	it("escapes quotes and backslashes inside a quoted arg", () => {
		expect(argsToText(['a"b'])).toBe('"a\\"b"');
		expect(argsToText(["a\\b"])).toBe('"a\\\\b"');
	});
});

describe("parseArgsText/argsToText round-trip", () => {
	it("round-trips a tricky array of args", () => {
		const original = ["--msg", "hello world", "--re", 'a"b', "--empty", "", "--back\\slash"];
		expect(parseArgsText(argsToText(original))).toEqual(original);
	});

	it("round-trips assorted single-value cases", () => {
		const cases = [
			["--flag"],
			["value with spaces"],
			["tab\tvalue"],
			["newline\nvalue"],
			["quote'here"],
			['double"quote'],
			["back\\slash"],
			[""],
			["a", "b", "c"],
			// An arg that starts with "#" must survive: written bare it would be
			// re-read as a comment line and silently dropped.
			["#tag"],
			["--label", "#1"],
			["  leading space"],
		];
		for (const original of cases) {
			expect(parseArgsText(argsToText(original))).toEqual(original);
		}
	});
});
