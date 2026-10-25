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
  const crumbs = <><Link to="/contracts">Contracts</Link> / {name}</>;
  if (q.isPending) return <Page crumbs={crumbs}><Loading /></Page>;
  if (q.error) {
    const e = q.error;
    return (
      <Page crumbs={crumbs}>
        {e instanceof ApiError && e.status === 403 ? <div className="notice warn">You may see this contract's name only; its details are closed to you.</div>
          : e instanceof ApiError && e.status === 404 ? <div className="notice warn">Gaia has applied no contract by this name.</div>
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
          {c.openChange && <LinkButton to={`/changes/${encodeURIComponent(c.openChange)}`} icon="pr" title="The change between Git and what is applied">Open change</LinkButton>}
          <LinkButton to={`/runs?contract=${encodeURIComponent(c.name)}`} icon="play">Runs</LinkButton>
          <a className="btn" href="#history"><Icon name="history" size={15} />History</a>
        </div>
        <div className="row">
          {hasRole(session, "operator") && c.state?.appliedPlanHash && !c.openChange && (
            <Button icon="rerun" onClick={reconcile} title="Open a change that applies the applied plan again (puts drift right)">Reapply</Button>
          )}
          <Button icon="sync" onClick={() => void qc.invalidateQueries({ queryKey: ["contract", c.name] })} title="Reload what this page shows">Refresh</Button>
        </div>
      </div>

      <StatusBar items={[
        { icon: "heart", title: "Health", hint: <HealthLegend />, value: <HealthLabel h={h} size={20} />, sub: `${c.hosts.filter((x) => x.status === "active").length}/${c.hosts.length} servers active` },
        {
          icon: "sync", title: "Sync", hint: <SyncLegend />, value: <Sync state={s} size={20} />,
          sub: c.openChange ? <Link to={`/changes/${encodeURIComponent(c.openChange)}`}>Git has a change not applied yet</Link>
            : c.state?.evaluatedAt ? `evaluated ${ago(c.state.evaluatedAt)}` : "",
        },
        {
          icon: "history", title: "Last applied", hint: <>The run that last applied the contract successfully.<span className="leg"><Icon name="check-circle" size={15} color="var(--green)" /><span>hover the shield next to it for the run record</span></span></>,
          value: lastApplied ? <span className="row" style={{ gap: 8 }}><IL icon="check-circle" color="var(--green)" size={20}>Applied</IL><LazyRecordTip id={lastApplied.id} /></span> : <span className="muted" style={{ fontSize: 15 }}>not yet</span>,
          sub: <>{c.state?.appliedLabel && <Mono>{shortLabel(c.state.appliedLabel)}</Mono>}{c.state?.appliedAt && ` · ${ago(c.state.appliedAt)}`}{lastApplied && <> · <Link to={`/runs/${encodeURIComponent(lastApplied.id)}`}>{lastApplied.id}</Link></>}</>,
        },
        {
          icon: "scan", title: "Drift", hint: <>Whether the real resources differ from what Git says.
            <Leg icon="check-circle" color="var(--fg)" name="None">no drift found</Leg>
            <Leg icon="scan" color="var(--orange)" name="Found">drift found; reported or put right, by the management level</Leg></>,
          value: drifts.length ? <IL icon="scan" color="var(--orange)" size={20}>{drifts.length} found</IL> : <IL icon="check-circle" color="var(--fg)" size={20}>None</IL>,
          sub: drifts.length ? `first seen ${ago(drifts[0]?.firstSeen)}` : "",
        },
      ]} />

      {c.state?.error && <div className="notice warn" role="status">Gaia could not evaluate this contract: {c.state.error}</div>}

      <Card title="Resource tree" icon="network"
        hint={<>The real resources Gaia manages for the contract: contract → servers → each server's VM, address, monitoring entry and its records in other systems.
          <Leg icon="heart" color="var(--green)" name="active" fill="var(--green)">the server runs</Leg>
          <Leg icon="check" color="var(--green)" name="registered">the resource exists in that system</Leg>
          <Leg icon="clock" color="var(--yellow)" name="missing">in Git, not created yet (dashed)</Leg></>}
        right={<span className="row faint" style={{ gap: 6, fontSize: 12 }}><svg width="22" height="4" aria-hidden="true"><path d="M1 2h20" stroke="var(--yellow)" strokeWidth="2" strokeDasharray="4 3" /></svg>in Git, not yet created</span>}>
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
      <span className="label-caps row" style={{ gap: 6, marginRight: 4 }}><Icon name="book" size={14} />What it uses
        <Hint>The objects the contract refers to, and the providers behind them. The contract names them only.</Hint></span>
      {sites.map((x) => <Chip key={"s" + x} icon="pin" kind="Site" name={x} to={[compute(x), p.monitoring?.provider].filter(Boolean).join(", ")} hint="Site: a data centre; where VMs are created and how they are watched." />)}
      {nets.map((x) => <Chip key={"n" + x} icon="network" kind="Network" name={x} to={ipam(x)} hint="Network: what servers join. Addresses come from its IPAM pool." />)}
      {p.image && <Chip icon="disc" kind="Image" name={p.image} hint="Image: the operating system template VMs are cloned from." />}
      {p.size && <Chip icon="size" kind="Size" name={p.size.name} hint={`Size: ${p.size.cpu ?? "?"} vCPUs, ${p.size.memoryGiB ?? "?"} GiB memory, ${p.size.diskGiB ?? "?"} GiB disk.`} />}
      {baselineName(p) && <Chip icon="layers" kind="Baseline" name={baselineName(p)} hint="Baseline: the base configuration every server gets (time, SSH, packages)." />}
      {(p.roles ?? []).map((r) => <Chip key={"r" + r.name} icon="wrench" kind="AnsibleRole" name={r.name} to={r.provider} hint="AnsibleRole: a role the contract applies to its servers." />)}
    </div>
  );
}

function ip(ids: Record<string, string>, kind: string): [string, string] | undefined {
  const e = Object.entries(ids).find(([k]) => k.endsWith("/" + kind));
  return e ? [e[0].split("/")[0] ?? "", e[1]] : undefined;
}

function Servers({ c, drifts }: { c: Contract; drifts: Drift[] }) {
  return (
    <Card title="Servers" icon="server">
      {c.hosts.length === 0 ? <div className="empty">This contract has no live servers.</div> : (
        <table className="grid">
          <thead>
            <tr>
              <th>Hostname</th>
              <th><span className="th-hint">UID<Hint>The server's lasting identity in Gaia. It stays when a hostname is reused; logs and run records are kept by it.</Hint></span></th>
              <th>Status</th><th>IP</th><th>VM</th><th>Monitoring</th>
              <th><span className="th-hint">Drift<Hint right>Whether this server differs from what Git says.</Hint></span></th>
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
                  <td>{d ? <IL icon="scan" color="var(--orange)" size={14}>{d}</IL> : <span className="muted">none</span>}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </Card>
  );
}

const AREA: Record<string, string> = { registration: "Registration", provisioning: "Provisioning", configuration: "Configuration" };

function DriftCard({ drifts }: { drifts: Drift[] }) {
  return (
    <Card title="Drift" icon="scan" hint="How the real resource differs from what Git says: the area, the wanted and the found value.">
      <table className="grid">
        <thead><tr><th>Server</th><th>Area</th><th>Key</th><th>Wanted</th><th>Found</th><th>Seen</th><th>Fix</th></tr></thead>
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
      <Card title="History" icon="history" hint={<SourceLegend />}>
        {!changes ? <Loading /> : changes.length === 0 ? <div className="empty">No changes for this contract.</div> : (
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
