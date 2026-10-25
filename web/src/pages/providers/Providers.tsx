// Provider'lar: the systems Gaia talks to, where they are used and what
// the last discovery got from them (platform-admin).
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, ApiError } from "../../api/client";
import { hasRole, useSession } from "../../api/session";
import { Icon } from "../../ui/icons";
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Button, Card, ErrorNote, Hint, IL, Leg, Loading, Mono } from "../../ui/parts";
import { ago, stamp } from "../../lib/time";
import { Page } from "../../ui/shell";
import { refLink } from "../catalog/Catalog";

type Check = { status: "ok" | "failed" | "skipped"; detail?: string; error?: string; latencyMs: number; checkedAt: string; lastOkAt?: string };
type Provider = { name: string; type: string; endpoint?: string; credentials?: string; usedBy: { kind: string; name: string; field: string }[]; error?: string; check?: Check };

function CheckCell({ c }: { c?: Check }) {
  if (!c) return <span className="faint">henüz denetlenmedi</span>;
  if (c.status === "ok") {
    return (
      <span title={`${c.latencyMs} ms · ${stamp(c.checkedAt)}`}>
        <IL icon="check-circle" color="var(--green)" size={15}>Bağlı</IL>
        <span className="muted" style={{ display: "block", fontSize: 12 }}>{c.detail} · {c.latencyMs} ms</span>
      </span>
    );
  }
  if (c.status === "skipped") {
    return (
      <span>
        <IL icon="minus" color="var(--fg4)" size={15} weight={500}>Denetlenmiyor</IL>
        <span className="faint" style={{ display: "block", fontSize: 12 }}>{c.detail}</span>
      </span>
    );
  }
  return (
    <span title={c.error}>
      <IL icon="x-circle" color="var(--red)" size={15}>Ulaşılamıyor</IL>
      <span className="muted" style={{ display: "block", fontSize: 12 }}>{c.lastOkAt ? `son bağlantı ${ago(c.lastOkAt)}` : "hiç bağlanamadı"}</span>
    </span>
  );
}

export function Providers() {
  const session = useSession();
  const admin = hasRole(session, "platform-admin");
  const q = useQuery({ queryKey: ["providers"], queryFn: () => api<Provider[]>("/providers"), enabled: admin, refetchInterval: 60_000 });
  const qc = useQueryClient();
  const [note, setNote] = useState<string | null>(null);
  const checkNow = async () => {
    try {
      await api("/providers/check", { method: "POST" });
      setNote("Denetim başladı; sonuçlar birkaç saniye içinde gelir.");
      setTimeout(() => void qc.invalidateQueries({ queryKey: ["providers"] }), 4000);
      setTimeout(() => void qc.invalidateQueries({ queryKey: ["providers"] }), 25000);
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <Page crumbs="Provider'lar">
      <div className="spread">
      <div className="title-row">
        <h1>Provider'lar</h1>
        <Hint>Provider, Gaia'nın konuştuğu bir sistemdir: vSphere, Proxmox, IPAM, Zabbix, AD, OpenBao, Git… Kontratlar provider'ları doğrudan değil, katalog nesneleri (Site, Network…) üzerinden kullanır.</Hint>
      </div>
      {admin && <Button icon="sync" onClick={checkNow} title="Bütün provider'ları şimdi denetle">Şimdi denetle</Button>}
      </div>
      {note && <div className="notice" role="status">{note}</div>}
      {!admin ? <div className="notice warn">Provider'ları sadece platform-admin rolü görür.</div>
        : q.isPending ? <Loading />
        : q.error ? (q.error instanceof ApiError && q.error.status === 503 ? <div className="notice warn">Bu sunucu bir repo izlemiyor; provider tanımı yok.</div> : <ErrorNote error={q.error} />)
        : (
          <Card title="Provider'lar" icon="plug" right={<span className="faint">{q.data.length} provider</span>}>
            <table className="grid">
              <thead>
                <tr>
                  <th>Ad</th><th>Tür</th><th>Adres</th>
                  <th><span className="th-hint">Kimlik bilgisi<Hint>Sadece OpenBao'daki yerine referans (bao://…). Secret'ın kendisi Gaia'nın API'sinden hiç çıkmaz.</Hint></span></th>
                  <th>Kullanan</th>
                  <th><span className="th-hint">Bağlantı<Hint right width={320}>Gaia her beş dakikada bir her provider'a kendi kimlik bilgisiyle zararsız bir çağrı yapar (sürüm sorgusu, tek bir okuma, LDAP bind).
                    <Leg icon="check-circle" color="var(--green)" name="Bağlı">adres ve kimlik bilgisi çalışıyor; cevabı ve süresi altta</Leg>
                    <Leg icon="x-circle" color="var(--red)" name="Ulaşılamıyor">bağlanamadı ya da kimlik bilgisi reddedildi; üstüne gelince hata görünür</Leg>
                    <Leg icon="minus" color="var(--fg4)" name="Denetlenmiyor">zararsız bir çağrısı yok (ör. Slack'e denemek bir mesaj göndermek olurdu)</Leg></Hint></span></th>
                  <th>Denetim</th>
                </tr>
              </thead>
              <tbody>
                {q.data.map((p) => (
                  <tr key={p.name}>
                    <td><Link to={`/catalog/Provider/${encodeURIComponent(p.name)}`} className="il"><Icon name="plug" size={15} />{p.name}</Link></td>
                    <td><span className="badge gray">{p.type}</span></td>
                    <td className="mono">{p.endpoint ?? "–"}</td>
                    <td className="mono muted">{p.credentials ?? "–"}</td>
                    <td>
                      <span className="row" style={{ gap: 6 }}>
                        {p.usedBy.slice(0, 4).map((u) => <Link key={u.kind + u.name + u.field} to={refLink(u)} className="badge gray" style={{ fontWeight: 500 }}>{u.kind}/{u.name}</Link>)}
                        {p.usedBy.length > 4 && <span className="faint">+{p.usedBy.length - 4}</span>}
                        {p.usedBy.length === 0 && <span className="faint">–</span>}
                      </span>
                    </td>
                    <td><CheckCell c={p.check} /></td>
                    <td className="muted">{p.check ? ago(p.check.checkedAt) : "–"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {q.data.some((p) => p.check?.status === "failed" || p.error) && (
              <div style={{ padding: "0 16px 16px" }}>
                {q.data.filter((p) => p.check?.status === "failed").map((p) => <div key={p.name} className="notice err" style={{ marginTop: 8 }}><b>{p.name}</b>: <Mono>{p.check?.error}</Mono></div>)}
                {q.data.filter((p) => p.error).map((p) => <div key={p.name + "/d"} className="notice warn" style={{ marginTop: 8 }}><b>{p.name}</b> (son keşif): <Mono>{p.error}</Mono></div>)}
              </div>
            )}
          </Card>
        )}
    </Page>
  );
}
