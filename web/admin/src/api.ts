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
