// The frame around every screen: Argo CD's left menu and the top bar.
import { useState, type FormEvent, type ReactNode } from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { useSession } from "../api/session";
import type { Change } from "../api/types";
import { Icon, Logo, type IconName } from "./icons";

const MENU: { to: string; icon: IconName; label: string; end?: boolean }[] = [
  { to: "/", icon: "grid", label: "Genel bakış", end: true },
  { to: "/changes", icon: "pr", label: "Değişiklikler" },
  { to: "/contracts", icon: "file", label: "Kontratlar" },
  { to: "/operations", icon: "calendar", label: "Operasyonlar" },
  { to: "/runs", icon: "play", label: "Run'lar" },
  { to: "/catalog", icon: "book", label: "Katalog" },
  { to: "/infra", icon: "server", label: "Altyapı" },
  { to: "/access", icon: "shield", label: "Erişim" },
  { to: "/settings", icon: "gear", label: "Ayarlar" },
];

function PendingCount() {
  const q = useQuery({
    queryKey: ["changes", "pending-count"],
    queryFn: () => api<Change[]>("/changes?open=true&limit=500"),
    refetchInterval: 30_000,
  });
  const sq = useQuery({
    queryKey: ["proposals", "pending-count"],
    queryFn: () => api<{ id: string }[]>("/settings/proposals?status=pending"),
    refetchInterval: 30_000,
  });
  const n = (q.data?.filter((c) => c.status === "pending").length ?? 0) + (sq.data?.length ?? 0);
  return n > 0 ? <span className="count" title="Onay bekleyen değişiklik">{n}</span> : null;
}

export function Shell() {
  return (
    <div className="shell">
      <nav className="nav" aria-label="Ana menü">
        <div className="nav-logo"><Logo />gaia</div>
        {MENU.map((m) => (
          <NavLink key={m.to} to={m.to} end={m.end} className={({ isActive }) => (isActive ? "active" : "")}>
            <Icon name={m.icon} size={18} />
            <span>{m.label}</span>
            {m.to === "/changes" && <PendingCount />}
          </NavLink>
        ))}
        <div className="nav-foot">Gaia</div>
      </nav>
      <div className="main">
        <Outlet />
      </div>
    </div>
  );
}

function Search() {
  const nav = useNavigate();
  const [q, setQ] = useState("");
  const go = (e: FormEvent) => {
    e.preventDefault();
    const v = q.trim();
    if (!v) return;
    nav(v.startsWith("run-") ? `/runs/${encodeURIComponent(v)}` : `/contracts/${encodeURIComponent(v)}`);
    setQ("");
  };
  return (
    <form role="search" onSubmit={go}>
      <label className="search">
        <span className="sr-only">Ara</span>
        <Icon name="search" size={14} />
        <input type="search" placeholder="Kontrat adı veya run-…" value={q} onChange={(e) => setQ(e.target.value)} />
      </label>
    </form>
  );
}

async function logout() {
  await api("/auth/logout", { method: "POST" }).catch(() => undefined);
  window.location.assign("/login");
}

// Page puts a screen's breadcrumb into the top bar and lays out its body.
export function Page({ crumbs, children }: { crumbs: ReactNode; children: ReactNode }) {
  const { me, open } = useSession();
  return (
    <>
      <header className="topbar">
        <div>{crumbs}</div>
        <div className="topbar-right">
          <Search />
          {me && (
            <>
              <NavLink to="/profile" className="il" style={{ color: "var(--fg2)" }}><Icon name="user" size={18} />{me.username}</NavLink>
              <button type="button" className="icon-btn" title="Çıkış" aria-label="Çıkış" onClick={logout}><Icon name="logout" size={18} /></button>
            </>
          )}
          {open && <span className="badge yellow" title="Bu sunucuda AuthConfig yok; API herkese açık.">kimlik doğrulama kapalı</span>}
        </div>
      </header>
      <main className="page">{children}</main>
    </>
  );
}
