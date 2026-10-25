// The API client. Every request is same-origin and carries the session
// cookie; changes carry X-Gaia-Request, Gaia's CSRF guard.

export class ApiError extends Error {
  status: number;
  stepUp: boolean;
  constructor(status: number, message: string, stepUp = false) {
    super(message);
    this.status = status;
    this.stepUp = stepUp;
  }
}

type Options = { method?: "GET" | "POST" | "PUT" | "DELETE"; body?: unknown; signal?: AbortSignal };

export async function api<T>(path: string, opts: Options = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json" };
  if (method !== "GET") {
    headers["X-Gaia-Request"] = "1";
    // Gaia compares this literally, so no charset.
    headers["Content-Type"] = "application/json";
  }
  const res = await fetch("/api/v1" + path, {
    method,
    headers,
    credentials: "same-origin",
    body: method === "GET" ? undefined : JSON.stringify(opts.body ?? {}),
    signal: opts.signal,
  });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { error: text.trim() };
    }
  }
  if (!res.ok) {
    const d = (data ?? {}) as { error?: string; stepUp?: boolean };
    throw new ApiError(res.status, d.error ?? res.statusText, d.stepUp === true);
  }
  return data as T;
}

// Where the browser goes to sign in; the page sends it back to next.
export function loginURL(next = window.location.pathname + window.location.search): string {
  return "/login?next=" + encodeURIComponent(next);
}
