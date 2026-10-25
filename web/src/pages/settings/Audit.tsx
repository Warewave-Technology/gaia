// Ayarlar → Denetim kaydı (design 17.4): every administrative action, who
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
  "auth.login": "Giriş", "auth.logout": "Çıkış", "auth.step-up": "Kimlik doğrulama (step-up)", "auth.totp.enroll": "Doğrulama uygulaması kaydı",
  "auth.recovery-code": "Kurtarma koduyla giriş", "auth.device.approve": "Cihaz girişini onaylama", "auth.device.deny": "Cihaz girişini reddetme",
  "session.revoke": "Oturum kapatma", "user.create": "Hesap açma", "user.disable": "Hesabı kapatma", "user.enable": "Hesabı açma", "user.enroll": "Yeniden kayıt bağlantısı",
  "passkey.add": "Passkey ekleme", "passkey.delete": "Passkey silme", "recovery-codes.renew": "Kurtarma kodlarını yenileme",
  "signing-key.add": "İmza anahtarı ekleme", "signing-key.revoke": "İmza anahtarı kaldırma", "login-key.add": "Giriş anahtarı ekleme", "login-key.revoke": "Giriş anahtarı kaldırma",
  "settings.import": "Ayarları Git'ten alma", "settings.write": "Ayar sürümü yazma", "settings.source": "Source'tan ayar sürümü", "settings.propose": "Ayar değişikliği önerme",
  "settings.approve": "Ayar değişikliğini onaylama", "settings.apply": "Ayar değişikliğini uygulama", "settings.reject": "Ayar değişikliğini reddetme", "settings.withdraw": "Ayar değişikliğini geri çekme",
  "change.approve": "Change onaylama", "change.reject": "Change reddetme", "change.retry": "Change'i yeniden deneme", "contract.reconcile": "Kontratı düzeltme (reconcile)",
  "commit.revert": "Commit'i geri alma", "run.cancel": "Run iptali", "discovery.request": "Keşif isteme", "sources.sync": "Eşitleme isteme", "providers.check": "Provider denetimi isteme",
};

const AREAS: { prefix: string; label: string; icon: IconName }[] = [
  { prefix: "auth.", label: "Girişler", icon: "key" },
  { prefix: "settings.", label: "Ayarlar", icon: "gear" },
  { prefix: "change.", label: "Change kararları", icon: "pr" },
  { prefix: "user.", label: "Hesaplar", icon: "user" },
  { prefix: "run.", label: "Run'lar", icon: "play" },
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
  ok: { tone: "green", icon: "check", label: "tamam" },
  denied: { tone: "orange", icon: "x", label: "reddedildi" },
  failed: { tone: "red", icon: "alert", label: "başarısız" },
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
  if (!admin) return <div className="notice warn">Denetim kaydını sadece platform-admin rolü görür.</div>;
  return (
    <>
      <TabHead icon="eye" text="Gaia'da yapılan her yönetim işlemi: kim, nereden, ne zaman, neye, nasıl sonuçlandı."
        hint={<>Kayıt sadece eklenir: Gaia'nın veritabanı rolü onu değiştiremez ve silemez. Dışa aktarma her satırı bir JSON nesnesi olarak verir; bir SIEM'e gönderilebilir.
          <Leg icon="check" color="var(--green)" name="tamam">işlem yapıldı</Leg>
          <Leg icon="x" color="var(--orange)" name="reddedildi">yetki ya da doğrulama yüzünden yapılmadı (ör. yanlış parola)</Leg>
          <Leg icon="alert" color="var(--red)" name="başarısız">bir hata yüzünden yapılmadı</Leg></>}
        right={<a className="btn" href={`/api/v1/audit?format=jsonl&limit=10000&${query(f)}`} download="gaia-audit.jsonl"><Icon name="arrow" size={14} />JSON satırları</a>} />
      <div className="chips" role="group" aria-label="Alan">
        {AREAS.map((a) => <button key={a.prefix} type="button" className="chip" aria-pressed={f.action === a.prefix} onClick={() => area(a.prefix)}><Icon name={a.icon} size={14} />{a.label}</button>)}
      </div>
      <form className="card audit-filter" onSubmit={apply} aria-label="Süzgeç">
        <label className="field"><span>Kişi</span><input className="input" value={form.actor} onChange={(e) => set("actor")(e.target.value)} placeholder="kullanıcı adı" /></label>
        <label className="field"><span>İşlem<Hint width={260}>Bir işlemin adı ya da başı: <b className="term">settings.</b> bütün ayar işlemlerini getirir.</Hint></span><input className="input mono" value={form.action} onChange={(e) => set("action")(e.target.value)} placeholder="change.approve" /></label>
        <label className="field"><span>Nesne</span><input className="input mono" value={form.object} onChange={(e) => set("object")(e.target.value)} placeholder="chg-…, set-…, run-…" /></label>
        <label className="field"><span>Başlangıç</span><input className="input" type="datetime-local" value={form.since} onChange={(e) => set("since")(e.target.value)} /></label>
        <Button type="submit" kind="primary" icon="filter">Süz</Button>
      </form>
      <Card title="Kayıtlar" icon="eye">
        <table className="grid">
          <thead><tr><th>Zaman</th><th>Kim</th><th>İşlem</th><th>Nesne</th><th>Sonuç</th><th>Ayrıntı</th></tr></thead>
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
      {q.data.length === 0 && before === 0 && <tr><td colSpan={6} className="empty">Kayıt yok.</td></tr>}
      {q.data.map((e) => (
        <tr key={e.id} style={{ verticalAlign: "top" }}>
          <td className="nowrap muted">{stamp(e.at)}</td>
          <td className="nowrap"><span style={{ fontWeight: 600 }}>{e.actor}</span>{e.address && <div className="faint mono" style={{ fontSize: 12 }}>{e.address}</div>}</td>
          <td><span className="il" style={{ fontWeight: 500 }}><span className="faint"><Icon name={iconOf(e.action)} size={14} /></span>{ACTIONS[e.action] ?? e.action}</span><div className="faint mono" style={{ fontSize: 12 }}>{e.action}</div></td>
          <td><ObjectRef o={e.object} /></td>
          <td><Badge tone={RESULT[e.result]?.tone ?? "gray"} icon={RESULT[e.result]?.icon}>{RESULT[e.result]?.label ?? e.result}</Badge></td>
          <td>{e.detail ? <details><summary className="muted" style={{ cursor: "pointer" }}>göster</summary><pre className="mono" style={{ margin: "6px 0 0", fontSize: 12, whiteSpace: "pre-wrap" }}>{JSON.stringify(e.detail, null, 2)}</pre></details> : <span className="faint">–</span>}</td>
        </tr>
      ))}
      {onMore && q.data.length === PAGE && (
        <tr><td colSpan={6} style={{ textAlign: "center" }}><Button icon="history" onClick={() => onMore(q.data[q.data.length - 1]!.id)}>Daha eski</Button></td></tr>
      )}
    </tbody>
  );
}
