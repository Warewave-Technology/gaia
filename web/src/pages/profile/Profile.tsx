// Profil: sessions, passkeys, recovery codes, signing and login keys, and
// notification subscriptions (design 11.5). Adding and removing happen in
// modals opened from each card's header.
import { useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { startRegistration } from "@simplewebauthn/browser";
import { api, ApiError } from "../../api/client";
import { useSession } from "../../api/session";
import { Icon, type IconName } from "../../ui/icons";
import { Badge, Button, Card, ErrorNote, Hint, IL, Loading, Mono } from "../../ui/parts";
import { Modal, Switch } from "../../ui/modal";
import { Page } from "../../ui/shell";
import { useStrong } from "../../ui/strong";
import { ago, stamp } from "../../lib/time";

type SessionRow = { id: string; kind: "ui" | "cli-refresh"; current: boolean; createdAt: string; lastSeenAt: string; expiresAt: string; ip?: string; userAgent?: string };
type Passkey = { id: string; name: string; createdAt: string; lastUsedAt?: string };
type Key = { fingerprint: string; kind?: string; name: string; publicKey: string; createdAt: string };
type Notes = { email: string; emailEditable: boolean; triggers: string[]; available: string[] };

// What each trigger is, grouped as the list shows them.
const TRIGGERS: { group: string; items: Record<string, { label: string; text: string }> }[] = [
  {
    group: "Changes",
    items: {
      "change.pending": { label: "Awaiting approval", text: "A change you may approve was opened." },
      "change.approved": { label: "Approved", text: "A change has the approvals it needs and will be applied." },
      "change.rejected": { label: "Rejected", text: "An approver rejected a change." },
      "change.applied": { label: "Applied", text: "A change's run finished successfully." },
      "change.failed": { label: "Failed", text: "A change's run failed or was cancelled." },
      "approval.expiring": { label: "Approval expiring", text: "An approved change may not run within its maintenance window." },
    },
  },
  {
    group: "Settings",
    items: {
      "settings.pending": { label: "Settings change awaiting approval", text: "A settings change was proposed." },
      "settings.applied": { label: "Settings change applied", text: "A settings change became a new settings version." },
      "settings.rejected": { label: "Settings change rejected", text: "An approver rejected a settings change." },
    },
  },
  {
    group: "Servers and access",
    items: {
      "drift.found": { label: "Drift found", text: "A real resource no longer matches what Git says." },
      "access.privileged": { label: "Added to a privileged group", text: "The directory added someone to a team in a privileged group." },
    },
  },
];

export function message(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error && e.name === "NotAllowedError") return "The passkey prompt was cancelled or timed out.";
  return e instanceof Error ? e.message : String(e);
}

type Strong = <T>(fn: () => Promise<T>) => Promise<T>;

export function Profile() {
  const { me, open } = useSession();
  const strong = useStrong();
  if (open || !me) {
    return <Page crumbs="Profile"><h1>Profile</h1><div className="notice warn">Sign-in is off on this server; there is no profile.</div></Page>;
  }
  return (
    <Page crumbs="Profile">
      <div className="title-row">
        <span style={{ color: "var(--fg2)" }}><Icon name="user" size={22} /></span>
        <h1>{me.username}</h1>
        {me.roles.map((r) => <Badge key={r} tone={r === "platform-admin" ? "orange" : r === "operator" ? "yellow" : "gray"}>{r}</Badge>)}
        <Hint>Your roles come from your teams (the Policy's gaiaRoles). Your teams come from the directory.</Hint>
        <span className="muted">{me.teams.join(", ")}</span>
      </div>
      {strong.prompt}
      <Sessions />
      <Passkeys strong={strong.run} />
      <Keys strong={strong.run} kind="signing" />
      <Keys strong={strong.run} kind="login" />
      <Notifications />
    </Page>
  );
}

// useAction runs fn, keeps whether it is busy and its error, for a modal.
function useAction() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async (fn: () => Promise<unknown>): Promise<boolean> => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      return true;
    } catch (e) {
      setError(message(e));
      return false;
    } finally {
      setBusy(false);
    }
  };
  return { busy, error, run, reset: () => setError(null) };
}

// Confirm is the modal before removing something.
function Confirm({ title, children, action, onClose, run }: { title: string; children: ReactNode; action: string; onClose: () => void; run: () => Promise<unknown> }) {
  const a = useAction();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (await a.run(run)) onClose();
  };
  return (
    <Modal title={title} icon="alert" tone="red" onClose={onClose} onSubmit={submit}
      footer={<><Button onClick={onClose} disabled={a.busy}>Cancel</Button><Button type="submit" kind="solid-danger" icon="x" disabled={a.busy}>{a.busy ? "Waiting…" : action}</Button></>}>
      <div>{children}</div>
      {a.error && <div className="error" role="alert">{a.error}</div>}
    </Modal>
  );
}

function Sessions() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["me", "sessions"], queryFn: () => api<SessionRow[]>("/me/sessions") });
  const [closing, setClosing] = useState<SessionRow | null>(null);
  return (
    <Card title="Sessions" icon="key" hint="Your open browser and CLI sessions. Close one you do not recognise; its token stops working at once.">
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : (
        <table className="grid">
          <thead><tr><th>Kind</th><th>Address</th><th>Browser / tool</th><th>Opened</th><th>Last seen</th><th>Expires</th><th /></tr></thead>
          <tbody>
            {q.data.map((s) => (
              <tr key={s.id}>
                <td><span className="row" style={{ gap: 6 }}><IL icon={s.kind === "ui" ? "grid" : "terminal"} size={14} weight={400}>{s.kind === "ui" ? "Browser" : "CLI"}</IL>{s.current && <Badge tone="green">this session</Badge>}</span></td>
                <td className="mono">{s.ip ?? "–"}</td>
                <td className="muted" style={{ maxWidth: 280, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={s.userAgent}>{s.userAgent ?? "–"}</td>
                <td className="muted">{stamp(s.createdAt)}</td>
                <td className="muted">{ago(s.lastSeenAt)}</td>
                <td className="muted">{stamp(s.expiresAt)}</td>
                <td style={{ textAlign: "right" }}><Button kind="danger" icon="x" onClick={() => setClosing(s)}>Close</Button></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {closing && (
        <Confirm title="Close the session" action="Close the session" onClose={() => setClosing(null)} run={async () => {
          await api(`/me/sessions/${encodeURIComponent(closing.id)}`, { method: "DELETE" });
          if (closing.current) window.location.assign("/login");
          await qc.invalidateQueries({ queryKey: ["me", "sessions"] });
        }}>
          {closing.current ? "This is the session you are using; closing it takes you to the sign-in page." : <>The {closing.kind === "ui" ? "browser" : "CLI"} session (<Mono>{closing.ip ?? "no address"}</Mono>) stops working at once.</>}
        </Confirm>
      )}
    </Card>
  );
}

function HeadButton({ icon, children, onClick, primary }: { icon: IconName; children: ReactNode; onClick: () => void; primary?: boolean }) {
  return <Button kind={primary ? "primary" : "plain"} icon={icon} onClick={onClick}>{children}</Button>;
}

function Passkeys({ strong }: { strong: Strong }) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["me", "passkeys"], queryFn: () => api<Passkey[]>("/me/passkeys") });
  const [modal, setModal] = useState<"add" | "codes" | null>(null);
  const [removing, setRemoving] = useState<Passkey | null>(null);
  return (
    <Card title="Passkeys" icon="key"
      hint="The passkeys you sign in and approve with. The last one cannot be removed. Recovery codes get you in once when you cannot reach your passkey."
      right={<div className="row"><HeadButton icon="rerun" onClick={() => setModal("codes")}>Recovery codes</HeadButton><HeadButton icon="plus" primary onClick={() => setModal("add")}>Add passkey</HeadButton></div>}>
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : q.data.length === 0 ? (
        <div className="empty">You have no passkey; approvals ask for your authenticator app's code.</div>
      ) : (
        <table className="grid">
          <thead><tr><th>Name</th><th>Added</th><th>Last used</th><th /></tr></thead>
          <tbody>
            {q.data.map((p) => (
              <tr key={p.id}>
                <td><IL icon="key" size={14}>{p.name || "unnamed"}</IL></td>
                <td className="muted">{stamp(p.createdAt)}</td>
                <td className="muted">{p.lastUsedAt ? ago(p.lastUsedAt) : "–"}</td>
                <td style={{ textAlign: "right" }}>
                  <Button kind="danger" icon="x" onClick={() => setRemoving(p)} disabled={q.data.length < 2} title={q.data.length < 2 ? "The last passkey cannot be removed" : undefined}>Remove</Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {modal === "add" && <AddPasskey strong={strong} onClose={() => setModal(null)} onDone={() => void qc.invalidateQueries({ queryKey: ["me", "passkeys"] })} />}
      {modal === "codes" && <RecoveryCodes strong={strong} onClose={() => setModal(null)} />}
      {removing && (
        <Confirm title="Remove the passkey" action="Remove" onClose={() => setRemoving(null)} run={async () => {
          await strong(() => api(`/me/passkeys/${encodeURIComponent(removing.id)}`, { method: "DELETE" }));
          await qc.invalidateQueries({ queryKey: ["me", "passkeys"] });
        }}>
          The passkey <b>{removing.name || "unnamed"}</b> is removed; this device can no longer sign in.
        </Confirm>
      )}
    </Card>
  );
}

function AddPasskey({ strong, onClose, onDone }: { strong: Strong; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const a = useAction();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const ok = await a.run(() => strong(async () => {
      const begin = await api<{ flow: string; options: { publicKey: Parameters<typeof startRegistration>[0]["optionsJSON"] } }>("/me/passkeys/begin", { method: "POST" });
      const credential = await startRegistration({ optionsJSON: begin.options.publicKey });
      await api("/me/passkeys/finish", { method: "POST", body: { flow: begin.flow, credential, name: name.trim() } });
    }));
    if (ok) {
      onDone();
      onClose();
    }
  };
  return (
    <Modal title="Add a passkey" icon="key" onClose={onClose} onSubmit={submit}
      footer={<><Button onClick={onClose} disabled={a.busy}>Cancel</Button><Button type="submit" kind="primary" icon="plus" disabled={a.busy}>{a.busy ? "Waiting…" : "Create"}</Button></>}>
      <p className="muted" style={{ margin: 0 }}>Your browser or security key will create a new passkey. Give it a name you will recognise.</p>
      <label className="field"><span>Name</span><input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. work laptop" /></label>
      {a.error && <div className="error" role="alert">{a.error}</div>}
    </Modal>
  );
}

function RecoveryCodes({ strong, onClose }: { strong: Strong; onClose: () => void }) {
  const [codes, setCodes] = useState<string[] | null>(null);
  const a = useAction();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    await a.run(() => strong(async () => setCodes((await api<{ recoveryCodes: string[] }>("/me/recovery-codes", { method: "POST" })).recoveryCodes)));
  };
  if (codes) {
    return (
      <Modal title="New recovery codes" icon="key" onClose={onClose} footer={<Button kind="primary" icon="check" onClick={onClose}>I saved them</Button>}>
        <div className="notice warn" style={{ margin: 0 }}>The codes are shown once; each works once. Keep them somewhere safe.</div>
        <pre className="codes">{codes.join("\n")}</pre>
      </Modal>
    );
  }
  return (
    <Modal title="New recovery codes" icon="rerun" onClose={onClose} onSubmit={submit}
      footer={<><Button onClick={onClose} disabled={a.busy}>Cancel</Button><Button type="submit" kind="primary" icon="rerun" disabled={a.busy}>{a.busy ? "Waiting…" : "Generate"}</Button></>}>
      <p style={{ margin: 0 }}>New codes are made and <b>all the old ones stop working</b>.</p>
      {a.error && <div className="error" role="alert">{a.error}</div>}
    </Modal>
  );
}

function Keys({ strong, kind }: { strong: Strong; kind: "signing" | "login" }) {
  const qc = useQueryClient();
  const path = kind === "signing" ? "/me/signing-keys" : "/me/login-keys";
  const q = useQuery({ queryKey: ["me", kind], queryFn: () => api<Key[]>(path) });
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Key | null>(null);
  const title = kind === "signing" ? "Signing keys" : "SSH login keys";
  const hint = kind === "signing"
    ? "The SSH or OpenPGP public keys you sign commits with. Gaia knows who wrote a commit by them; an unsigned or unknown commit is not applied."
    : "The public keys you log in to servers with over SSH. Gaia puts them on the servers you have access to.";
  return (
    <Card title={title} icon={kind === "signing" ? "key" : "terminal"} hint={hint} right={<HeadButton icon="plus" primary onClick={() => setAdding(true)}>Add key</HeadButton>}>
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : q.data.length === 0 ? <div className="empty">No keys.</div> : (
        <table className="grid">
          <thead><tr><th>Name</th>{kind === "signing" && <th>Kind</th>}<th>Fingerprint</th><th>Added</th><th /></tr></thead>
          <tbody>
            {q.data.map((k) => (
              <tr key={k.fingerprint}>
                <td>{k.name || "unnamed"}</td>
                {kind === "signing" && <td><Badge tone="gray">{k.kind}</Badge></td>}
                <td><Mono>{k.fingerprint}</Mono></td>
                <td className="muted">{stamp(k.createdAt)}</td>
                <td style={{ textAlign: "right" }}><Button kind="danger" icon="x" onClick={() => setRemoving(k)}>Remove</Button></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {adding && <AddKey kind={kind} path={path} strong={strong} onClose={() => setAdding(false)} onDone={() => void qc.invalidateQueries({ queryKey: ["me", kind] })} />}
      {removing && (
        <Confirm title="Remove the key" action="Remove" onClose={() => setRemoving(null)} run={async () => {
          await strong(() => api(`${path}/${encodeURIComponent(removing.fingerprint)}`, { method: "DELETE" }));
          await qc.invalidateQueries({ queryKey: ["me", kind] });
        }}>
          <b>{removing.name || "unnamed"}</b> (<Mono>{removing.fingerprint}</Mono>) is removed.{" "}
          {kind === "signing" ? "New commits signed with it are no longer applied." : "It leaves the servers at the next access run."}
        </Confirm>
      )}
    </Card>
  );
}

function AddKey({ kind, path, strong, onClose, onDone }: { kind: "signing" | "login"; path: string; strong: Strong; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const [pub, setPub] = useState("");
  const a = useAction();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const ok = await a.run(() => strong(() => api(path, { method: "POST", body: { name: name.trim(), publicKey: pub.trim() } })));
    if (ok) {
      onDone();
      onClose();
    }
  };
  return (
    <Modal title={kind === "signing" ? "Add a signing key" : "Add an SSH login key"} icon={kind === "signing" ? "key" : "terminal"} onClose={onClose} onSubmit={submit} width={600}
      footer={<><Button onClick={onClose} disabled={a.busy}>Cancel</Button><Button type="submit" kind="primary" icon="plus" disabled={a.busy || !pub.trim()}>{a.busy ? "Waiting…" : "Add"}</Button></>}>
      <label className="field"><span>Name</span><input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. laptop" /></label>
      <label className="field">
        <span>Public key</span>
        <textarea className="input mono" rows={4} value={pub} onChange={(e) => setPub(e.target.value)} required
          placeholder={kind === "signing" ? "ssh-ed25519 AAAA… or -----BEGIN PGP PUBLIC KEY BLOCK-----" : "ssh-ed25519 AAAA…"} />
      </label>
      {a.error && <div className="error" role="alert">{a.error}</div>}
    </Modal>
  );
}

function Notifications() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["me", "notifications"], queryFn: () => api<Notes>("/me/notifications") });
  const [editing, setEditing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState<string | null>(null);
  const save = async (body: { email: string | null; triggers: string[] }) => {
    const out = await api<Notes>("/me/notifications", { method: "PUT", body });
    qc.setQueryData(["me", "notifications"], out);
  };
  const toggle = async (n: Notes, t: string, on: boolean) => {
    setError(null);
    setSaving(t);
    try {
      await save({ email: null, triggers: on ? [...n.triggers, t] : n.triggers.filter((x) => x !== t) });
    } catch (e) {
      setError(message(e));
    } finally {
      setSaving(null);
    }
  };
  const right = q.data && (
    <div className="row" style={{ gap: 12 }}>
      <span className="il muted" style={{ fontWeight: 400 }}><Icon name="bell" size={14} />{q.data.email || "no email"}</span>
      {q.data.emailEditable && <Button icon="pencil" onClick={() => setEditing(true)}>Change email</Button>}
    </div>
  );
  return (
    <Card title="Notifications" icon="bell" right={right}
      hint="Which events email you. A switch is saved at once. Notifications to team channels are set on the Team; these are only yours.">
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : (
        <>
          {!q.data.email && <div className="notice warn" style={{ margin: 16 }}>You have no email address; the events you pick cannot reach you.</div>}
          {TRIGGERS.map((g) => {
            const items = Object.entries(g.items).filter(([k]) => q.data.available.includes(k));
            if (items.length === 0) return null;
            return (
              <section key={g.group} aria-label={g.group}>
                <div className="label-caps" style={{ padding: "14px 16px 6px" }}>{g.group}</div>
                <div>
                  {items.map(([k, t]) => (
                    <div className="setting" key={k}>
                      <Switch on={q.data.triggers.includes(k)} label={t.label} disabled={saving === k} onChange={(on) => void toggle(q.data, k, on)} />
                      <span className="what"><span>{t.label}</span><small>{t.text}</small></span>
                      <span className="code">{k}</span>
                    </div>
                  ))}
                </div>
              </section>
            );
          })}
          {error && <div className="error" role="alert" style={{ padding: "0 16px 14px" }}>{error}</div>}
        </>
      )}
      {editing && q.data && <EditEmail current={q.data.email} onClose={() => setEditing(false)} save={(email) => save({ email, triggers: q.data.triggers })} />}
    </Card>
  );
}

function EditEmail({ current, onClose, save }: { current: string; onClose: () => void; save: (email: string) => Promise<void> }) {
  const [email, setEmail] = useState(current);
  const a = useAction();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (await a.run(() => save(email.trim()))) onClose();
  };
  return (
    <Modal title="Email address" icon="bell" onClose={onClose} onSubmit={submit}
      footer={<><Button onClick={onClose} disabled={a.busy}>Cancel</Button><Button type="submit" kind="primary" icon="check" disabled={a.busy}>Save</Button></>}>
      <label className="field"><span>Where notifications go</span><input className="input" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required /></label>
      {a.error && <div className="error" role="alert">{a.error}</div>}
    </Modal>
  );
}
