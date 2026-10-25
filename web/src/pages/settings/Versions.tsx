// Ayarlar → Sürümler: every version of Gaia's settings, who wrote it and
// why (design 17.2); a version's page shows its objects as written.
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { api } from "../../api/client";
import type { SettingsVersion } from "../../api/objects";
import { Icon } from "../../ui/icons";
import { Card, ErrorNote, Hint, IL, Loading, Mono, short } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { TabHead } from "../../ui/tabs";
import { kindOf } from "../../ui/kinds";
import { ago, stamp } from "../../lib/time";

// Author shows who wrote a version: a person, Gaia, or a Source.
export function Author({ who }: { who: string }) {
  if (who.startsWith("source/")) return <IL icon="repeat" size={14} weight={500}>{who.slice(7)}</IL>;
  if (who === "gaia") return <IL icon="grid" size={14} weight={500}>Gaia</IL>;
  return <IL icon="user" size={14} weight={500}>{who}</IL>;
}

export function Versions() {
  const q = useQuery({ queryKey: ["settings", "versions"], queryFn: () => api<SettingsVersion[]>("/settings/versions") });
  return (
    <>
      <TabHead icon="history" text="Ayarların her sürümü: kim, ne zaman, neden."
        hint={<>Her uygulanan ayar değişikliği yeni bir sürümdür; eski sürümler silinmez. Bir run ya da Change hangi sürümle planlandıysa onu taşır.
          <span style={{ display: "block", marginTop: 6 }}>Yazanlar: bir kişi, Gaia (Git'ten içeri alma) ya da bir Source (ağlar IPAM'dan, takımlar dizinden).</span></>} />
      <Card title="Sürümler" icon="history" right={<span className="faint">{q.data?.length ?? 0} sürüm</span>}>
        {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : q.data.length === 0 ? <div className="empty">Bu Gaia ayarlarını henüz Git'ten okuyor.</div> : (
          <table className="grid">
            <thead><tr><th>Sürüm</th><th>Yazan</th><th>Neden</th><th>Nesne</th><th>Zaman</th></tr></thead>
            <tbody>
              {q.data.map((v, i) => (
                <tr key={v.version}>
                  <td className="nowrap"><Link to={`/settings/versions/${v.version}`} style={{ fontWeight: 600 }}>v{v.version}</Link>{i === 0 && <span className="badge green" style={{ marginLeft: 8 }}>güncel</span>}</td>
                  <td className="nowrap"><Author who={v.author} /></td>
                  <td>{v.reason}</td>
                  <td>{v.objects}</td>
                  <td className="muted nowrap" title={stamp(v.createdAt)}>{ago(v.createdAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </>
  );
}

type VersionDetail = SettingsVersion & { files: { path: string; kind: string; yaml: string }[] };

export function VersionPage() {
  const { version = "" } = useParams();
  const q = useQuery({ queryKey: ["settings", "versions", version], queryFn: () => api<VersionDetail>(`/settings/versions/${encodeURIComponent(version)}`) });
  return (
    <Page crumbs={<><Link to="/settings">Ayarlar</Link> / <Link to="/settings/versions">Sürümler</Link> / v{version}</>}>
      <div className="title-row">
        <span style={{ color: "var(--blue)" }}><Icon name="history" size={22} /></span>
        <h1>Ayarlar v{version}</h1>
        {q.data && <span className="il muted" style={{ fontWeight: 400 }}><Mono>{short(q.data.contentHash.replace("sha256:", ""), 12)}</Mono><Hint>Sürümün içeriğinin hash'i. Aynı içerik aynı hash'i verir.</Hint></span>}
      </div>
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : (
        <>
          <section className="status" aria-label="Sürüm">
            <div className="status-cell"><div className="status-title"><Icon name="user" size={14} /><span>Yazan</span></div><div className="status-value" style={{ fontSize: 15 }}><Author who={q.data.author} /></div></div>
            <div className="status-cell"><div className="status-title"><Icon name="info" size={14} /><span>Neden</span></div><div className="status-value" style={{ fontSize: 15 }}>{q.data.reason}</div></div>
            <div className="status-cell"><div className="status-title"><Icon name="clock" size={14} /><span>Zaman</span></div><div className="status-value" style={{ fontSize: 15 }}>{ago(q.data.createdAt)}</div><div className="status-sub">{stamp(q.data.createdAt)}</div></div>
            <div className="status-cell"><div className="status-title"><Icon name="box" size={14} /><span>Nesne</span></div><div className="status-value">{q.data.files.length}</div></div>
          </section>
          {q.data.files.map((f) => (
            <details key={f.path} className="card">
              <summary className="card-head" style={{ cursor: "pointer" }}>
                <span className="card-title"><Icon name={kindOf(f.kind).icon} size={16} /><h2><span className="faint" style={{ fontWeight: 400 }}>{f.kind}/</span>{f.path.slice(f.path.indexOf("/") + 1).replace(/\.yaml$/, "")}</h2></span>
                <Mono>{f.path}</Mono>
              </summary>
              <pre className="mono" style={{ margin: 0, padding: 16, overflowX: "auto", color: "var(--fg2)", lineHeight: 1.6 }}>{f.yaml}</pre>
            </details>
          ))}
        </>
      )}
    </Page>
  );
}
