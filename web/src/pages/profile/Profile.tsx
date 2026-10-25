// Profil: sessions, passkeys, recovery codes, signing and login keys, and
// notification subscriptions (design 11.5).
import { useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { startRegistration } from "@simplewebauthn/browser";
import { api, ApiError } from "../../api/client";
import { useSession } from "../../api/session";
import { Icon } from "../../ui/icons";
import { Badge, Button, Card, ErrorNote, Hint, IL, Loading, Mono } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { useStrong } from "../../ui/strong";
import { ago, stamp } from "../../lib/time";

type SessionRow = { id: string; kind: "ui" | "cli-refresh"; current: boolean; createdAt: string; lastSeenAt: string; expiresAt: string; ip?: string; userAgent?: string };
type Passkey = { id: string; name: string; createdAt: string; lastUsedAt?: string };
type Key = { fingerprint: string; kind?: string; name: string; publicKey: string; createdAt: string };
type Notes = { email: string; emailEditable: boolean; triggers: string[]; available: string[] };

const TRIGGERS: Record<string, string> = {
  "change.pending": "Bir değişiklik onay bekliyor",
  "change.approved": "Onaylar tamamlandı",
  "change.rejected": "Bir değişiklik reddedildi",
  "change.applied": "Bir değişiklik uygulandı",
  "change.failed": "Bir değişikliğin run'ı başarısız oldu",
  "drift.found": "Sapma bulundu",
  "approval.expiring": "Onaylanmış bir değişiklik zamanında çalışamayabilir",
  "access.privileged": "Ayrıcalıklı bir gruba biri eklendi",
};

function message(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error && e.name === "NotAllowedError") return "Passkey işlemi iptal edildi ya da zaman aşımına uğradı.";
  return e instanceof Error ? e.message : String(e);
}

export function Profile() {
  const { me, open } = useSession();
  const strong = useStrong();
  const [error, setError] = useState<string | null>(null);
  const act = async (fn: () => Promise<unknown>) => {
    setError(null);
    try {
      await strong.run(fn);
    } catch (e) {
      setError(message(e));
    }
  };
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
      {error && <div className="notice err" role="alert">{error}</div>}
      <Sessions act={act} />
      <Passkeys act={act} />
      <Keys act={act} kind="signing" />
      <Keys act={act} kind="login" />
      <Notifications />
    </Page>
  );
}

type Act = (fn: () => Promise<unknown>) => Promise<void>;

function Sessions({ act }: { act: Act }) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["me", "sessions"], queryFn: () => api<SessionRow[]>("/me/sessions") });
  const revoke = (s: SessionRow) => act(async () => {
    await api(`/me/sessions/${encodeURIComponent(s.id)}`, { method: "DELETE" });
    if (s.current) window.location.assign("/login");
    await qc.invalidateQueries({ queryKey: ["me", "sessions"] });
  });
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
                <td><Button kind="danger" icon="x" onClick={() => void revoke(s)}>Kapat</Button></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

function Passkeys({ act }: { act: Act }) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["me", "passkeys"], queryFn: () => api<Passkey[]>("/me/passkeys") });
  const [name, setName] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const add = (e: FormEvent) => {
    e.preventDefault();
    void act(async () => {
      const begin = await api<{ flow: string; options: { publicKey: Parameters<typeof startRegistration>[0]["optionsJSON"] } }>("/me/passkeys/begin", { method: "POST" });
      const credential = await startRegistration({ optionsJSON: begin.options.publicKey });
      await api("/me/passkeys/finish", { method: "POST", body: { flow: begin.flow, credential, name: name.trim() } });
      setName("");
      await qc.invalidateQueries({ queryKey: ["me", "passkeys"] });
    });
  };
  const remove = (p: Passkey) => act(async () => {
    await api(`/me/passkeys/${encodeURIComponent(p.id)}`, { method: "DELETE" });
    await qc.invalidateQueries({ queryKey: ["me", "passkeys"] });
  });
  const regenerate = () => act(async () => {
    const r = await api<{ recoveryCodes: string[] }>("/me/recovery-codes", { method: "POST" });
    setCodes(r.recoveryCodes);
  });
  return (
    <Card title="Passkey'ler" icon="key" hint="Giriş ve onaylar için kullandığınız passkey'ler. Son passkey silinemez. Kurtarma kodları passkey'inize erişemediğinizde bir kez kullanılır.">
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : (
        <>
          {q.data.length === 0 ? <div className="empty">Passkey'iniz yok; onaylar için doğrulama uygulamanızın kodu istenir.</div> : (
            <table className="grid">
              <thead><tr><th>Ad</th><th>Eklendi</th><th>Son kullanım</th><th /></tr></thead>
              <tbody>
                {q.data.map((p) => (
                  <tr key={p.id}>
                    <td><IL icon="key" size={14}>{p.name || "adsız"}</IL></td>
                    <td className="muted">{stamp(p.createdAt)}</td>
                    <td className="muted">{p.lastUsedAt ? ago(p.lastUsedAt) : "–"}</td>
                    <td><Button kind="danger" icon="x" onClick={() => void remove(p)} disabled={q.data.length < 2} title={q.data.length < 2 ? "Son passkey silinemez" : undefined}>Sil</Button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <form onSubmit={add} className="row" style={{ padding: 16, borderTop: "1px solid var(--bg1)", alignItems: "flex-end" }}>
            <label className="field" style={{ flex: "1 1 240px" }}><span>Yeni passkey'in adı</span><input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="ör. iş bilgisayarı" /></label>
            <Button type="submit" kind="primary" icon="plus">Passkey ekle</Button>
            <Button icon="rerun" onClick={() => void regenerate()} title="Eskileri geçersiz kılar">Yeni kurtarma kodları</Button>
          </form>
          {codes && (
            <div className="notice warn" style={{ margin: "0 16px 16px" }} role="status">
              <b>Kurtarma kodlarınız bir kez gösterilir; güvenli bir yere kaydedin.</b>
              <pre className="mono" style={{ margin: "8px 0 0", columns: 2 }}>{codes.join("\n")}</pre>
            </div>
          )}
        </>
      )}
    </Card>
  );
}

function Keys({ act, kind }: { act: Act; kind: "signing" | "login" }) {
  const qc = useQueryClient();
  const path = kind === "signing" ? "/me/signing-keys" : "/me/login-keys";
  const q = useQuery({ queryKey: ["me", kind], queryFn: () => api<Key[]>(path) });
  const [name, setName] = useState("");
  const [pub, setPub] = useState("");
  const add = (e: FormEvent) => {
    e.preventDefault();
    void act(async () => {
      await api(path, { method: "POST", body: { name: name.trim(), publicKey: pub.trim() } });
      setName("");
      setPub("");
      await qc.invalidateQueries({ queryKey: ["me", kind] });
    });
  };
  const remove = (k: Key) => act(async () => {
    await api(`${path}/${encodeURIComponent(k.fingerprint)}`, { method: "DELETE" });
    await qc.invalidateQueries({ queryKey: ["me", kind] });
  });
  const title = kind === "signing" ? "İmza anahtarları" : "SSH giriş anahtarları";
  const hint: ReactNode = kind === "signing"
    ? "Commit'lerinizi imzaladığınız SSH ya da OpenPGP açık anahtarları. Gaia bir commit'in sahibini bu anahtarlardan bilir; imzasız ya da tanınmayan commit uygulanmaz."
    : "Erişiminiz olan sunuculara SSH ile girerken kullandığınız açık anahtarlar. Gaia bunları erişim run'larında sunuculara dağıtır.";
  return (
    <Card title={title} icon={kind === "signing" ? "key" : "terminal"} hint={hint}>
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : (
        <>
          {q.data.length === 0 ? <div className="empty">Kayıtlı anahtar yok.</div> : (
            <table className="grid">
              <thead><tr><th>Ad</th>{kind === "signing" && <th>Tür</th>}<th>Parmak izi</th><th>Eklendi</th><th /></tr></thead>
              <tbody>
                {q.data.map((k) => (
                  <tr key={k.fingerprint}>
                    <td>{k.name || "adsız"}</td>
                    {kind === "signing" && <td><Badge tone="gray">{k.kind}</Badge></td>}
                    <td><Mono>{k.fingerprint}</Mono></td>
                    <td className="muted">{stamp(k.createdAt)}</td>
                    <td><Button kind="danger" icon="x" onClick={() => void remove(k)}>Sil</Button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <form onSubmit={add} style={{ padding: 16, borderTop: "1px solid var(--bg1)", display: "flex", flexDirection: "column", gap: 10 }}>
            <label className="field"><span>Ad</span><input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="ör. dizüstü" /></label>
            <label className="field"><span>Açık anahtar</span><textarea className="input mono" rows={3} value={pub} onChange={(e) => setPub(e.target.value)} placeholder={kind === "signing" ? "ssh-ed25519 AAAA… ya da -----BEGIN PGP PUBLIC KEY BLOCK-----" : "ssh-ed25519 AAAA…"} required /></label>
            <div><Button type="submit" kind="primary" icon="plus">Anahtar ekle</Button></div>
          </form>
        </>
      )}
    </Card>
  );
}

function Notifications() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["me", "notifications"], queryFn: () => api<Notes>("/me/notifications") });
  const [draft, setDraft] = useState<{ email: string; triggers: string[] } | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  if (q.isPending) return <Card title="Bildirimler" icon="bell"><Loading /></Card>;
  if (q.error) return <Card title="Bildirimler" icon="bell"><ErrorNote error={q.error} /></Card>;
  const cur = draft ?? { email: q.data.email, triggers: q.data.triggers };
  const toggle = (t: string) => setDraft({ ...cur, triggers: cur.triggers.includes(t) ? cur.triggers.filter((x) => x !== t) : [...cur.triggers, t] });
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setSaved(null);
    try {
      await api("/me/notifications", { method: "PUT", body: { email: q.data.emailEditable ? cur.email : null, triggers: cur.triggers } });
      setDraft(null);
      setSaved("Kaydedildi.");
      await qc.invalidateQueries({ queryKey: ["me", "notifications"] });
    } catch (err) {
      setSaved(message(err));
    }
  };
  return (
    <Card title="Bildirimler" icon="bell" hint="Hangi olaylarda size e-posta gelsin. Takım kanallarına giden bildirimler Team objesinde tanımlıdır; bunlar sadece size gelenlerdir.">
      <form onSubmit={save} style={{ padding: 16, display: "flex", flexDirection: "column", gap: 12 }}>
        <label className="field" style={{ maxWidth: 420 }}>
          <span>E-posta {q.data.emailEditable ? "" : "(kimlik sağlayıcınızdan gelir)"}</span>
          <input className="input" type="email" value={cur.email} disabled={!q.data.emailEditable} onChange={(e) => setDraft({ ...cur, email: e.target.value })} />
        </label>
        <fieldset style={{ border: 0, padding: 0, margin: 0, display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(300px, 1fr))", gap: 8 }}>
          <legend className="label-caps" style={{ marginBottom: 8 }}>Olaylar</legend>
          {q.data.available.map((t) => (
            <label key={t} className="row" style={{ gap: 8 }}>
              <input type="checkbox" checked={cur.triggers.includes(t)} onChange={() => toggle(t)} />
              <span>{TRIGGERS[t] ?? t}</span><span className="mono faint">{t}</span>
            </label>
          ))}
        </fieldset>
        <div className="row"><Button type="submit" kind="primary" icon="check" disabled={!draft}>Kaydet</Button>{saved && <span className="muted" role="status">{saved}</span>}</div>
      </form>
    </Card>
  );
}
