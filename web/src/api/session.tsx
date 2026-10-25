// Who is signed in. A server without an AuthConfig is open: it has no
// /me, and the UI then runs without a person.
import { createContext, useContext, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, ApiError, loginURL } from "./client";
import type { Me, Role } from "./types";

export type Session = { me: Me | null; open: boolean };

const Ctx = createContext<Session>({ me: null, open: true });

export async function loadSession(): Promise<Session> {
  try {
    return { me: await api<Me>("/me"), open: false };
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return { me: null, open: true };
    throw e;
  }
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const q = useQuery({ queryKey: ["me"], queryFn: loadSession, staleTime: 60_000 });
  if (q.error instanceof ApiError && q.error.status === 401) {
    window.location.assign(loginURL());
    return null;
  }
  if (q.isPending) return <div className="empty" role="status">Yükleniyor…</div>;
  if (q.error) return <div className="page"><div className="notice err" role="alert">Gaia'ya ulaşılamadı: {q.error.message}</div></div>;
  return <Ctx.Provider value={q.data}>{children}</Ctx.Provider>;
}

export function useSession(): Session {
  return useContext(Ctx);
}

export function hasRole(s: Session, role: Role): boolean {
  return s.open || (s.me?.roles.includes(role) ?? false);
}
