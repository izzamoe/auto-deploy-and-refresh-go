export class AdminAPIError extends Error {
  errors: string[];
  constructor(message: string, errors: string[] = []) {
    super(message);
    this.name = "AdminAPIError";
    this.errors = errors;
  }
}

export async function apiRequest(endpoint: string, options: RequestInit = {}) {
  const defaultHeaders: Record<string, string> = {
    "x-admin-request": "true",
    "x-requested-with": "AdminUI",
  };
  
  if (options.method && ["POST", "PUT", "PATCH", "DELETE"].includes(options.method)) {
    if (!options.body) {
      options.body = "{}";
    }
    defaultHeaders["Content-Type"] = "application/json";
  }

  const res = await fetch(`/admin/api${endpoint}`, {
    ...options,
    headers: { ...defaultHeaders, ...options.headers },
  });

  if (!res.ok) {
    const errorBody = await res.json().catch(() => ({}));
    throw new AdminAPIError(
      errorBody.error || errorBody.errors?.[0] || "API request failed",
      errorBody.errors || []
    );
  }
  return res.json();
}

export interface Account {
  username: string;
  mustChangePassword: boolean;
}

export function getAccount(): Promise<Account> {
  return apiRequest("/account");
}

export function changePassword(currentPassword: string, newPassword: string) {
  return apiRequest("/account/password", {
    method: "POST",
    body: JSON.stringify({ currentPassword, newPassword }),
  });
}

export function changeUsername(currentPassword: string, newUsername: string) {
  return apiRequest("/account/username", {
    method: "POST",
    body: JSON.stringify({ currentPassword, newUsername }),
  });
}

export function listAppReleases(id: string): Promise<{ releases: string[] }> {
  return apiRequest(`/apps/${id}/releases`);
}

export interface TelegramConfig {
  appId: number;
  appHash: string;
  botToken: string;
  chatUsername: string;
  enabled: boolean;
}

export async function getTelegramConfig(): Promise<TelegramConfig> {
  const res = await apiRequest("/telegram");
  return res.config;
}

export async function saveTelegramConfig(config: TelegramConfig): Promise<TelegramConfig> {
  const res = await apiRequest("/telegram", {
    method: "POST",
    body: JSON.stringify(config),
  });
  return res.config;
}

export interface GitHubConfig {
  token: string;
  hasToken: boolean;
  source: "database" | "environment" | "none";
}

export async function getGitHubConfig(): Promise<GitHubConfig> {
  const res = await apiRequest("/github");
  return res.config;
}

export async function saveGitHubConfig(token: string): Promise<GitHubConfig> {
  const res = await apiRequest("/github", {
    method: "POST",
    body: JSON.stringify({ token }),
  });
  return res.config;
}

export interface EnvVar {
  name: string;
  value: string;
}

export async function getAppEnv(id: string): Promise<EnvVar[]> {
  const res = await apiRequest(`/apps/${id}/env`);
  return res.envVars || [];
}

export async function saveAppEnv(id: string, envVars: EnvVar[]): Promise<EnvVar[]> {
  const res = await apiRequest(`/apps/${id}/env`, {
    method: "PUT",
    body: JSON.stringify({ envVars }),
  });
  return res.envVars || [];
}

// parseEnvText turns a "NAME=VALUE" per-line textarea value into EnvVar[],
// skipping blank lines and # comments. The value keeps any "=" after the first.
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

export function envVarsToText(vars: EnvVar[]): string {
  return vars.map((v) => `${v.name}=${v.value}`).join("\n");
}

// getJobLog returns the service logs captured when a deploy job completed
// (e.g. the health-check failure logs saved for a failed deploy).
export async function getJobLog(jobId: string): Promise<string> {
  const res = await apiRequest(`/jobs/${jobId}/logs`);
  return res.log || "";
}

// linesQuery builds the ?lines= query shared by the live log endpoints.
// lines <= 0 means "all" (the full available journal).
function linesQuery(lines: number): string {
  return `?lines=${lines > 0 ? lines : "all"}`;
}

// getAppLogs returns the current (live) systemd journal for an app's service.
// lines caps how many recent lines to fetch (<= 0 means all).
export async function getAppLogs(id: string, lines = 100): Promise<string> {
  const res = await apiRequest(`/apps/${id}/logs${linesQuery(lines)}`);
  return res.log || "";
}

// getSystemLogs returns auto-deploy's own systemd journal, for diagnosing the
// service itself. lines caps how many recent lines to fetch (<= 0 means all).
export async function getSystemLogs(lines = 100): Promise<string> {
  const res = await apiRequest(`/system/logs${linesQuery(lines)}`);
  return res.log || "";
}

export async function getServiceStatus(id: string): Promise<string> {
  const res = await apiRequest(`/apps/${id}/service/status`);
  return res.status || "unknown";
}

export type ServiceAction = "start" | "stop" | "restart";

export async function controlService(id: string, action: ServiceAction): Promise<{ status: string; message: string }> {
  const res = await apiRequest(`/apps/${id}/service/${action}`, { method: "POST" });
  return { status: res.status || "unknown", message: res.message || "" };
}
