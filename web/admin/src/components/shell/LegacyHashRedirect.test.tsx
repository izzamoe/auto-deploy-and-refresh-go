// @vitest-environment jsdom
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LegacyHashRedirect, mapLegacyHash } from "./LegacyHashRedirect";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("mapLegacyHash (R9)", () => {
  it.each([
    ["#/apps/x/edit", "/admin/apps/edit/?id=x"],
    ["#/apps/my-app_2/edit", "/admin/apps/edit/?id=my-app_2"],
    ["#/apps/new", "/admin/apps/new/"],
    ["#/apps", "/admin/apps/"],
    ["#/apps/x", "/admin/apps/"],
    ["#/history", "/admin/history/"],
    ["#/history?appId=a1", "/admin/history/?appId=a1"],
    ["#/account", "/admin/account/"],
    ["#/telegram", "/admin/settings/telegram/"],
    ["#/github", "/admin/settings/github/"],
    ["#/system-logs", "/admin/logs/"],
    ["#/", "/admin/"],
    ["#/something-else", "/admin/"],
  ])("%s -> %s", (hash, want) => {
    expect(mapLegacyHash(hash)).toBe(want);
  });

  it("leaves non-legacy hashes alone", () => {
    for (const hash of ["", "#", "#main-content", "#section-2"]) expect(mapLegacyHash(hash)).toBeNull();
  });

  it("never lets a crafted id or appId through (no query/path injection)", () => {
    expect(mapLegacyHash("#/apps/..%2F..%2Fx/edit")).toBe("/admin/apps/");
    expect(mapLegacyHash("#/apps/a?b=c/edit")).toBe("/admin/apps/");
    expect(mapLegacyHash("#/apps/%3Cscript%3E/edit")).toBe("/admin/apps/");
    expect(mapLegacyHash("#/history?appId=a1%26x%3D1")).toBe("/admin/history/");
    expect(mapLegacyHash("#/history?appId=https://evil.example")).toBe("/admin/history/");
  });
});

describe("LegacyHashRedirect", () => {
  it("replaces the location once for a legacy hash", () => {
    const replace = vi.fn();
    vi.stubGlobal("location", { ...window.location, hash: "#/apps/x/edit", replace, assign: vi.fn() });
    render(<LegacyHashRedirect />);
    expect(replace).toHaveBeenCalledTimes(1);
    expect(replace).toHaveBeenCalledWith("/admin/apps/edit/?id=x");
  });

  it("does nothing without a legacy hash", () => {
    const replace = vi.fn();
    vi.stubGlobal("location", { ...window.location, hash: "", replace, assign: vi.fn() });
    render(<LegacyHashRedirect />);
    expect(replace).not.toHaveBeenCalled();
  });
});

// B59 / FR-080: the old ?flash= message channel is gone for good; notices come from state only.
describe("no ?flash= channel (B59, FR-080)", () => {
  it("no shell source reads the flash query parameter", () => {
    const offenders: string[] = [];
    for (const name of readdirSync(__dirname)) {
      if (!/\.(ts|tsx)$/.test(name) || /\.test\.tsx?$/.test(name)) continue;
      const text = readFileSync(join(__dirname, name), "utf8");
      if (/flash/i.test(text) || /searchParams|location\.search/.test(text)) offenders.push(name);
    }
    expect(offenders).toEqual([]);
  });

  it("a ?flash= query renders nothing", () => {
    vi.stubGlobal("location", { ...window.location, search: "?flash=Sesi+habis+di+evil.example", hash: "", replace: vi.fn(), assign: vi.fn() });
    const { container } = render(<LegacyHashRedirect />);
    expect(container).toBeEmptyDOMElement();
    expect(document.body.textContent).not.toContain("evil.example");
  });
});
