// Sync: what each Source (NetworkSource, AnsibleRoleSource, TeamSource)
// did last, and Gaia's commits (design 5.4, 11.5).
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api } from "../../api/client";
import { hasRole, useSession } from "../../api/session";
import { Icon } from "../../ui/icons";
import { Badge, Button, Card, ErrorNote, Hint, Leg, Loading, Mono, short, type Tone } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { kindOf } from "../../ui/kinds";
import { ago } from "../../lib/time";

type Report = { source: string; status: "unchanged" | "committed" | "refused" | "failed"; commit?: string; detail?: { added?: string[]; changed?: string[]; removed?: string[]; problems?: string[] }; error?: string; ranAt: string };

const STATUS: Record<Report["status"], { tone: Tone; label: string }> = {
  unchanged: { tone: "gray", label: "Değişiklik yok" },
  committed: { tone: "green", label: "Commit yazıldı" },
  refused: { tone: "yellow", label: "Reddedildi" },
  failed: { tone: "red", label: "Başarısız" },
};

export function Sync() {
  const session = useSession();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["sources"], queryFn: () => api<Report[]>("/sources"), refetchInterval: 30_000 });
  const [note, setNote] = useState<string | null>(null);
  const sync = async () => {
    setNote(null);
    try {
      await api("/sources/sync", { method: "POST" });
      setNote("Eşitleme başladı; sonuçlar birazdan burada.");
      setTimeout(() => void qc.invalidateQueries({ queryKey: ["sources"] }), 3000);
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <Page crumbs="Sync">
      <div className="spread">
        <div className="title-row">
          <h1>Sync</h1>
          <Hint>Source'lar katalog nesnelerini başka sistemlerden üretir: ağları IPAM'dan, rolleri config reposundan, takımları AD'den. Ürettiklerini Gaia'nın imzalı commit'iyle platform reposuna yazarlar.</Hint>
        </div>
        {hasRole(session, "platform-admin") && <Button icon="sync" onClick={sync}>Şimdi eşitle</Button>}
      </div>
      {note && <div className="notice" role="status">{note}</div>}
      <Card title="Source'lar" icon="repeat" hint={<>Her Source'un son çalışması.
        <Leg icon="same" color="var(--fg4)" name="Değişiklik yok">üretilen nesneler zaten Git'tekiyle aynı</Leg>
        <Leg icon="commit" color="var(--green)" name="Commit yazıldı">Gaia değişiklikleri platform reposuna yazdı</Leg>
        <Leg icon="alert" color="var(--yellow)" name="Reddedildi">sonuç doğrulanamadı; Git'e yazılmadı</Leg>
        <Leg icon="x-circle" color="var(--red)" name="Başarısız">kaynak sisteme ulaşılamadı ya da hata verdi</Leg></>}>
        {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : q.data.length === 0 ? <div className="empty">Henüz bir Source çalışmadı.</div> : (
          <table className="grid">
            <thead><tr><th>Source</th><th>Sonuç</th><th>Commit</th><th>Dosyalar</th><th>Sorunlar</th><th>Zaman</th></tr></thead>
            <tbody>
              {q.data.map((r) => {
                const [kind = "", name = ""] = r.source.split("/");
                const d = r.detail ?? {};
                return (
                  <tr key={r.source} style={{ verticalAlign: "top" }}>
                    <td><Link to={`/catalog/${encodeURIComponent(kind)}/${encodeURIComponent(name)}`} className="il"><Icon name={kindOf(kind).icon} size={15} /><span className="faint" style={{ fontWeight: 400 }}>{kind}/</span>{name}</Link></td>
                    <td><Badge tone={STATUS[r.status]?.tone ?? "gray"}>{STATUS[r.status]?.label ?? r.status}</Badge>{r.error && <div className="error" style={{ fontSize: 13, marginTop: 4 }}>{r.error}</div>}</td>
                    <td>{r.commit ? <span className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={15} /><Mono>{short(r.commit)}</Mono></span> : <span className="faint">–</span>}</td>
                    <td className="muted">
                      {([["+", d.added, "var(--green)"], ["~", d.changed, "var(--yellow)"], ["−", d.removed, "var(--red)"]] as const).map(([sym, xs, color]) =>
                        xs?.length ? <span key={sym} style={{ color, marginRight: 10 }} title={xs.join("\n")}>{`${sym}${xs.length}`}</span> : null)}
                      {!d.added?.length && !d.changed?.length && !d.removed?.length && "–"}
                    </td>
                    <td>{d.problems?.length ? <details><summary className="error" style={{ cursor: "pointer" }}>{d.problems.length} sorun</summary><ul style={{ margin: "6px 0 0", paddingLeft: 18, fontSize: 13 }}>{d.problems.map((p) => <li key={p}>{p}</li>)}</ul></details> : <span className="faint">–</span>}</td>
                    <td className="muted">{ago(r.ranAt)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </Card>
    </Page>
  );
}
