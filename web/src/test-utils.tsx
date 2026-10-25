// Renders the app at a path against a fake API.
import { render } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { vi } from "vitest";
import { AppRoutes, makeClient } from "./App";
import { SessionProvider } from "./api/session";

export type Reply = { status?: number; body?: unknown };
export type Routes = Record<string, Reply | ((init: RequestInit | undefined) => Reply)>;
export type Call = { method: string; path: string; body?: unknown; headers: Record<string, string> };

// fakeAPI answers "METHOD /path" (path after /api/v1, with its query) and
// records every call.
export function fakeAPI(routes: Routes) {
  const calls: Call[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input).replace(/^\/api\/v1/, "");
    const method = init?.method ?? "GET";
    const headers = (init?.headers ?? {}) as Record<string, string>;
    calls.push({ method, path: url, body: init?.body ? JSON.parse(String(init.body)) : undefined, headers });
    const r = routes[`${method} ${url}`] ?? routes[`${method} ${url.split("?")[0]}`];
    const reply = typeof r === "function" ? r(init) : r;
    if (!reply) return new Response(JSON.stringify({ error: "not found" }), { status: 404 });
    const status = reply.status ?? 200;
    return new Response(status === 204 ? null : JSON.stringify(reply.body ?? {}), { status });
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

export const ME = { id: "u1", username: "ayse.k", session: "s1", sessionKind: "ui", teams: ["team-platform"], roles: ["viewer", "operator"] };

export function renderAt(path: string) {
  const client = makeClient();
  client.setDefaultOptions({ queries: { retry: false } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <SessionProvider>
          <AppRoutes />
        </SessionProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
