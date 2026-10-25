// Değişiklikler: the open changes, those that wait for me, and the rest.
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../../api/client";
import { useSession } from "../../api/session";
import type { Change } from "../../api/types";
import { Icon } from "../../ui/icons";
import { Badge, Card, ErrorNote, Hint, Loading, Mono, short } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { ChangeBadge, ChangeStatusLegend, SourceLabel, SourceLegend } from "../../ui/status";
import { ago } from "../../lib/time";

const FILTERS = [
  { key: "mine", label: "Onayımı bekleyenler", query: "?awaiting=me&limit=200" },
  { key: "open", label: "Açık", query: "?open=true&limit=200" },
  { key: "all", label: "Tümü", query: "?limit=200" },
] as const;

export function ChangeList() {
  const { me } = useSession();
  const [params, setParams] = useSearchParams();
  const filters = me ? FILTERS : FILTERS.filter((f) => f.key !== "mine");
  const key = params.get("f") ?? "open";
  const f = filters.find((x) => x.key === key) ?? filters[0]!;
  const q = useQuery({ queryKey: ["changes", f.key], queryFn: () => api<Change[]>("/changes" + f.query), refetchInterval: 15_000 });
  return (
    <Page crumbs="Değişiklikler">
      <div className="title-row">
        <h1>Değişiklikler</h1>
        <Hint>Change: kontrat reposuna gelen bir commit'in Gaia'daki karşılığı. Gaia planı hesaplar, gerekenler (imza, onay…) sağlanınca uygular.</Hint>
      </div>
      <div className="row" role="tablist" aria-label="Filtre">
        {filters.map((x) => (
          <button key={x.key} type="button" role="tab" aria-selected={x.key === f.key} className={x.key === f.key ? "btn primary" : "btn"}
            onClick={() => setParams(x.key === "open" ? {} : { f: x.key })}>
            {x.label}
          </button>
        ))}
      </div>
      <Card title={f.label} icon="pr">
        {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : q.data.length === 0 ? (
          <div className="empty">{f.key === "mine" ? "Onayınızı bekleyen değişiklik yok." : "Değişiklik yok."}</div>
        ) : (
          <table className="grid">
            <thead>
              <tr>
                <th><span className="th-hint">Durum<Hint><ChangeStatusLegend /></Hint></span></th>
                <th>Kontrat</th>
                <th><span className="th-hint">Kaynak<Hint><SourceLegend /></Hint></span></th>
                <th>Commit</th>
                <th>İsteyen</th>
                <th>Zaman</th>
              </tr>
            </thead>
            <tbody>
              {q.data.map((c) => <Row key={c.id} c={c} />)}
            </tbody>
          </table>
        )}
      </Card>
    </Page>
  );
}

function Row({ c }: { c: Change }) {
  if (c.restricted) {
    return (
      <tr>
        <td><ChangeBadge status={c.status} /></td>
        <td>{c.contract} <span className="faint">(yetkiniz yok)</span></td>
        <td colSpan={3} />
        <td className="muted">{ago(c.updatedAt)}</td>
      </tr>
    );
  }
  const commits = (c.commits ?? []) as { commit: string }[];
  return (
    <tr>
      <td>
        <span className="row">
          <ChangeBadge status={c.status} />
          {c.awaitingMe && <Badge tone="orange" icon="user">sizi bekliyor</Badge>}
        </span>
      </td>
      <td><Link to={`/changes/${encodeURIComponent(c.id)}`} style={{ fontWeight: 600 }}>{c.contract}</Link></td>
      <td><SourceLabel source={c.source} /></td>
      <td>
        {commits.length > 0 && (
          <span className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={15} /><Mono>{commits.map((x) => short(x.commit)).join(", ")}</Mono></span>
        )}
      </td>
      <td className="muted">{(c.requestedBy ?? []).join(", ")}</td>
      <td className="muted">{ago(c.createdAt)}</td>
    </tr>
  );
}
