// Settings → Audit log (design 17.4): every administrative action, who
// did it from where and how it ended; filtered, paged and exported as
// JSON lines for a SIEM.
import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api } from "../../api/client";
import { hasRole, useSession } from "../../api/session";
import { Icon, type IconName } from "../../ui/icons";
import { Badge, Button, Card, ErrorNote, Hint, Leg, Loading, Mono, type Tone } from "../../ui/parts";
import { TabHead } from "../../ui/tabs";
import { stamp } from "../../lib/time";

export type AuditEntry = {
  id: number; at: string; actor: string; actorId?: string; sessionId?: string; address?: string;
  action: string; object?: string; result: "ok" | "denied" | "failed"; detail?: unknown;
};

// What each action is, in words.
const ACTIONS: Record<string, string> = {
  "auth.login": "Sign-in", "auth.logout": "Sign-out", "auth.step-up": "Confirmation (step-up)", "auth.totp.enroll": "Authenticator app registered",
  "auth.recovery-code": "Sign-in with a recovery code", "auth.device.approve": "Device sign-in approved", "auth.device.deny": "Device sign-in denied",
  "session.revoke": "Session closed", "user.create": "Account opened", "user.disable": "Account closed", "user.enable": "Account opened again", "user.enroll": "Enrollment link made",
  "passkey.add": "Passkey added", "passkey.delete": "Passkey removed", "recovery-codes.renew": "Recovery codes renewed",
  "signing-key.add": "Signing key added", "signing-key.revoke": "Signing key removed", "login-key.add": "Login key added", "login-key.revoke": "Login key removed",
  "settings.import": "Settings imported from Git", "settings.write": "Settings version written", "settings.source": "Settings version from a Source", "settings.propose": "Settings change proposed",
  "settings.approve": "Settings change approved", "settings.apply": "Settings change applied", "settings.reject": "Settings change rejected", "settings.withdraw": "Settings change withdrawn",
  "change.approve": "Change approved", "change.reject": "Change rejected", "change.retry": "Change retried", "contract.reconcile": "Contract reapplied",
  "commit.revert": "Commit reverted", "run.cancel": "Run cancelled", "discovery.request": "Discovery requested", "sources.sync": "Sync requested", "providers.check": "Provider check requested",
};

const AREAS: { prefix: string; label: string; icon: IconName }[] = [
  { prefix: "auth.", label: "Sign-ins", icon: "key" },
  { prefix: "settings.", label: "Settings", icon: "gear" },
  { prefix: "change.", label: "Change decisions", icon: "pr" },
  { prefix: "user.", label: "Accounts", icon: "user" },
  { prefix: "run.", label: "Runs", icon: "play" },
];

function iconOf(action: string): IconName {
  const p = action.split(".")[0];
  const m: Record<string, IconName> = {
    auth: "key", session: "logout", user: "user", passkey: "key", "recovery-codes": "key", "signing-key": "key", "login-key": "key",
    settings: "gear", change: "pr", contract: "file", commit: "commit", run: "play", discovery: "compass", sources: "repeat", providers: "plug",
  };
  return m[p ?? ""] ?? "info";
}

const RESULT: Record<AuditEntry["result"], { tone: Tone; icon: IconName; label: string }> = {
  ok: { tone: "green", icon: "check", label: "ok" },
  denied: { tone: "orange", icon: "x", label: "denied" },
  failed: { tone: "red", icon: "alert", label: "failed" },
};

// ObjectRef links an entry's object to its page.
function ObjectRef({ o }: { o?: string }) {
  if (!o) return <span className="faint">–</span>;
  let to: string | null = null;
  if (o.startsWith("set-")) to = `/changes/settings/${encodeURIComponent(o)}`;
  else if (o.startsWith("chg-")) to = `/changes/${encodeURIComponent(o)}`;
  else if (o.startsWith("run-")) to = `/runs/${encodeURIComponent(o)}`;
  else if (/^settings@\d+$/.test(o)) to = `/settings/versions/${o.slice(9)}`;
  else if (o.startsWith("Server/")) to = `/contracts/${encodeURIComponent(o.slice(7))}`;
  else if (o.startsWith("user/")) to = "/settings/users";
  return to ? <Link to={to}><Mono>{o}</Mono></Link> : <Mono>{o}</Mono>;
}

type Filter = { actor: string; action: string; object: string; since: string };

function query(f: Filter, before?: number): string {
  const p = new URLSearchParams();
  if (f.actor) p.set("actor", f.actor);
  if (f.action) p.set("action", f.action);
  if (f.object) p.set("object", f.object);
  if (f.since) p.set("since", new Date(f.since).toISOString());
  if (before) p.set("before", String(before));
  return p.toString();
}

const PAGE = 100;

export function Audit() {
  const admin = hasRole(useSession(), "platform-admin");
  const [form, setForm] = useState<Filter>({ actor: "", action: "", object: "", since: "" });
  const [f, setF] = useState<Filter>(form);
  const [pages, setPages] = useState<number[]>([0]);
  const apply = (e: FormEvent) => {
    e.preventDefault();
    setF(form);
    setPages([0]);
  };
  const set = (k: keyof Filter) => (v: string) => setForm({ ...form, [k]: v });
  const area = (prefix: string) => {
    const next = { ...form, action: form.action === prefix ? "" : prefix };
    setForm(next);
    setF(next);
    setPages([0]);
  };
  if (!admin) return <div className="notice warn">The audit log is for platform admins.</div>;
  return (
    <>
      <TabHead icon="eye" text="Every administrative action in Gaia: who, from where, when, on what, and how it ended."
        hint={<>The log is append-only: Gaia's database role cannot change or delete it. The export gives one JSON object per line, ready for a SIEM.
          <Leg icon="check" color="var(--green)" name="ok">it was done</Leg>
          <Leg icon="x" color="var(--orange)" name="denied">not done, for lack of permission or a failed check (a wrong password…)</Leg>
          <Leg icon="alert" color="var(--red)" name="failed">not done because of an error</Leg></>}
        right={<a className="btn" href={`/api/v1/audit?format=jsonl&limit=10000&${query(f)}`} download="gaia-audit.jsonl"><Icon name="arrow" size={14} />Export (JSON lines)</a>} />
      <div className="chips" role="group" aria-label="Area">
        {AREAS.map((a) => <button key={a.prefix} type="button" className="chip" aria-pressed={f.action === a.prefix} onClick={() => area(a.prefix)}><Icon name={a.icon} size={14} />{a.label}</button>)}
      </div>
      <form className="card audit-filter" onSubmit={apply} aria-label="Filter">
        <label className="field"><span>Person</span><input className="input" value={form.actor} onChange={(e) => set("actor")(e.target.value)} placeholder="username" /></label>
        <label className="field"><span>Action<Hint width={260}>An action's name or its start: <b className="term">settings.</b> brings every settings action.</Hint></span><input className="input mono" value={form.action} onChange={(e) => set("action")(e.target.value)} placeholder="change.approve" /></label>
        <label className="field"><span>Object</span><input className="input mono" value={form.object} onChange={(e) => set("object")(e.target.value)} placeholder="chg-…, set-…, run-…" /></label>
        <label className="field"><span>Since</span><input className="input" type="datetime-local" value={form.since} onChange={(e) => set("since")(e.target.value)} /></label>
        <Button type="submit" kind="primary" icon="filter">Filter</Button>
      </form>
      <Card title="Entries" icon="eye">
        <table className="grid">
          <thead><tr><th>When</th><th>Who</th><th>Action</th><th>Object</th><th>Result</th><th>Detail</th></tr></thead>
          {pages.map((before, i) => <Page key={before} f={f} before={before} onMore={i === pages.length - 1 ? (id) => setPages([...pages, id]) : undefined} />)}
        </table>
      </Card>
    </>
  );
}

// Page is one page of entries; the last one offers the next.
function Page({ f, before, onMore }: { f: Filter; before: number; onMore?: (before: number) => void }) {
  const q = useQuery({ queryKey: ["audit", f, before], queryFn: () => api<AuditEntry[]>(`/audit?limit=${PAGE}&${query(f, before || undefined)}`) });
  if (q.isPending) return <tbody><tr><td colSpan={6}><Loading /></td></tr></tbody>;
  if (q.error) return <tbody><tr><td colSpan={6}><ErrorNote error={q.error} /></td></tr></tbody>;
  return (
    <tbody>
      {q.data.length === 0 && before === 0 && <tr><td colSpan={6} className="empty">No entries.</td></tr>}
      {q.data.map((e) => (
        <tr key={e.id} style={{ verticalAlign: "top" }}>
          <td className="nowrap muted">{stamp(e.at)}</td>
          <td className="nowrap"><span style={{ fontWeight: 600 }}>{e.actor}</span>{e.address && <div className="faint mono" style={{ fontSize: 12 }}>{e.address}</div>}</td>
          <td><span className="il" style={{ fontWeight: 500 }}><span className="faint"><Icon name={iconOf(e.action)} size={14} /></span>{ACTIONS[e.action] ?? e.action}</span><div className="faint mono" style={{ fontSize: 12 }}>{e.action}</div></td>
          <td><ObjectRef o={e.object} /></td>
          <td><Badge tone={RESULT[e.result]?.tone ?? "gray"} icon={RESULT[e.result]?.icon}>{RESULT[e.result]?.label ?? e.result}</Badge></td>
          <td>{e.detail ? <details><summary className="muted" style={{ cursor: "pointer" }}>show</summary><pre className="mono" style={{ margin: "6px 0 0", fontSize: 12, whiteSpace: "pre-wrap" }}>{JSON.stringify(e.detail, null, 2)}</pre></details> : <span className="faint">–</span>}</td>
        </tr>
      ))}
      {onMore && q.data.length === PAGE && (
        <tr><td colSpan={6} style={{ textAlign: "center" }}><Button icon="history" onClick={() => onMore(q.data[q.data.length - 1]!.id)}>Older</Button></td></tr>
      )}
    </tbody>
  );
}
