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
