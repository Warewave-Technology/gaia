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
    group: "Değişiklikler",
    items: {
      "change.pending": { label: "Onay bekliyor", text: "Onaylayabileceğiniz bir değişiklik açıldı." },
      "change.approved": { label: "Onaylar tamamlandı", text: "Değişikliğin gereken onayları geldi; uygulanacak." },
      "change.rejected": { label: "Reddedildi", text: "Bir onaycı değişikliği reddetti." },
      "change.applied": { label: "Uygulandı", text: "Değişikliğin run'ı başarıyla bitti." },
      "change.failed": { label: "Başarısız", text: "Değişikliğin run'ı hata verdi ya da iptal edildi." },
      "approval.expiring": { label: "Onayın süresi doluyor", text: "Onaylanmış bir değişiklik bakım penceresi içinde çalışamayabilir." },
    },
  },
  {
    group: "Sunucular ve erişim",
    items: {
      "drift.found": { label: "Sapma bulundu", text: "Gerçek kaynak Git'teki tanımdan ayrıldı." },
      "access.privileged": { label: "Ayrıcalıklı gruba ekleme", text: "AD'de biri ayrıcalıklı bir gruptaki takıma eklendi." },
    },
  },
];

export function message(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error && e.name === "NotAllowedError") return "Passkey işlemi iptal edildi ya da zaman aşımına uğradı.";
  return e instanceof Error ? e.message : String(e);
}

type Strong = <T>(fn: () => Promise<T>) => Promise<T>;

export function Profile() {
  const { me, open } = useSession();
  const strong = useStrong();
  if (open || !me) {
    return <Page crumbs="Profil"><h1>Profil</h1><div className="notice warn">Bu sunucuda kimlik doğrulama kapalı; profil yok.</div></Page>;
  }
  return (
    <Page crumbs="Profil">
      <div className="title-row">
        <span style={{ color: "var(--fg2)" }}><Icon name="user" size={22} /></span>
        <h1>{me.username}</h1>
        {me.roles.map((r) => <Badge key={r} tone={r === "platform-admin" ? "orange" : r === "operator" ? "yellow" : "gray"}>{r}</Badge>)}
        <Hint>Roller takım üyeliklerinizden gelir (Policy'deki gaiaRoles). Takımlarınız AD'den eşitlenir.</Hint>
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
      footer={<><Button onClick={onClose} disabled={a.busy}>Vazgeç</Button><Button type="submit" kind="solid-danger" icon="x" disabled={a.busy}>{a.busy ? "Bekleniyor…" : action}</Button></>}>
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
    <Card title="Oturumlar" icon="key" hint="Açık tarayıcı ve CLI oturumlarınız. Tanımadığınız bir oturumu kapatın; kapanan oturumun token'ı anında geçersiz olur.">
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : (
        <table className="grid">
          <thead><tr><th>Tür</th><th>Adres</th><th>Tarayıcı / araç</th><th>Açıldı</th><th>Son görülme</th><th>Bitiş</th><th /></tr></thead>
          <tbody>
            {q.data.map((s) => (
              <tr key={s.id}>
                <td><span className="row" style={{ gap: 6 }}><IL icon={s.kind === "ui" ? "grid" : "terminal"} size={14} weight={400}>{s.kind === "ui" ? "Arayüz" : "CLI"}</IL>{s.current && <Badge tone="green">bu oturum</Badge>}</span></td>
                <td className="mono">{s.ip ?? "–"}</td>
                <td className="muted" style={{ maxWidth: 280, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={s.userAgent}>{s.userAgent ?? "–"}</td>
                <td className="muted">{stamp(s.createdAt)}</td>
                <td className="muted">{ago(s.lastSeenAt)}</td>
                <td className="muted">{stamp(s.expiresAt)}</td>
                <td style={{ textAlign: "right" }}><Button kind="danger" icon="x" onClick={() => setClosing(s)}>Kapat</Button></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {closing && (
        <Confirm title="Oturumu kapat" action="Oturumu kapat" onClose={() => setClosing(null)} run={async () => {
          await api(`/me/sessions/${encodeURIComponent(closing.id)}`, { method: "DELETE" });
          if (closing.current) window.location.assign("/login");
          await qc.invalidateQueries({ queryKey: ["me", "sessions"] });
        }}>
          {closing.current ? "Bu, şu an kullandığınız oturum; kapatınca giriş sayfasına dönersiniz." : <>{closing.kind === "ui" ? "Arayüz" : "CLI"} oturumu (<Mono>{closing.ip ?? "adres yok"}</Mono>) hemen geçersiz olur.</>}
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
    <Card title="Passkey'ler" icon="key"
      hint="Giriş ve onaylar için kullandığınız passkey'ler. Son passkey silinemez. Kurtarma kodları passkey'inize erişemediğinizde bir kez kullanılır."
      right={<div className="row"><HeadButton icon="rerun" onClick={() => setModal("codes")}>Kurtarma kodları</HeadButton><HeadButton icon="plus" primary onClick={() => setModal("add")}>Passkey ekle</HeadButton></div>}>
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : q.data.length === 0 ? (
        <div className="empty">Passkey'iniz yok; onaylar için doğrulama uygulamanızın kodu istenir.</div>
      ) : (
        <table className="grid">
          <thead><tr><th>Ad</th><th>Eklendi</th><th>Son kullanım</th><th /></tr></thead>
          <tbody>
            {q.data.map((p) => (
              <tr key={p.id}>
                <td><IL icon="key" size={14}>{p.name || "adsız"}</IL></td>
                <td className="muted">{stamp(p.createdAt)}</td>
                <td className="muted">{p.lastUsedAt ? ago(p.lastUsedAt) : "–"}</td>
                <td style={{ textAlign: "right" }}>
                  <Button kind="danger" icon="x" onClick={() => setRemoving(p)} disabled={q.data.length < 2} title={q.data.length < 2 ? "Son passkey silinemez" : undefined}>Sil</Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {modal === "add" && <AddPasskey strong={strong} onClose={() => setModal(null)} onDone={() => void qc.invalidateQueries({ queryKey: ["me", "passkeys"] })} />}
      {modal === "codes" && <RecoveryCodes strong={strong} onClose={() => setModal(null)} />}
      {removing && (
        <Confirm title="Passkey'i sil" action="Sil" onClose={() => setRemoving(null)} run={async () => {
          await strong(() => api(`/me/passkeys/${encodeURIComponent(removing.id)}`, { method: "DELETE" }));
          await qc.invalidateQueries({ queryKey: ["me", "passkeys"] });
        }}>
          <b>{removing.name || "adsız"}</b> passkey'i silinir; bu cihazla bir daha giriş yapılamaz.
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
    <Modal title="Passkey ekle" icon="key" onClose={onClose} onSubmit={submit}
      footer={<><Button onClick={onClose} disabled={a.busy}>Vazgeç</Button><Button type="submit" kind="primary" icon="plus" disabled={a.busy}>{a.busy ? "Bekleniyor…" : "Oluştur"}</Button></>}>
      <p className="muted" style={{ margin: 0 }}>Tarayıcınız ya da güvenlik anahtarınız yeni bir passkey oluşturacak. Bu cihazı tanıyacağınız bir ad verin.</p>
      <label className="field"><span>Ad</span><input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="ör. iş bilgisayarı" /></label>
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
      <Modal title="Yeni kurtarma kodları" icon="key" onClose={onClose} footer={<Button kind="primary" icon="check" onClick={onClose}>Kaydettim</Button>}>
        <div className="notice warn" style={{ margin: 0 }}>Kodlar bir kez gösterilir; her biri bir kez kullanılır. Güvenli bir yere kaydedin.</div>
        <pre className="codes">{codes.join("\n")}</pre>
      </Modal>
    );
  }
  return (
    <Modal title="Yeni kurtarma kodları" icon="rerun" onClose={onClose} onSubmit={submit}
      footer={<><Button onClick={onClose} disabled={a.busy}>Vazgeç</Button><Button type="submit" kind="primary" icon="rerun" disabled={a.busy}>{a.busy ? "Bekleniyor…" : "Üret"}</Button></>}>
      <p style={{ margin: 0 }}>Yeni kodlar üretilir ve <b>eskilerin hepsi geçersiz olur</b>.</p>
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
  const title = kind === "signing" ? "İmza anahtarları" : "SSH giriş anahtarları";
  const hint = kind === "signing"
    ? "Commit'lerinizi imzaladığınız SSH ya da OpenPGP açık anahtarları. Gaia bir commit'in sahibini bu anahtarlardan bilir; imzasız ya da tanınmayan commit uygulanmaz."
    : "Erişiminiz olan sunuculara SSH ile girerken kullandığınız açık anahtarlar. Gaia bunları erişim run'larında sunuculara dağıtır.";
  return (
    <Card title={title} icon={kind === "signing" ? "key" : "terminal"} hint={hint} right={<HeadButton icon="plus" primary onClick={() => setAdding(true)}>Anahtar ekle</HeadButton>}>
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : q.data.length === 0 ? <div className="empty">Kayıtlı anahtar yok.</div> : (
        <table className="grid">
          <thead><tr><th>Ad</th>{kind === "signing" && <th>Tür</th>}<th>Parmak izi</th><th>Eklendi</th><th /></tr></thead>
          <tbody>
            {q.data.map((k) => (
              <tr key={k.fingerprint}>
                <td>{k.name || "adsız"}</td>
                {kind === "signing" && <td><Badge tone="gray">{k.kind}</Badge></td>}
                <td><Mono>{k.fingerprint}</Mono></td>
                <td className="muted">{stamp(k.createdAt)}</td>
                <td style={{ textAlign: "right" }}><Button kind="danger" icon="x" onClick={() => setRemoving(k)}>Sil</Button></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {adding && <AddKey kind={kind} path={path} strong={strong} onClose={() => setAdding(false)} onDone={() => void qc.invalidateQueries({ queryKey: ["me", kind] })} />}
      {removing && (
        <Confirm title="Anahtarı sil" action="Sil" onClose={() => setRemoving(null)} run={async () => {
          await strong(() => api(`${path}/${encodeURIComponent(removing.fingerprint)}`, { method: "DELETE" }));
          await qc.invalidateQueries({ queryKey: ["me", kind] });
        }}>
          <b>{removing.name || "adsız"}</b> (<Mono>{removing.fingerprint}</Mono>) silinir.{" "}
          {kind === "signing" ? "Bu anahtarla imzalanan yeni commit'ler artık uygulanmaz." : "Bir sonraki erişim run'ında sunuculardan da kalkar."}
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
    <Modal title={kind === "signing" ? "İmza anahtarı ekle" : "SSH giriş anahtarı ekle"} icon={kind === "signing" ? "key" : "terminal"} onClose={onClose} onSubmit={submit} width={600}
      footer={<><Button onClick={onClose} disabled={a.busy}>Vazgeç</Button><Button type="submit" kind="primary" icon="plus" disabled={a.busy || !pub.trim()}>{a.busy ? "Bekleniyor…" : "Ekle"}</Button></>}>
      <label className="field"><span>Ad</span><input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="ör. dizüstü" /></label>
      <label className="field">
        <span>Açık anahtar</span>
        <textarea className="input mono" rows={4} value={pub} onChange={(e) => setPub(e.target.value)} required
          placeholder={kind === "signing" ? "ssh-ed25519 AAAA… ya da -----BEGIN PGP PUBLIC KEY BLOCK-----" : "ssh-ed25519 AAAA…"} />
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
      <span className="il muted" style={{ fontWeight: 400 }}><Icon name="bell" size={14} />{q.data.email || "e-posta yok"}</span>
      {q.data.emailEditable && <Button icon="pencil" onClick={() => setEditing(true)}>E-postayı değiştir</Button>}
    </div>
  );
  return (
    <Card title="Bildirimler" icon="bell" right={right}
      hint="Hangi olaylarda size e-posta gelsin. Değişiklik hemen kaydedilir. Takım kanallarına giden bildirimler Team objesinde tanımlıdır; bunlar sadece size gelenlerdir.">
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : (
        <>
          {!q.data.email && <div className="notice warn" style={{ margin: 16 }}>E-posta adresiniz yok; seçtiğiniz olaylar size ulaşmaz.</div>}
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
    <Modal title="E-posta adresi" icon="bell" onClose={onClose} onSubmit={submit}
      footer={<><Button onClick={onClose} disabled={a.busy}>Vazgeç</Button><Button type="submit" kind="primary" icon="check" disabled={a.busy}>Kaydet</Button></>}>
      <label className="field"><span>Bildirimlerin gideceği adres</span><input className="input" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required /></label>
      {a.error && <div className="error" role="alert">{a.error}</div>}
    </Modal>
  );
}
