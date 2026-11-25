// Typed client of the admin API v1 (FR-070, FR-072, FR-079, B50, B56, B58, K2).
//
// Types come from schema.gen.ts (generated from api/admin-api.v1.openapi.yaml). One
// `apiFetch` handles, for every call:
//   - CSRF (FR-011): `X-Admin-Request: true` + a JSON body on every mutation;
//   - 401 -> ONE redirect to /admin/login/?next=<current path> (no loop, no popup);
//   - 403 password_change_required -> an auth event the shell turns into the forced-change screen;
//   - AbortSignal forwarding (TanStack Query aborts stale requests, B56);
//   - network failures and non-JSON bodies (Cloudflare 52x HTML) -> typed ApiError (B58).
import type { components, operations } from "./schema.gen";

export const API_BASE = "/admin/api/v1";
export const LOGIN_PATH = "/admin/login/";

export type Schemas = components["schemas"];
type OpName = keyof operations;

type Json<R> = R extends { content: { "application/json": infer J } } ? J : never;
type OkKeys<Op extends OpName> = Extract<keyof operations[Op]["responses"], 200 | 201 | 202>;
/** JSON body of the success response (200/201/202) of an operation. */
export type Ok<Op extends OpName> = Json<operations[Op]["responses"][OkKeys<Op>]>;
/** JSON request body of an operation. */
export type Body<Op extends OpName> = operations[Op] extends { requestBody: infer R }
  ? Json<NonNullable<R>>
  : never;

export type ErrorKind = "http" | "network" | "not_json";

export class ApiError extends Error {
  readonly kind: ErrorKind;
  readonly status: number;
  /** OpenAPI ErrorCode (or an unknown server code); undefined for network/non-JSON errors. */
  readonly code?: string;
  readonly fieldErrors: Schemas["FieldError"][];
  readonly requestId?: string;
  /** Whole seconds from the `Retry-After` header (429), when present and valid. */
  readonly retryAfterSeconds?: number;

  constructor(init: {
    kind: ErrorKind;
    status: number;
    code?: string;
    message?: string;
    fieldErrors?: Schemas["FieldError"][];
    requestId?: string;
    retryAfterSeconds?: number;
  }) {
    super(init.message ?? init.code ?? init.kind);
    this.name = "ApiError";
    this.kind = init.kind;
    this.status = init.status;
    this.code = init.code;
    this.fieldErrors = init.fieldErrors ?? [];
    this.requestId = init.requestId;
    this.retryAfterSeconds = init.retryAfterSeconds;
  }
}

export function isAbortError(err: unknown): boolean {
  return err instanceof DOMException ? err.name === "AbortError" : (err as { name?: string })?.name === "AbortError";
}

// ---- auth events ---------------------------------------------------------------------

export type AuthEvent = "password_change_required" | "session_expired";
const listeners: Record<AuthEvent, Set<() => void>> = {
  password_change_required: new Set(),
  session_expired: new Set(),
};

/** Subscribes to an auth event; returns the unsubscribe function. */
export function onAuthEvent(event: AuthEvent, listener: () => void): () => void {
  listeners[event].add(listener);
  return () => listeners[event].delete(listener);
}

function emit(event: AuthEvent) {
  for (const l of [...listeners[event]]) l();
}

let redirecting = false;

/** Test hook: the redirect guard is module state. */
export function resetAuthStateForTests() {
  redirecting = false;
}

// `next` must stay inside the admin: a same-origin absolute path under /admin/, never a
// scheme, protocol-relative URL or backslash trick (open redirect).
export function sanitizeNext(raw: string | null | undefined, fallback = "/admin/"): string {
  if (!raw || !raw.startsWith("/admin/")) return fallback;
  if (/[\\\u0000-\u001f]/.test(raw) || raw.startsWith("//")) return fallback;
  if (raw.startsWith(LOGIN_PATH)) return fallback;
  // `/admin/../evil` is "under /admin/" as text but the browser resolves it to /evil.
  let decoded = raw;
  try {
    decoded = decodeURIComponent(raw);
  } catch {
    return fallback;
  }
  if (decoded.split(/[?#]/)[0].split("/").some((seg) => seg === ".." || seg === ".")) return fallback;
  return raw;
}

function redirectToLogin() {
  if (redirecting || window.location.pathname.startsWith(LOGIN_PATH)) return;
  redirecting = true;
  const next = window.location.pathname + window.location.search;
  window.location.assign(`${LOGIN_PATH}?next=${encodeURIComponent(next)}`);
}

// ---- transport -----------------------------------------------------------------------

export interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  query?: Record<string, string | number | boolean | undefined | null>;
  signal?: AbortSignal;
  /** false for the login call: a 401 there is a wrong password, not an expired session. */
  redirectOn401?: boolean;
}

function buildUrl(path: string, query: RequestOptions["query"]): string {
  const url = `${API_BASE}${path}`;
  if (!query) return url;
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== null) qs.set(k, String(v));
  const text = qs.toString();
  return text ? `${url}?${text}` : url;
}

/** `Retry-After` in delta-seconds form; an HTTP-date or garbage yields undefined. */
export function parseRetryAfter(raw: string | null | undefined): number | undefined {
  if (!raw || !/^\d+$/.test(raw.trim())) return undefined;
  const n = Number(raw.trim());
  return n > 0 ? n : undefined;
}

function isErrorBody(b: unknown): b is { code?: string; error?: string; errors?: Schemas["FieldError"][]; requestId?: string } {
  return typeof b === "object" && b !== null && !Array.isArray(b);
}

export async function apiFetch<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? "GET";
  const mutating = method !== "GET";
  const headers: Record<string, string> = { Accept: "application/json" };
  let body: string | undefined;
  if (mutating) {
    headers["X-Admin-Request"] = "true";
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(options.body ?? {});
  }

  let res: Response;
  try {
    res = await fetch(buildUrl(path, options.query), {
      method,
      headers,
      body,
      credentials: "same-origin",
      signal: options.signal,
    });
  } catch (err) {
    if (options.signal?.aborted || isAbortError(err)) throw err;
    throw new ApiError({ kind: "network", status: 0, message: err instanceof Error ? err.message : "network error" });
  }

  if (res.status === 204) return undefined as T;

  let parsed: unknown;
  let parseFailed = false;
  try {
    parsed = await res.json();
  } catch {
    parseFailed = true;
  }

  if (!res.ok) {
    const b = isErrorBody(parsed) ? parsed : undefined;
    const err = new ApiError({
      kind: b ? "http" : "not_json",
      status: res.status,
      code: typeof b?.code === "string" ? b.code : undefined,
      message: typeof b?.error === "string" ? b.error : undefined,
      fieldErrors: Array.isArray(b?.errors) ? b.errors : [],
      requestId: typeof b?.requestId === "string" ? b.requestId : undefined,
      retryAfterSeconds: parseRetryAfter(res.headers?.get?.("Retry-After")),
    });
    if (res.status === 401 && options.redirectOn401 !== false) {
      emit("session_expired");
      redirectToLogin();
    } else if (res.status === 403 && err.code === "password_change_required") {
      emit("password_change_required");
    }
    throw err;
  }

  if (parseFailed) throw new ApiError({ kind: "not_json", status: res.status });
  return parsed as T;
}

// ---- localized message (B58, FR-079) -----------------------------------------------

export interface Messages {
  t: (key: "error.network" | "error.notJson" | "error.unknown") => string;
  errorMessage: (code: string | undefined, status?: number) => string;
}

/** Text for the user: localized by `code`, never the raw server string. Empty for aborts. */
export function describeError(err: unknown, i18n: Messages): string {
  if (isAbortError(err)) return "";
  if (err instanceof ApiError) {
    if (err.kind === "network") return i18n.t("error.network");
    if (err.kind === "not_json") return i18n.t("error.notJson");
    return i18n.errorMessage(err.code, err.status);
  }
  return i18n.t("error.unknown");
}

// ---- typed endpoints ---------------------------------------------------------------

const enc = encodeURIComponent;
type Sig = { signal?: AbortSignal };

export const api = {
  getSession: (o?: Sig) => apiFetch<Ok<"getSession">>("/session", o),
  login: (body: Body<"createSession">, o?: Sig) =>
    apiFetch<Ok<"createSession">>("/session", { method: "POST", body, redirectOn401: false, ...o }),
  logout: (o?: Sig) => apiFetch<void>("/session", { method: "DELETE", ...o }),

  getAccount: (o?: Sig) => apiFetch<Ok<"getAccount">>("/account", o),
  changePassword: (body: Body<"changePassword">) => apiFetch<Ok<"changePassword">>("/account/password", { method: "POST", body }),
  changeUsername: (body: Body<"changeUsername">) => apiFetch<Ok<"changeUsername">>("/account/username", { method: "POST", body }),
  revokeAllSessions: () => apiFetch<Ok<"revokeAllSessions">>("/account/sessions/revoke-all", { method: "POST" }),

  listTokens: (o?: Sig) => apiFetch<Ok<"listApiTokens">>("/tokens", o),
  createToken: (body: Body<"createApiToken">) => apiFetch<Ok<"createApiToken">>("/tokens", { method: "POST", body }),
  revokeToken: (tokenId: string) => apiFetch<void>(`/tokens/${enc(tokenId)}`, { method: "DELETE" }),

  listApps: (o?: Sig) => apiFetch<Ok<"listApps">>("/apps", o),
  getApp: (appId: string, o?: Sig) => apiFetch<Ok<"getApp">>(`/apps/${enc(appId)}`, o),
  createApp: (body: Body<"createApp">) => apiFetch<Ok<"createApp">>("/apps", { method: "POST", body }),
  updateApp: (appId: string, body: Body<"updateApp">) => apiFetch<Ok<"updateApp">>(`/apps/${enc(appId)}`, { method: "PUT", body }),
  deleteApp: (appId: string, removeUnitFile = false) =>
    apiFetch<Ok<"deleteApp">>(`/apps/${enc(appId)}`, { method: "DELETE", query: removeUnitFile ? { removeUnitFile: true } : undefined }),
  toggleApp: (appId: string, enabled: boolean) => apiFetch<Ok<"toggleApp">>(`/apps/${enc(appId)}/toggle`, { method: "POST", body: { enabled } }),
  deployApp: (appId: string, body: Body<"deployApp">) => apiFetch<Ok<"deployApp">>(`/apps/${enc(appId)}/deploy`, { method: "POST", body }),
  cancelAppJobs: (appId: string) => apiFetch<Ok<"cancelAppJobs">>(`/apps/${enc(appId)}/cancel`, { method: "POST" }),
  rotateWebhookSecret: (appId: string) => apiFetch<Ok<"rotateWebhookSecret">>(`/apps/${enc(appId)}/webhook-secret/rotate`, { method: "POST" }),

  getAppEnv: (appId: string, o?: Sig) => apiFetch<Ok<"getAppEnv">>(`/apps/${enc(appId)}/env`, o),
  saveAppEnv: (appId: string, body: Body<"saveAppEnv">) => apiFetch<Ok<"saveAppEnv">>(`/apps/${enc(appId)}/env`, { method: "PUT", body }),
  getAppArgs: (appId: string, o?: Sig) => apiFetch<Ok<"getAppArgs">>(`/apps/${enc(appId)}/args`, o),
  saveAppArgs: (appId: string, body: Body<"saveAppArgs">) => apiFetch<Ok<"saveAppArgs">>(`/apps/${enc(appId)}/args`, { method: "PUT", body }),
  listReleases: (appId: string, o?: Sig) => apiFetch<Ok<"listAppReleases">>(`/apps/${enc(appId)}/releases`, o),

  previewServiceUnit: (appId: string, o?: Sig) => apiFetch<Ok<"previewServiceUnit">>(`/apps/${enc(appId)}/service-unit/preview`, o),
  applyServiceUnit: (appId: string) => apiFetch<Ok<"applyServiceUnit">>(`/apps/${enc(appId)}/service-unit/apply`, { method: "POST" }),
  getAppLogs: (appId: string, lines?: number, o?: Sig) => apiFetch<Ok<"getAppLogs">>(`/apps/${enc(appId)}/logs`, { query: { lines }, ...o }),
  getServiceStatus: (appId: string, o?: Sig) => apiFetch<Ok<"getServiceStatus">>(`/apps/${enc(appId)}/service/status`, o),
  controlService: (appId: string, action: "start" | "stop" | "restart") =>
    apiFetch<Ok<"controlService">>(`/apps/${enc(appId)}/service/${action}`, { method: "POST" }),

  listHistory: (query: { appId?: string; page?: number; pageSize?: number }, o?: Sig) => apiFetch<Ok<"listHistory">>("/history", { query, ...o }),
  retryJob: (jobId: string) => apiFetch<Ok<"retryJob">>(`/history/${enc(jobId)}/retry`, { method: "POST" }),
  cancelJob: (jobId: string) => apiFetch<Ok<"cancelJob">>(`/jobs/${enc(jobId)}/cancel`, { method: "POST" }),
  getJobLogs: (jobId: string, o?: Sig) => apiFetch<Ok<"getJobLogs">>(`/jobs/${enc(jobId)}/logs`, o),

  getGitHub: (o?: Sig) => apiFetch<Ok<"getGitHubConfig">>("/github", o),
  saveGitHub: (body: Body<"saveGitHubConfig">) => apiFetch<Ok<"saveGitHubConfig">>("/github", { method: "POST", body }),
  getTelegram: (o?: Sig) => apiFetch<Ok<"getTelegramConfig">>("/telegram", o),
  saveTelegram: (body: Body<"saveTelegramConfig">) => apiFetch<Ok<"saveTelegramConfig">>("/telegram", { method: "POST", body }),
  getSystemLogs: (lines?: number, o?: Sig) => apiFetch<Ok<"getSystemLogs">>("/system/logs", { query: { lines }, ...o }),
  getStats: (query: { range?: "7d" | "30d"; tz?: string } = {}, o?: Sig) => apiFetch<Ok<"getStats">>("/stats", { query, ...o }),
};
