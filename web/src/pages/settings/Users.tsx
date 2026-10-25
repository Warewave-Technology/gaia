// Settings → Users: who can sign in (design 17.5). Directory
// accounts come from the directory and are changed there; local accounts
// are break-glass platform admins when there is a directory.
import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../api/client";
import { hasRole, useSession } from "../../api/session";
import { Icon } from "../../ui/icons";
import { Badge, Button, Card, ErrorNote, Hint, IL, Leg, Loading } from "../../ui/parts";
import { Modal, Switch } from "../../ui/modal";
import { useStrong } from "../../ui/strong";
import { TabHead } from "../../ui/tabs";
import { objectPath } from "../../ui/kinds";
import { Link } from "react-router-dom";

export type User = { id: string; username: string; displayName?: string; provider: string; enabled: boolean; teams: string[]; directory: boolean; breakGlass?: boolean };
type AuthConfig = { require: string; providers: { name: string; type: string }[] };

export function useUsers(enabled = true) {
  return useQuery({ queryKey: ["users"], queryFn: () => api<User[]>("/users"), enabled });
}

function Kind({ u }: { u: User }) {
  if (u.directory) return <IL icon="users" size={14} weight={500} color="var(--blue)">directory · {u.provider}</IL>;
  if (u.breakGlass) return <IL icon="alert" size={14} weight={500} color="var(--orange)">break-glass</IL>;
  return <IL icon="user" size={14} weight={500} color="var(--fg3)">local</IL>;
}

function LinkModal({ title, link, onClose }: { title: string; link: string; onClose: () => void }) {
  return (
    <Modal title={title} icon="key" onClose={onClose} footer={<Button kind="primary" icon="check" onClick={onClose}>Done</Button>}>
      <p className="muted" style={{ margin: 0 }}>Send this link to the person by a safe channel. It works once, for a limited time; with it they register their passkey (or a password and an authenticator app).</p>
      <pre className="mono" style={{ margin: 0, padding: 12, background: "var(--bg0h)", borderRadius: 4, whiteSpace: "pre-wrap", wordBreak: "break-all" }}>{link}</pre>
    </Modal>
  );
}

function AddUser({ directory, onClose }: { directory: boolean; onClose: () => void }) {
  const [username, setUsername] = useState("");
  const [display, setDisplay] = useState("");
  const [teams, setTeams] = useState(directory ? "team-platform" : "");
  const [error, setError] = useState<string | null>(null);
  const [link, setLink] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const strong = useStrong();
  const qc = useQueryClient();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const out = await strong.run(() => api<{ enrollUrl: string }>("/users", { method: "POST", body: {
        username, displayName: display, teams: teams.split(",").map((t) => t.trim()).filter(Boolean), breakGlass: directory,
      } }));
      void qc.invalidateQueries({ queryKey: ["users"] });
      setLink(out.enrollUrl);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  if (link) return <LinkModal title={`Enrollment link for ${username}`} link={link} onClose={onClose} />;
  return (
    <>
      <Modal title={directory ? "Open a break-glass account" : "Add a user"} icon={directory ? "alert" : "user"} onClose={onClose} onSubmit={submit}
        footer={<><Button onClick={onClose} disabled={busy}>Cancel</Button><Button type="submit" kind="primary" icon="plus" disabled={busy}>{busy ? "Waiting…" : "Open"}</Button></>}>
        <p className="muted" style={{ margin: 0 }}>
          {directory
            ? "People sign in with the directory; a local account is only for getting in when the directory or sign-in is broken. A break-glass account is a platform admin's, and every use of it is in the audit log."
            : "This Gaia has no directory: people sign in with local accounts, and teams are set by hand."}
        </p>
        <div className="form-grid two">
          <label className="field"><span>Username</span><input className="input mono" value={username} onChange={(e) => setUsername(e.target.value)} required pattern="[a-z0-9._-]+" /></label>
          <label className="field"><span>Full name</span><input className="input" value={display} onChange={(e) => setDisplay(e.target.value)} /></label>
        </div>
        <label className="field"><span>Teams<Hint width={260}>Separate with commas. Roles come from the Policy's gaiaRoles; a break-glass account's team must make it a platform admin.</Hint></span><input className="input mono" value={teams} onChange={(e) => setTeams(e.target.value)} /></label>
        {error && <div className="error" role="alert">{error}</div>}
      </Modal>
      {strong.prompt}
    </>
  );
}

export function Users() {
  const session = useSession();
  const admin = hasRole(session, "platform-admin");
  const q = useUsers(admin);
  const cfg = useQuery({ queryKey: ["auth-config"], queryFn: () => api<AuthConfig>("/auth/config") });
  const directory = !!cfg.data?.providers.some((p) => p.type === "ldap");
  const [adding, setAdding] = useState(false);
  const [link, setLink] = useState<{ user: string; url: string } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const strong = useStrong();
  const qc = useQueryClient();
  const toggle = async (u: User, on: boolean) => {
    setError(null);
    try {
      await api(`/users/${encodeURIComponent(u.id)}/${on ? "enable" : "disable"}`, { method: "POST" });
      void qc.invalidateQueries({ queryKey: ["users"] });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  const reenroll = async (u: User) => {
    setError(null);
    try {
      const out = await strong.run(() => api<{ enrollUrl: string }>(`/users/${encodeURIComponent(u.id)}/enroll`, { method: "POST" }));
      setLink({ user: u.username, url: out.enrollUrl });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <>
      <TabHead icon="users" text={directory ? "People sign in with the directory; here are their accounts and the break-glass accounts." : "The accounts that can sign in to Gaia."}
        hint={<>Directory accounts come from the directory: their teams, and whether they are open, are set there, not here. An account closed in the directory cannot sign in, and its sessions end at the next sync.
          <Leg icon="users" color="var(--blue)" name="directory">an account from the directory</Leg>
          <Leg icon="alert" color="var(--orange)" name="break-glass">a local platform admin account for when the directory or sign-in is broken</Leg>
          <Leg icon="user" color="var(--fg3)" name="local">a local account where there is no directory</Leg></>} />
      {error && <div className="notice err" role="alert">{error}</div>}
      {!admin ? <div className="notice warn">Users are for platform admins.</div> : (
        <Card title="Users" icon="users" right={<Button icon="plus" onClick={() => setAdding(true)}>{directory ? "Add break-glass account" : "Add user"}</Button>}>
          {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : (
            <table className="grid">
              <thead><tr><th>User</th><th>Source</th><th>Teams</th><th>Status</th><th /></tr></thead>
              <tbody>
                {q.data.map((u) => (
                  <tr key={u.id}>
                    <td><span style={{ fontWeight: 600 }}>{u.username}</span>{u.displayName && <div className="muted" style={{ fontSize: 13 }}>{u.displayName}</div>}</td>
                    <td className="nowrap"><Kind u={u} /></td>
                    <td><span className="row" style={{ gap: 8 }}>{u.teams.length ? u.teams.map((t) => <Link key={t} to={objectPath("Team", t)} className="il" style={{ fontWeight: 500 }}><Icon name="users" size={13} />{t}</Link>) : <span className="faint">–</span>}</span></td>
                    <td className="nowrap">
                      {u.directory
                        ? (u.enabled ? <Badge tone="green" icon="check">open</Badge> : <Badge tone="gray" icon="x">closed in the directory</Badge>)
                        : <span className="row" style={{ gap: 8 }}><Switch on={u.enabled} label={`${u.username} open`} disabled={u.username === session.me?.username} onChange={(on) => void toggle(u, on)} /><span className="muted">{u.enabled ? "open" : "closed"}</span></span>}
                    </td>
                    <td className="nowrap" style={{ textAlign: "right" }}>{!u.directory && <Button icon="key" onClick={() => void reenroll(u)}>Re-enroll</Button>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      )}
      {adding && <AddUser directory={directory} onClose={() => setAdding(false)} />}
      {link && <LinkModal title={`Enrollment link for ${link.user}`} link={link.url} onClose={() => setLink(null)} />}
      {strong.prompt}
    </>
  );
}
