// A contract: its state against Git, its resource tree, servers, drift
// and history.
import { useMemo, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, ApiError } from "../../api/client";
import { hasRole, useSession } from "../../api/session";
import type { Change, Contract, Drift, Run } from "../../api/types";
import { health, syncState } from "../../lib/contract";
import { baselineName, isServerPlan, type PlanContract, type PlanServer } from "../../lib/plan";
import { buildTree, type Tone } from "../../lib/tree";
import { ago } from "../../lib/time";
import { Icon, type IconName } from "../../ui/icons";
import { Badge, Button, Card, ErrorNote, Hint, IL, Leg, LinkButton, Loading, Mono, short, shortLabel, StatusBar } from "../../ui/parts";
import { LazyRecordTip } from "../../ui/record";
import { Page } from "../../ui/shell";
import { ChangeBadge, HealthLabel, HealthLegend, SourceLabel, SourceLegend, SYNC, Sync, SyncLegend } from "../../ui/status";
import { ResourceTree } from "../../ui/tree";

export function ContractDetail() {
  const { name = "" } = useParams();
  const q = useQuery({ queryKey: ["contract", name], queryFn: () => api<Contract>(`/contracts/${encodeURIComponent(name)}`), refetchInterval: 30_000 });
  const crumbs = <><Link to="/contracts">Kontratlar</Link> / {name}</>;
  if (q.isPending) return <Page crumbs={crumbs}><Loading /></Page>;
  if (q.error) {
    const e = q.error;
    return (
      <Page crumbs={crumbs}>
        {e instanceof ApiError && e.status === 403 ? <div className="notice warn">Bu kontratın sadece adını görebilirsiniz; ayrıntıları kapalı.</div>
          : e instanceof ApiError && e.status === 404 ? <div className="notice warn">Gaia'nın bu adla uyguladığı bir kontrat yok.</div>
          : <ErrorNote error={e} />}
      </Page>
    );
  }
  return <Body c={q.data} crumbs={crumbs} />;
}

const TONE_ICON: Record<string, Tone> = { "var(--green)": "green", "var(--yellow)": "yellow", "var(--orange)": "yellow", "var(--gray)": "gray" };

function Body({ c, crumbs }: { c: Contract; crumbs: ReactNode }) {
  const session = useSession();
  const qc = useQueryClient();
  const nav = useNavigate();
  const drift = useQuery({ queryKey: ["drift", c.name], queryFn: () => api<Drift[]>(`/drift?contract=${encodeURIComponent(c.name)}`) });
  const changes = useQuery({ queryKey: ["changes", "contract", c.name], queryFn: () => api<Change[]>(`/changes?contract=${encodeURIComponent(c.name)}&limit=20`) });
  const open = useQuery({ queryKey: ["change", c.openChange], queryFn: () => api<Change>(`/changes/${encodeURIComponent(c.openChange!)}`), enabled: !!c.openChange });

  const drifts = drift.data ?? [];
  const lastRun = c.runs[0];
  const lastApplied = c.runs.find((r) => r.status === "succeeded");
  const h = health(lastRun?.status, c.hosts, drifts.length);
  const s = syncState(c.state?.sync ?? (c.openChange ? "out-of-sync" : "unknown"), drifts.length);
  const applied = isServerPlan(c.state?.appliedPlan) ? c.state.appliedPlan.contracts?.find((x) => x.name === c.name) : undefined;
  const desired = isServerPlan(open.data?.plan) ? open.data.plan.contracts?.find((x) => x.name === c.name) : undefined;
  const plan = desired ?? applied;
  const tree = useMemo(() => {
    const look = SYNC[s];
    return buildTree(c, { label: look.label, icon: look.icon, tone: TONE_ICON[look.color] ?? "gray" }, desired?.servers ?? []);
  }, [c, s, desired]);

  const reconcile = async () => {
    const r = await api<{ change: string }>(`/contracts/${encodeURIComponent(c.name)}/reconcile`, { method: "POST" });
    await qc.invalidateQueries({ queryKey: ["contract", c.name] });
    nav(`/changes/${encodeURIComponent(r.change)}`);
  };
  const labels = Object.entries(c.labels ?? {}).map(([k, v]) => `${k} ${v}`).join(" · ");

  return (
    <Page crumbs={crumbs}>
      <div className="title-row">
        <span style={{ color: "var(--yellow)" }}><Icon name="file" size={22} /></span>
        <h1>{c.name}</h1>
        <Badge tone="gray" icon="server">Server</Badge>
        {labels && <span className="muted">{labels}</span>}
      </div>

      <div className="spread">
        <div className="row">
          {c.openChange && <LinkButton to={`/changes/${encodeURIComponent(c.openChange)}`} icon="pr" title="Git'teki ile uygulanan arasındaki değişiklik">Açık değişiklik</LinkButton>}
          <LinkButton to={`/runs?contract=${encodeURIComponent(c.name)}`} icon="play">Run'lar</LinkButton>
          <a className="btn" href="#history"><Icon name="history" size={15} />Geçmiş</a>
        </div>
        <div className="row">
          {hasRole(session, "operator") && c.state?.appliedPlanHash && !c.openChange && (
            <Button icon="rerun" onClick={reconcile} title="Uygulanan planı yeniden uygulayan bir Change aç (sapmaları düzeltir)">Yeniden uygula</Button>
          )}
          <Button icon="sync" onClick={() => void qc.invalidateQueries({ queryKey: ["contract", c.name] })} title="Ekrandaki bilgileri yenile">Yenile</Button>
        </div>
      </div>

      <StatusBar items={[
        { icon: "heart", title: "Sağlık", hint: <HealthLegend />, value: <HealthLabel h={h} size={20} />, sub: `${c.hosts.filter((x) => x.status === "active").length}/${c.hosts.length} sunucu active` },
        {
          icon: "sync", title: "Sync durumu", hint: <SyncLegend />, value: <Sync state={s} size={20} />,
          sub: c.openChange ? <Link to={`/changes/${encodeURIComponent(c.openChange)}`}>Git'te uygulanmamış bir değişiklik var</Link>
            : c.state?.evaluatedAt ? `son değerlendirme ${ago(c.state.evaluatedAt)}` : "",
        },
        {
          icon: "history", title: "Son uygulama", hint: <>Kontratı en son başarıyla uygulayan run.<span className="leg"><Icon name="check-circle" size={15} color="var(--green)" /><span>yanındaki kalkanın üstüne gelince run kaydı açılır</span></span></>,
          value: lastApplied ? <span className="row" style={{ gap: 8 }}><IL icon="check-circle" color="var(--green)" size={20}>Uygulandı</IL><LazyRecordTip id={lastApplied.id} /></span> : <span className="muted" style={{ fontSize: 15 }}>henüz yok</span>,
          sub: <>{c.state?.appliedLabel && <Mono>{shortLabel(c.state.appliedLabel)}</Mono>}{c.state?.appliedAt && ` · ${ago(c.state.appliedAt)}`}{lastApplied && <> · <Link to={`/runs/${encodeURIComponent(lastApplied.id)}`}>{lastApplied.id}</Link></>}</>,
        },
        {
          icon: "scan", title: "Drift", hint: <>Gerçek kaynakların Git'teki tanımdan sapıp sapmadığı.
            <Leg icon="check-circle" color="var(--fg)" name="Yok">sapma bulunmadı</Leg>
            <Leg icon="scan" color="var(--orange)" name="Var">sapma bulundu; yönetim seviyesine göre bildirilir ya da düzeltilir</Leg></>,
          value: drifts.length ? <IL icon="scan" color="var(--orange)" size={20}>{drifts.length} sapma</IL> : <IL icon="check-circle" color="var(--fg)" size={20}>Yok</IL>,
          sub: drifts.length ? `ilk görülme ${ago(drifts[0]?.firstSeen)}` : "",
        },
      ]} />

      {c.state?.error && <div className="notice warn" role="status">Gaia bu kontratı değerlendiremedi: {c.state.error}</div>}

      <Card title="Kaynak ağacı" icon="network"
        hint={<>Kontratın Gaia'nın yönettiği gerçek kaynakları: kontrat → sunucular → her sunucunun VM'i, IP'si, izleme kaydı ve diğer sistemlerdeki karşılıkları.
          <Leg icon="heart" color="var(--green)" name="active" fill="var(--green)">sunucu çalışıyor</Leg>
          <Leg icon="check" color="var(--green)" name="kayıtlı">kaynak o sistemde var</Leg>
          <Leg icon="clock" color="var(--yellow)" name="eksik">Git'te var, henüz oluşturulmadı (kesik çizgi)</Leg></>}
        right={<span className="row faint" style={{ gap: 6, fontSize: 12 }}><svg width="22" height="4" aria-hidden="true"><path d="M1 2h20" stroke="var(--yellow)" strokeWidth="2" strokeDasharray="4 3" /></svg>Git'te var, henüz yok</span>}>
        {plan && <CatalogChips p={plan} />}
        <div style={{ padding: "8px 16px 16px" }}><ResourceTree nodes={tree.nodes} edges={tree.edges} /></div>
      </Card>

      <Servers c={c} drifts={drifts} />
      {drifts.length > 0 && <DriftCard drifts={drifts} />}
      <History changes={changes.data} runs={c.runs} />
    </Page>
  );
}

function Chip({ icon, kind, name, to, hint }: { icon: IconName; kind: string; name: string; to?: string; hint: string }) {
  return (
    <span className="row" style={{ gap: 6, background: "var(--bg1)", border: "1px solid var(--bg2)", borderRadius: 4, padding: "3px 6px 3px 8px", color: "var(--fg2)", fontSize: 13 }}>
      <span style={{ color: "var(--blue)" }}><Icon name={icon} size={14} /></span>
      <span><span className="faint">{kind}/</span>{name}</span>
      {to && <><span className="faint"><Icon name="arrow" size={12} /></span><span style={{ color: "var(--aqua)" }}>{to}</span></>}
      <Hint width={260}>{hint}</Hint>
    </span>
  );
}

function uniq<T>(xs: T[]): T[] {
  return [...new Set(xs)];
}

function CatalogChips({ p }: { p: PlanContract }) {
  const servers: PlanServer[] = p.servers ?? [];
  const sites = uniq(servers.map((s) => s.site));
  const nets = uniq(servers.map((s) => s.network));
  const compute = (site: string) => uniq(servers.filter((s) => s.site === site).map((s) => s.compute?.provider ?? "")).filter(Boolean).join(", ");
  const ipam = (net: string) => uniq(servers.filter((s) => s.network === net).map((s) => s.ipam?.provider ?? "")).filter(Boolean).join(", ");
  return (
    <div className="row" style={{ padding: "12px 16px", borderBottom: "1px solid var(--bg1)" }}>
      <span className="label-caps row" style={{ gap: 6, marginRight: 4 }}><Icon name="book" size={14} />Kullandığı katalog
        <Hint>Kontratın referans verdiği katalog nesneleri ve arkalarındaki provider'lar. Katalog platform reposunda durur; kontrat sadece adlarını yazar.</Hint></span>
      {sites.map((x) => <Chip key={"s" + x} icon="pin" kind="Site" name={x} to={[compute(x), p.monitoring?.provider].filter(Boolean).join(", ")} hint="Site: veri merkezi. Hangi compute ve izleme provider'larının kullanılacağını söyler." />)}
      {nets.map((x) => <Chip key={"n" + x} icon="network" kind="Network" name={x} to={ipam(x)} hint="Network: sunucuların bağlanacağı ağ. IP'ler bu ağın IPAM havuzundan ayrılır." />)}
      {p.image && <Chip icon="disc" kind="Image" name={p.image} hint="Image: VM'in klonlandığı işletim sistemi şablonu." />}
      {p.size && <Chip icon="size" kind="Size" name={p.size.name} hint={`Size: VM boyutu (${p.size.cpu ?? "?"} vCPU, ${p.size.memoryGiB ?? "?"} GiB bellek, ${p.size.diskGiB ?? "?"} GiB disk).`} />}
      {baselineName(p) && <Chip icon="layers" kind="Baseline" name={baselineName(p)} hint="Baseline: her sunucuya uygulanan temel Ansible rolleri (saat, SSH, paketler)." />}
      {(p.roles ?? []).map((r) => <Chip key={"r" + r.name} icon="wrench" kind="AnsibleRole" name={r.name} to={r.provider} hint="AnsibleRole: kontratın sunuculara uyguladığı Ansible rolü." />)}
    </div>
  );
}

function ip(ids: Record<string, string>, kind: string): [string, string] | undefined {
  const e = Object.entries(ids).find(([k]) => k.endsWith("/" + kind));
  return e ? [e[0].split("/")[0] ?? "", e[1]] : undefined;
}

function Servers({ c, drifts }: { c: Contract; drifts: Drift[] }) {
  return (
    <Card title="Sunucular" icon="server">
      {c.hosts.length === 0 ? <div className="empty">Bu kontratın canlı sunucusu yok.</div> : (
        <table className="grid">
          <thead>
            <tr>
              <th>Hostname</th>
              <th><span className="th-hint">UID<Hint>Sunucunun Gaia'daki kalıcı kimliği. Hostname yeniden kullanılsa da değişmez; loglar ve run kayıtları bununla tutulur.</Hint></span></th>
              <th>Durum</th><th>IP</th><th>VM</th><th>İzleme</th>
              <th><span className="th-hint">Drift<Hint right>Bu sunucuda Git'teki tanımdan sapma var mı.</Hint></span></th>
            </tr>
          </thead>
          <tbody>
            {c.hosts.map((x) => {
              const vm = ip(x.externalIds, "vm");
              const mon = ip(x.externalIds, "host");
              const d = drifts.filter((y) => y.uid === x.uid).length;
              return (
                <tr key={x.uid}>
                  <td style={{ fontWeight: 600 }}>{x.hostname}</td>
                  <td className="mono">{x.uid}</td>
                  <td>{x.status === "active" ? <HealthLabel h="green" size={14}>active</HealthLabel> : <Badge tone={x.status === "decommissioning" ? "red" : "yellow"}>{x.status}</Badge>}</td>
                  <td className="mono">{ip(x.externalIds, "ip")?.[1] ?? "–"}</td>
                  <td>{vm ? `${vm[0]} ${vm[1]}` : "–"}</td>
                  <td>{mon ? <IL icon="activity" size={14} weight={400}>{mon[0]} {mon[1]}</IL> : "–"}</td>
                  <td>{d ? <IL icon="scan" color="var(--orange)" size={14}>{d}</IL> : <span className="muted">yok</span>}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </Card>
  );
}

const AREA: Record<string, string> = { registration: "Kayıt", provisioning: "Provisioning", configuration: "Konfigürasyon" };

function DriftCard({ drifts }: { drifts: Drift[] }) {
  return (
    <Card title="Sapmalar" icon="scan" hint="Gerçek kaynağın Git'teki tanımdan farkı: alan, istenen ve bulunan değer.">
      <table className="grid">
        <thead><tr><th>Sunucu</th><th>Alan</th><th>Anahtar</th><th>İstenen</th><th>Bulunan</th><th>Görülme</th><th>Düzeltme</th></tr></thead>
        <tbody>
          {drifts.map((d, i) => (
            <tr key={i}>
              <td>{d.hostname ?? "–"}</td>
              <td>{AREA[d.area ?? ""] ?? d.area}</td>
              <td className="mono">{d.key}</td>
              <td className="mono" style={{ color: "var(--green)" }}>{d.want}</td>
              <td className="mono" style={{ color: "var(--orange)" }}>{d.got}</td>
              <td className="muted">{ago(d.lastSeen)}</td>
              <td>{d.change ? <Link to={`/changes/${encodeURIComponent(d.change)}`}>Change</Link> : <span className="muted">–</span>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  );
}

function History({ changes, runs }: { changes: Change[] | undefined; runs: Run[] }) {
  const runStatus = new Map(runs.map((r) => [r.id, r.status]));
  return (
    <section id="history">
      <Card title="Geçmiş" icon="history" hint={<SourceLegend />}>
        {!changes ? <Loading /> : changes.length === 0 ? <div className="empty">Bu kontrat için değişiklik yok.</div> : (
          <ol style={{ listStyle: "none", margin: 0, padding: 0 }}>
            {changes.map((x, i) => {
              const commits = (x.commits ?? []) as { commit: string }[];
              const recorded = x.runId && ["succeeded", "failed"].includes(runStatus.get(x.runId) ?? (x.status === "applied" ? "succeeded" : x.status === "failed" ? "failed" : ""));
              return (
                <li key={x.id} className="row" style={{ gap: 12, padding: "12px 16px", borderTop: i ? "1px solid var(--bg1)" : undefined }}>
                  <span className="row" style={{ minWidth: 190, gap: 8 }}>
                    <Link to={`/changes/${encodeURIComponent(x.id)}`}><ChangeBadge status={x.status} /></Link>
                    {recorded && <LazyRecordTip id={x.runId!} />}
                  </span>
                  <SourceLabel source={x.source} />
                  {commits.length > 0 && <span className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={15} /><Mono>{commits.map((y) => short(y.commit)).join(", ")}</Mono></span>}
                  <span className="muted">{(x.requestedBy ?? []).join(", ") || "gaia"} · {ago(x.createdAt)}</span>
                  {x.runId && <Link to={`/runs/${encodeURIComponent(x.runId)}`} className="mono">{x.runId}</Link>}
                </li>
              );
            })}
          </ol>
        )}
      </Card>
    </section>
  );
}
