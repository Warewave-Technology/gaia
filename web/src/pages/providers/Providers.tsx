// Provider'lar: the systems Gaia talks to, where they are used and what
// the last discovery got from them (platform-admin).
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, ApiError } from "../../api/client";
import { hasRole, useSession } from "../../api/session";
import { Icon } from "../../ui/icons";
import { Card, ErrorNote, Hint, IL, Leg, Loading, Mono } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { refLink } from "../catalog/Catalog";

type Provider = { name: string; type: string; endpoint?: string; credentials?: string; usedBy: { kind: string; name: string; field: string }[]; error?: string };

export function Providers() {
  const session = useSession();
  const admin = hasRole(session, "platform-admin");
  const q = useQuery({ queryKey: ["providers"], queryFn: () => api<Provider[]>("/providers"), enabled: admin });
  return (
    <Page crumbs="Provider'lar">
      <div className="title-row">
        <h1>Provider'lar</h1>
        <Hint>Provider, Gaia'nın konuştuğu bir sistemdir: vSphere, Proxmox, IPAM, Zabbix, AD, OpenBao, Git… Kontratlar provider'ları doğrudan değil, katalog nesneleri (Site, Network…) üzerinden kullanır.</Hint>
      </div>
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
                  <th><span className="th-hint">Durum<Hint right>Son keşfin bu provider'dan aldığı cevap.
                    <Leg icon="check-circle" color="var(--green)" name="Sorun yok">son keşif hata almadı</Leg>
                    <Leg icon="x-circle" color="var(--red)" name="Hata">son keşif bu provider'a ulaşamadı ya da hata aldı</Leg>
                    <span className="leg"><span /><span className="faint">Sürekli bağlantı denetimi henüz yok.</span></span></Hint></span></th>
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
                    <td>{p.error ? <span title={p.error}><IL icon="x-circle" color="var(--red)" size={15}>Hata</IL></span> : <IL icon="check-circle" color="var(--green)" size={15}>Sorun yok</IL>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {q.data.some((p) => p.error) && (
              <div style={{ padding: "0 16px 16px" }}>
                {q.data.filter((p) => p.error).map((p) => <div key={p.name} className="notice err" style={{ marginTop: 8 }}><b>{p.name}</b>: <Mono>{p.error}</Mono></div>)}
              </div>
            )}
          </Card>
        )}
    </Page>
  );
}
