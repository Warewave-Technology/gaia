// Değişiklikler: the open changes, those that wait for me, and the rest.
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../../api/client";
import { useSession } from "../../api/session";
import type { Change } from "../../api/types";
import { Icon } from "../../ui/icons";
import { Badge, Card, ErrorNote, Hint, IL, Leg, Loading, Mono, short } from "../../ui/parts";
import type { Proposal } from "../../api/proposals";
import { ProposalBadge, ProposalLegend } from "../settings/Proposal";
import { Page } from "../../ui/shell";
import { ChangeBadge, ChangeStatusLegend, SourceLabel, SourceLegend } from "../../ui/status";
import { ago } from "../../lib/time";

const FILTERS = [
  { key: "mine", label: "Onayımı bekleyenler", query: "?awaiting=me&limit=200", settings: "?awaiting=me" },
  { key: "open", label: "Açık", query: "?open=true&limit=200", settings: "?status=pending" },
  { key: "all", label: "Tümü", query: "?limit=200", settings: "?limit=200" },
] as const;

// A row is a contract change or a settings change, newest first.
type Item = { kind: "change"; at: string; c: Change } | { kind: "settings"; at: string; p: Proposal };

export function ChangeList() {
  const { me } = useSession();
  const [params, setParams] = useSearchParams();
  const filters = me ? FILTERS : FILTERS.filter((f) => f.key !== "mine");
  const key = params.get("f") ?? "open";
  const f = filters.find((x) => x.key === key) ?? filters[0]!;
  const q = useQuery({ queryKey: ["changes", f.key], queryFn: () => api<Change[]>("/changes" + f.query), refetchInterval: 15_000 });
  const sq = useQuery({ queryKey: ["proposals", f.key], queryFn: () => api<Proposal[]>("/settings/proposals" + f.settings), refetchInterval: 15_000 });
  const items: Item[] = [
    ...(q.data ?? []).map((c): Item => ({ kind: "change", at: c.createdAt, c })),
    ...(sq.data ?? []).map((p): Item => ({ kind: "settings", at: p.createdAt, p })),
  ].sort((a, b) => b.at.localeCompare(a.at));
  return (
    <Page crumbs="Değişiklikler">
      <div className="title-row">
        <h1>Değişiklikler</h1>
        <Hint width={340}>Change: kontrat reposuna gelen bir commit'in Gaia'daki karşılığı. Gaia planı hesaplar, gerekenler (imza, onay…) sağlanınca uygular.
          <span style={{ display: "block", marginTop: 6 }}>Ayar değişikliği: Gaia'nın ayarlarına (site, ağ, takım, provider…) önerilen bir değişiklik. Onaylanınca yeni bir ayar sürümü olur; planı değişen kontratlar için Change'ler açılır.</span>
          <Leg icon="file" color="var(--fg4)" name="kontrat">bir kontratın Change'i</Leg>
          <Leg icon="gear" color="var(--fg4)" name="ayar">bir ayar değişikliği</Leg></Hint>
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
        {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : items.length === 0 ? (
          <div className="empty">{f.key === "mine" ? "Onayınızı bekleyen değişiklik yok." : "Değişiklik yok."}</div>
        ) : (
          <table className="grid">
            <thead>
              <tr>
                <th><span className="th-hint">Durum<Hint><ChangeStatusLegend /><span style={{ display: "block", marginTop: 8 }}>Ayar değişiklikleri:</span><ProposalLegend /></Hint></span></th>
                <th>Kontrat ya da ayar</th>
                <th><span className="th-hint">Kaynak<Hint><SourceLegend /></Hint></span></th>
                <th>Commit ya da sürüm</th>
                <th>İsteyen</th>
                <th>Zaman</th>
              </tr>
            </thead>
            <tbody>
              {items.map((it) => (it.kind === "change" ? <Row key={it.c.id} c={it.c} /> : <SettingsRow key={it.p.id} p={it.p} />))}
            </tbody>
          </table>
        )}
      </Card>
    </Page>
  );
}

function SettingsRow({ p }: { p: Proposal }) {
  return (
    <tr>
      <td>
        <span className="row">
          <ProposalBadge status={p.status} />
          {p.mayDecide && <Badge tone="orange" icon="user">sizi bekliyor</Badge>}
        </span>
      </td>
      <td><Link to={`/changes/settings/${encodeURIComponent(p.id)}`} className="il" style={{ fontWeight: 600 }}><span className="faint"><Icon name="gear" size={14} /></span>{p.title}</Link></td>
      <td><IL icon="gear" color="var(--fg4)" size={15} weight={400}>ayar</IL></td>
      <td><span className="il muted" style={{ fontWeight: 400 }}><Icon name="history" size={15} />v{p.appliedVersion ?? p.baseVersion}</span></td>
      <td className="muted">{p.requestedBy}</td>
      <td className="muted">{ago(p.createdAt)}</td>
    </tr>
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
      <td><Link to={`/changes/${encodeURIComponent(c.id)}`} className="il" style={{ fontWeight: 600 }}><span className="faint"><Icon name="file" size={14} /></span>{c.contract}</Link></td>
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
