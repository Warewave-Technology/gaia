// Ayarlar → Kullanıcılar: who can sign in (design 17.5). Directory
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
  if (u.directory) return <IL icon="users" size={14} weight={500} color="var(--blue)">dizin · {u.provider}</IL>;
  if (u.breakGlass) return <IL icon="alert" size={14} weight={500} color="var(--orange)">break-glass</IL>;
  return <IL icon="user" size={14} weight={500} color="var(--fg3)">yerel</IL>;
}

function LinkModal({ title, link, onClose }: { title: string; link: string; onClose: () => void }) {
  return (
    <Modal title={title} icon="key" onClose={onClose} footer={<Button kind="primary" icon="check" onClick={onClose}>Tamam</Button>}>
      <p className="muted" style={{ margin: 0 }}>Bu bağlantıyı kişiye güvenli bir yoldan gönderin. Bir kez ve sınırlı bir süre çalışır; kişi onunla passkey'ini (ya da parola ve doğrulama uygulamasını) kaydeder.</p>
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
  if (link) return <LinkModal title={`${username} için kayıt bağlantısı`} link={link} onClose={onClose} />;
  return (
    <>
      <Modal title={directory ? "Break-glass hesabı aç" : "Kullanıcı ekle"} icon={directory ? "alert" : "user"} onClose={onClose} onSubmit={submit}
        footer={<><Button onClick={onClose} disabled={busy}>Vazgeç</Button><Button type="submit" kind="primary" icon="plus" disabled={busy}>{busy ? "Bekleniyor…" : "Aç"}</Button></>}>
        <p className="muted" style={{ margin: 0 }}>
          {directory
            ? "Kişiler dizinle girer; yerel hesap sadece dizin ya da giriş bozukken içeri girmek içindir. Break-glass hesabı bir platform-admin hesabıdır ve her kullanımı denetim kaydına yazılır."
            : "Bu Gaia'da dizin yok: kişiler yerel hesapla girer, takımlar elle yazılır."}
        </p>
        <div className="form-grid two">
          <label className="field"><span>Kullanıcı adı</span><input className="input mono" value={username} onChange={(e) => setUsername(e.target.value)} required pattern="[a-z0-9._-]+" /></label>
          <label className="field"><span>Ad soyad</span><input className="input" value={display} onChange={(e) => setDisplay(e.target.value)} /></label>
        </div>
        <label className="field"><span>Takımlar<Hint width={260}>Virgülle ayırın. Roller Policy'nin gaiaRoles alanından gelir; break-glass hesabının takımı platform-admin olmalı.</Hint></span><input className="input mono" value={teams} onChange={(e) => setTeams(e.target.value)} /></label>
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
      <TabHead icon="users" text={directory ? "Kişiler dizinle girer; burada dizin hesapları ve break-glass hesapları." : "Gaia'ya girebilen hesaplar."}
        hint={<>Dizin hesapları dizinden gelir: takımları ve açık/kapalı oluşu dizindedir, burada değiştirilmez. Dizinde kapatılan hesap giremez; oturumları bir sonraki eşitlemede kapanır.
          <Leg icon="users" color="var(--blue)" name="dizin">dizinden gelen hesap</Leg>
          <Leg icon="alert" color="var(--orange)" name="break-glass">dizin ya da giriş bozukken kullanılan yerel platform-admin hesabı</Leg>
          <Leg icon="user" color="var(--fg3)" name="yerel">dizin olmayan kurulumlarda yerel hesap</Leg></>} />
      {error && <div className="notice err" role="alert">{error}</div>}
      {!admin ? <div className="notice warn">Kullanıcıları sadece platform-admin rolü görür.</div> : (
        <Card title="Kullanıcılar" icon="users" right={<Button icon="plus" onClick={() => setAdding(true)}>{directory ? "Break-glass hesabı" : "Kullanıcı ekle"}</Button>}>
          {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : (
            <table className="grid">
              <thead><tr><th>Kullanıcı</th><th>Kaynak</th><th>Takımlar</th><th>Durum</th><th /></tr></thead>
              <tbody>
                {q.data.map((u) => (
                  <tr key={u.id}>
                    <td><span style={{ fontWeight: 600 }}>{u.username}</span>{u.displayName && <div className="muted" style={{ fontSize: 13 }}>{u.displayName}</div>}</td>
                    <td className="nowrap"><Kind u={u} /></td>
                    <td><span className="row" style={{ gap: 8 }}>{u.teams.length ? u.teams.map((t) => <Link key={t} to={objectPath("Team", t)} className="il" style={{ fontWeight: 500 }}><Icon name="users" size={13} />{t}</Link>) : <span className="faint">–</span>}</span></td>
                    <td className="nowrap">
                      {u.directory
                        ? (u.enabled ? <Badge tone="green" icon="check">açık</Badge> : <Badge tone="gray" icon="x">dizinde kapalı</Badge>)
                        : <span className="row" style={{ gap: 8 }}><Switch on={u.enabled} label={`${u.username} açık`} disabled={u.username === session.me?.username} onChange={(on) => void toggle(u, on)} /><span className="muted">{u.enabled ? "açık" : "kapalı"}</span></span>}
                    </td>
                    <td className="nowrap" style={{ textAlign: "right" }}>{!u.directory && <Button icon="key" onClick={() => void reenroll(u)}>Yeniden kayıt</Button>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      )}
      {adding && <AddUser directory={directory} onClose={() => setAdding(false)} />}
      {link && <LinkModal title={`${link.user} için kayıt bağlantısı`} link={link.url} onClose={() => setLink(null)} />}
      {strong.prompt}
    </>
  );
}
