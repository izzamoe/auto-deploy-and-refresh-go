// @vitest-environment jsdom
// T084: the corrected behaviour for B50, B56, B58, K2 (the T078 repro tests asserted the bugs).
import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createQueryClient, queryKeys, shouldRetry } from "@/lib/query";
import { translate, translateError } from "@/i18n";
import { ApiError, api, apiFetch, describeError, onAuthEvent, resetAuthStateForTests, sanitizeNext } from "./client";

type Res = { ok: boolean; status: number; json: () => Promise<unknown> };
const json = (status: number, body: unknown): Res => ({ ok: status >= 200 && status < 300, status, json: async () => body });
const errorBody = (code: string, extra: object = {}) => ({ status: "error", code, error: "english text", errors: [], ...extra });

function stubLocation(pathname: string, search = "") {
  const assign = vi.fn();
  vi.stubGlobal("location", { ...window.location, pathname, search, hash: "", assign, replace: vi.fn() });
  return assign;
}

const i18n = (locale: "id" | "en") => ({
  t: (key: "error.network" | "error.notJson" | "error.unknown") => translate(locale, key),
  errorMessage: (code: string | undefined, status?: number) => translateError(locale, code, status),
});

beforeEach(() => resetAuthStateForTests());
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("401 handling (B50, K2, FR-070)", () => {
  it("redirects to /admin/login/?next=<current path> exactly once, however many calls fail", async () => {
    const assign = stubLocation("/admin/apps/edit/", "?id=a1");
    vi.stubGlobal("fetch", vi.fn(async () => json(401, errorBody("session_expired"))));
    const expired = vi.fn();
    onAuthEvent("session_expired", expired);

    const results = await Promise.allSettled([api.getSession(), api.listApps(), api.listHistory({}), api.getStats()]);

    expect(results.every((r) => r.status === "rejected")).toBe(true);
    expect(assign).toHaveBeenCalledTimes(1);
    expect(assign).toHaveBeenCalledWith("/admin/login/?next=" + encodeURIComponent("/admin/apps/edit/?id=a1"));
    const err = (results[0] as PromiseRejectedResult).reason as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(401);
    expect(err.code).toBe("session_expired");
    expect(expired).toHaveBeenCalled();
  });

  it("does not redirect from the login page itself (no loop)", async () => {
    const assign = stubLocation("/admin/login/");
    vi.stubGlobal("fetch", vi.fn(async () => json(401, errorBody("session_expired"))));
    await expect(api.getSession()).rejects.toBeInstanceOf(ApiError);
    expect(assign).not.toHaveBeenCalled();
  });

  it("a wrong password on the login call is an error, not a session expiry", async () => {
    const assign = stubLocation("/admin/apps/");
    vi.stubGlobal("fetch", vi.fn(async () => json(401, errorBody("unauthorized"))));
    await expect(api.login({ username: "a", password: "b" })).rejects.toMatchObject({ status: 401, code: "unauthorized" });
    expect(assign).not.toHaveBeenCalled();
  });
});

describe("403 password_change_required (FR-070)", () => {
  it("emits the auth event and does not redirect", async () => {
    const assign = stubLocation("/admin/");
    vi.stubGlobal("fetch", vi.fn(async () => json(403, errorBody("password_change_required"))));
    const seen = vi.fn();
    const off = onAuthEvent("password_change_required", seen);
    await expect(api.listApps()).rejects.toMatchObject({ status: 403, code: "password_change_required" });
    expect(seen).toHaveBeenCalledTimes(1);
    expect(assign).not.toHaveBeenCalled();
    off();
  });

  it("other 403 codes do not trigger it", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json(403, errorBody("csrf_rejected"))));
    const seen = vi.fn();
    onAuthEvent("password_change_required", seen);
    await expect(api.deleteApp("x")).rejects.toMatchObject({ code: "csrf_rejected" });
    expect(seen).not.toHaveBeenCalled();
  });
});

describe("requests (FR-011)", () => {
  it("sends X-Admin-Request and a JSON body on mutations, nothing special on reads", async () => {
    const fetchMock = vi.fn(async () => json(200, { apps: [] }));
    vi.stubGlobal("fetch", fetchMock);
    await api.listApps();
    await api.cancelAppJobs("a/1");
    const [readUrl, readInit] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    const [writeUrl, writeInit] = fetchMock.mock.calls[1] as unknown as [string, RequestInit];
    expect(readUrl).toBe("/admin/api/v1/apps");
    expect(readInit.method).toBe("GET");
    expect((readInit.headers as Record<string, string>)["X-Admin-Request"]).toBeUndefined();
    expect(writeUrl).toBe("/admin/api/v1/apps/a%2F1/cancel");
    expect(writeInit.method).toBe("POST");
    expect(writeInit.body).toBe("{}");
    expect(writeInit.headers).toMatchObject({ "X-Admin-Request": "true", "Content-Type": "application/json" });
    expect(readInit.credentials).toBe("same-origin");
  });

  it("builds query strings and skips undefined values", async () => {
    const fetchMock = vi.fn(async (_url: string) => json(200, { history: [], total: 0 }));
    vi.stubGlobal("fetch", fetchMock);
    await api.listHistory({ appId: "a1", page: 2, pageSize: undefined });
    expect(fetchMock.mock.calls[0][0]).toBe("/admin/api/v1/history?appId=a1&page=2");
  });

  it("returns undefined for 204", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json(204, undefined)));
    await expect(api.logout()).resolves.toBeUndefined();
  });
});

describe("failures are typed and localized (B58, FR-079)", () => {
  it("a network failure ('Failed to fetch') becomes a localized network error", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("Failed to fetch"))));
    const err = await api.listApps().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).kind).toBe("network");
    expect(describeError(err, i18n("id"))).toBe(translate("id", "error.network"));
    expect(describeError(err, i18n("en"))).toBe(translate("en", "error.network"));
  });

  it("an HTML error page (Cloudflare 502) becomes a localized non-JSON error, not the generic text", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 502, json: async () => { throw new SyntaxError("Unexpected token <"); } })));
    const err = await api.listApps().catch((e: unknown) => e);
    expect((err as ApiError).kind).toBe("not_json");
    expect((err as ApiError).status).toBe(502);
    expect(describeError(err, i18n("en"))).toBe(translate("en", "error.notJson"));
  });

  it("a 200 whose body is not JSON is an error too", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => { throw new SyntaxError("x"); } })));
    await expect(api.listApps()).rejects.toMatchObject({ kind: "not_json" });
  });

  it("API errors are shown by code (localized), never by the raw English server text", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json(429, errorBody("rate_limited", { requestId: "req-1" }))));
    const err = await api.createApp({} as never).catch((e: unknown) => e);
    expect(describeError(err, i18n("id"))).toBe(translate("id", "error.rate_limited"));
    expect(describeError(err, i18n("id"))).not.toContain("english text");
    expect((err as ApiError).requestId).toBe("req-1");
  });

  it("keeps field errors for the forms", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json(400, errorBody("validation_failed", { errors: [{ field: "binaryPath", code: "invalid", message: "m" }] }))));
    const err = (await api.createApp({} as never).catch((e: unknown) => e)) as ApiError;
    expect(err.fieldErrors).toEqual([{ field: "binaryPath", code: "invalid", message: "m" }]);
  });

  it("describeError returns an empty string for aborts and a generic text for unknown errors", () => {
    expect(describeError(new DOMException("x", "AbortError"), i18n("en"))).toBe("");
    expect(describeError(new Error("boom"), i18n("en"))).toBe(translate("en", "error.unknown"));
  });
});

describe("abort and stale responses (B56, FR-077)", () => {
  it("forwards the AbortSignal and rethrows the abort untouched", async () => {
    const controller = new AbortController();
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: string, init: RequestInit) => new Promise((_resolve, reject) => {
        init.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      })),
    );
    const pending = apiFetch("/apps", { signal: controller.signal });
    controller.abort();
    const err = await pending.catch((e: unknown) => e);
    expect(err).toBeInstanceOf(DOMException);
    expect((err as DOMException).name).toBe("AbortError");
  });

  it("when the key changes, the previous request is aborted and a late old response never wins", async () => {
    const calls: { signal: AbortSignal | null | undefined; resolve: (r: Res) => void }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: string, init: RequestInit) => new Promise<Res>((resolve, reject) => {
        calls.push({ signal: init.signal, resolve });
        init.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      })),
    );
    const client = createQueryClient();
    const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children);
    const { result, rerender } = renderHook(
      ({ appId }: { appId: string | undefined }) =>
        useQuery({
          queryKey: queryKeys.historyPage(appId, 1, 20),
          queryFn: ({ signal }) => api.listHistory({ appId, page: 1, pageSize: 20 }, { signal }),
        }),
      { wrapper, initialProps: { appId: undefined as string | undefined } },
    );
    await waitFor(() => expect(calls).toHaveLength(1));
    rerender({ appId: "a2" });
    await waitFor(() => expect(calls).toHaveLength(2));

    expect(calls[0].signal?.aborted).toBe(true); // the old request was cancelled
    calls[1].resolve(json(200, { history: [{ id: "NEW" }], total: 1 }));
    await waitFor(() => expect(result.current.data).toMatchObject({ history: [{ id: "NEW" }] }));
    calls[0].resolve(json(200, { history: [{ id: "OLD" }], total: 1 })); // too late, already aborted
    await new Promise((r) => setTimeout(r, 20));
    expect(result.current.data).toMatchObject({ history: [{ id: "NEW" }] });
  });
});

describe("retry policy and next sanitising", () => {
  it("does not retry client errors or aborts, retries network/5xx twice", () => {
    expect(shouldRetry(0, new ApiError({ kind: "http", status: 404, code: "not_found" }))).toBe(false);
    expect(shouldRetry(0, new DOMException("x", "AbortError"))).toBe(false);
    expect(shouldRetry(0, new ApiError({ kind: "network", status: 0 }))).toBe(true);
    expect(shouldRetry(1, new ApiError({ kind: "http", status: 502 }))).toBe(true);
    expect(shouldRetry(2, new ApiError({ kind: "network", status: 0 }))).toBe(false);
  });

  it("sanitizeNext only accepts absolute paths under /admin/", () => {
    expect(sanitizeNext("/admin/apps/edit/?id=a1")).toBe("/admin/apps/edit/?id=a1");
    for (const bad of ["https://evil.example/", "//evil.example/admin/", "/other/", "/admin/..\\x", "/admin/../evil", "/admin/%2e%2e/evil", "/admin/a/./b", "/admin/%zz", "javascript:alert(1)", "", null, undefined, "/admin/login/?next=/admin/login/"]) {
      expect(sanitizeNext(bad as string | null | undefined)).toBe("/admin/");
    }
  });
});
