// Overview: what waits for me, the contracts and the last runs.
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api } from "../../api/client";
import { useSession } from "../../api/session";
import type { Change, Run } from "../../api/types";
import { driftCount, health, syncState } from "../../lib/contract";
import { ago, duration } from "../../lib/time";
import { Icon } from "../../ui/icons";
import { Card, ErrorNote, Hint, IL, LinkButton, Loading, Mono, short, StatusBar } from "../../ui/parts";
import { RunResult } from "../../ui/record";
import { Page } from "../../ui/shell";
import { HealthLabel, HealthLegend, RecordLegend, Sync, SyncLegend } from "../../ui/status";
import { ContractGrid, useContracts } from "../contracts/List";

const DAY = 24 * 3600 * 1000;

export function Overview() {
  const { me } = useSession();
  const { contracts, drift } = useContracts();
  const mine = useQuery({ queryKey: ["changes", "mine"], queryFn: () => api<Change[]>("/changes?awaiting=me&limit=200"), enabled: !!me, refetchInterval: 15_000 });
  const open = useQuery({ queryKey: ["changes", "open"], queryFn: () => api<Change[]>("/changes?open=true&limit=200"), refetchInterval: 15_000 });
  const runs = useQuery({ queryKey: ["runs", "recent"], queryFn: () => api<Run[]>("/runs?limit=200"), refetchInterval: 15_000 });

  const cs = (contracts.data ?? []).filter((c) => !c.restricted);
  const hs = cs.map((c) => health(c.lastStatus, c.liveHosts, driftCount(drift.data, c.name)));
  const ss = cs.map((c) => syncState(c.sync, driftCount(drift.data, c.name)));
  // "Now" is when the runs were read, so that rendering stays pure.
  const now = runs.dataUpdatedAt;
  const failed = (runs.data ?? []).filter((r) => r.status === "failed" && now - new Date(r.finishedAt ?? r.queuedAt).getTime() < DAY);
  const lastFailed = (runs.data ?? []).find((r) => r.status === "failed");
  const count = <T,>(xs: T[], x: T) => xs.filter((y) => y === x).length;

  return (
    <Page crumbs="Overview">
      <div className="title-row">
        <h1>Overview</h1>
        <Hint>Changes waiting for your approval, how the contracts are, and the latest runs.</Hint>
      </div>

      <StatusBar items={[
        {
          icon: "heart", title: "Health", hint: <HealthLegend />,
          value: <><HealthLabel h="green" size={18}>{count(hs, "green")}</HealthLabel><HealthLabel h="yellow" size={18}>{count(hs, "yellow")}</HealthLabel><HealthLabel h="red" size={18}>{count(hs, "red")}</HealthLabel></>,
          sub: `${cs.length} contracts`,
        },
        {
          icon: "sync", title: "Sync", hint: <SyncLegend />,
          value: <><Sync state="synced" size={18}>{count(ss, "synced")}</Sync><Sync state="out-of-sync" size={18}>{count(ss, "out-of-sync")}</Sync><Sync state="drifted" size={18}>{count(ss, "drifted")}</Sync></>,
          sub: count(ss, "unknown") ? `${count(ss, "unknown")} contracts not evaluated` : "",
        },
        {
          icon: "pr", title: "Awaiting you", hint: "Changes you may approve that wait to be applied. A change is the plan of a commit to the contract repository, or of a settings change.",
          value: <IL icon="clock" color="var(--blue)" size={18}>{me ? mine.data?.length ?? "…" : "–"}</IL>,
          sub: <>{open.data?.length ?? "…"} open changes · <Link to="/changes">review</Link></>,
        },
        {
          icon: "play", title: "Failed runs (24h)", hint: "Runs that failed in the last 24 hours. A run applies a change to servers.",
          value: failed.length ? <IL icon="x-circle" color="var(--red)" size={18}>{failed.length}</IL> : <IL icon="check-circle" color="var(--green)" size={18}>0</IL>,
          sub: lastFailed ? <>last failed: <Link to={`/runs/${encodeURIComponent(lastFailed.id)}`} className="mono">{lastFailed.id}</Link> · {ago(lastFailed.finishedAt ?? lastFailed.queuedAt)}</> : "",
        },
      ]} />

      {(mine.data ?? []).map((c) => (
        <section key={c.id} aria-label="Awaiting your approval" className="card row" style={{ borderLeft: "4px solid var(--blue)", gap: 16, padding: "14px 16px" }}>
          <span className="label-caps row" style={{ gap: 6 }}><Icon name="clock" size={14} />Awaiting your approval</span>
          <Link to={`/changes/${encodeURIComponent(c.id)}`} className="il" style={{ color: "var(--fg)" }}><span style={{ color: "var(--blue)" }}><Icon name="pr" size={16} /></span>{c.contract}</Link>
          {(c.commits as { commit: string }[] | undefined)?.map((x) => <span key={x.commit} className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={15} /><Mono>{short(x.commit)}</Mono></span>)}
          <span className="il muted" style={{ fontWeight: 400 }}><Icon name="user" size={15} />{(c.requestedBy ?? []).join(", ")} · {ago(c.createdAt)}</span>
          <span style={{ flex: "1 1 auto" }} />
          <LinkButton to={`/changes/${encodeURIComponent(c.id)}`} kind="primary" icon="eye">Review</LinkButton>
        </section>
      ))}

      {contracts.isPending ? <Loading /> : contracts.error ? <ErrorNote error={contracts.error} /> : <ContractGrid contracts={contracts.data} drift={drift.data} />}

      <Card title="Latest runs" icon="play" hint="A run applies a change to servers: addresses, VMs, monitoring and Ansible, in that order." right={<Link to="/runs">All</Link>}>
        {runs.isPending ? <Loading /> : runs.error ? <ErrorNote error={runs.error} /> : runs.data.length === 0 ? <div className="empty">No runs.</div> : (
          <table className="grid">
            <thead><tr><th>Run</th><th>Contract</th><th><span className="th-hint">Result<Hint><RecordLegend /></Hint></span></th><th>Duration</th><th>When</th></tr></thead>
            <tbody>
              {runs.data.slice(0, 8).map((r) => (
                <tr key={r.id}>
                  <td className="mono">{r.restricted ? r.id : <Link to={`/runs/${encodeURIComponent(r.id)}`}>{r.id}</Link>}</td>
                  <td>{r.contracts.join(", ")}</td>
                  <td><RunResult run={r} /></td>
                  <td>{duration(r.startedAt, r.finishedAt)}</td>
                  <td className="muted">{ago(r.queuedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </Page>
  );
}
