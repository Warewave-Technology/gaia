// Settings → Sources: the Sources that make Gaia's objects from other
// systems (design 5.4, 17.6), what each did last, the directory's
// membership sync, and the settings Git still has.
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api } from "../../api/client";
import { hasRole, useSession } from "../../api/session";
import { field, str, useObjects, useSettings, type CatalogObj } from "../../api/objects";
import { Icon, type IconName } from "../../ui/icons";
import { Badge, Button, Card, ErrorNote, Hint, Leg, Loading, Mono, short, type Tone } from "../../ui/parts";
import { kindOf, objectPath } from "../../ui/kinds";
import { TabHead } from "../../ui/tabs";
import { SettingsEditor, useCanPropose } from "../../ui/settings-editor";
import { ago, stamp } from "../../lib/time";

type Detail = {
  added?: string[]; changed?: string[]; removed?: string[]; problems?: string[]; version?: number;
  joined?: string[]; left?: string[]; disabled?: string[]; enabled?: string[];
};
type Report = { source: string; status: string; commit?: string; detail?: Detail; error?: string; ranAt: string };

const STATUS: Record<string, { tone: Tone; icon: IconName; label: string; text: string }> = {
  unchanged: { tone: "gray", icon: "check", label: "Up to date", text: "what it makes is already there" },
  committed: { tone: "green", icon: "commit", label: "Committed", text: "it wrote its changes to Git in a signed commit (roles, operations)" },
  stored: { tone: "green", icon: "history", label: "Saved", text: "it saved its changes as a new settings version (networks, teams)" },
  synced: { tone: "green", icon: "check", label: "Synced", text: "memberships are in step with the directory" },
  refused: { tone: "yellow", icon: "alert", label: "Refused", text: "the result would not load, so nothing was written" },
  failed: { tone: "red", icon: "x-circle", label: "Failed", text: "the system it reads could not be reached, or answered with an error" },
};

// WHAT each kind of Source makes, and where it writes.
const WHAT: Record<string, { makes: string; to: string }> = {
  TeamSource: { makes: "Teams from directory groups", to: "Settings" },
  NetworkSource: { makes: "Networks from IPAM subnets", to: "Settings" },
  AnsibleRoleSource: { makes: "Ansible roles from the configuration repository", to: "Git" },
  OperationSchedule: { makes: "Operations at set times", to: "Git" },
};

function StatusBadge({ r }: { r?: Report }) {
  if (!r) return <span className="faint">not run yet</span>;
  const s = STATUS[r.status] ?? { tone: "gray" as Tone, icon: "help" as IconName, label: r.status };
  return <Badge tone={s.tone} icon={s.icon}>{s.label}</Badge>;
}

function Counts({ d }: { d?: Detail }) {
  const parts = ([["+", d?.added, "var(--green)", "added"], ["~", d?.changed, "var(--yellow)", "changed"], ["−", d?.removed, "var(--red)", "removed"]] as const)
    .filter(([, xs]) => xs?.length);
  if (parts.length === 0) return <span className="faint">–</span>;
  return (
    <span className="row" style={{ gap: 10 }}>
      {parts.map(([sym, xs, color, what]) => <span key={sym} style={{ color }} title={`${what}:\n${xs!.join("\n")}`}>{sym}{xs!.length}</span>)}
    </span>
  );
}

function Wrote({ r }: { r?: Report }) {
  if (r?.commit) return <span className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={14} /><Mono>{short(r.commit)}</Mono></span>;
  if (r?.detail?.version) return <Link to={`/settings/versions/${r.detail.version}`} className="il" style={{ fontWeight: 400 }}><Icon name="history" size={14} />v{r.detail.version}</Link>;
  return null;
}

function AddSource() {
  const can = useCanPropose();
  const [menu, setMenu] = useState(false);
  const [kind, setKind] = useState<string | null>(null);
  if (!can) return null;
  return (
    <span className="menu-wrap">
      <Button icon="plus" onClick={() => setMenu(!menu)}>Add source</Button>
      {menu && (
        <ul className="menu" role="menu">
          {["NetworkSource", "TeamSource", "AnsibleRoleSource"].map((k) => (
            <li key={k}><button type="button" role="menuitem" onClick={() => { setMenu(false); setKind(k); }}><Icon name={kindOf(k).icon} size={14} /><span>{k}<small>{WHAT[k]!.makes}</small></span></button></li>
          ))}
        </ul>
      )}
      {kind && <SettingsEditor kind={kind} onClose={() => setKind(null)} />}
    </span>
  );
}

function SourcesCard({ defs, reports }: { defs: CatalogObj[]; reports: Map<string, Report> }) {
  // Reports with no definition: operation schedules, and Sources since removed.
  const defined = new Set(defs.map((o) => `${o.kind}/${o.name}`));
  const others = [...reports.values()].filter((r) => !r.source.startsWith("Directory/") && !defined.has(r.source));
  const rows: { kind: string; name: string; obj?: CatalogObj; r?: Report }[] = [
    ...defs.map((o) => ({ kind: o.kind, name: o.name, obj: o, r: reports.get(`${o.kind}/${o.name}`) })),
    ...others.map((r) => ({ kind: r.source.split("/")[0] ?? "", name: r.source.split("/").slice(1).join("/"), r })),
  ];
  return (
    <Card title="Sources" icon="repeat" right={<AddSource />}
      hint={<>Each Source reads a system and makes Gaia's objects from it. Networks and teams are settings, so they are saved as settings versions; roles and operations live in Git, so they are committed there.
        {Object.values(STATUS).filter((s) => s.label !== "Synced").map((s) => <Leg key={s.label} icon={s.icon} color={`var(--${s.tone === "gray" ? "fg4" : s.tone})`} name={s.label}>{s.text}</Leg>)}</>}>
      {rows.length === 0 ? <div className="empty">No sources. Add one to make networks, teams or roles from another system.</div> : (
        <table className="grid">
          <thead>
            <tr>
              <th>Source</th><th>Makes</th><th>Reads from</th><th>Writes to</th>
              <th>Last run</th>
              <th><span className="th-hint">Changes<Hint>Objects the last run added (+), changed (~) or removed (−). Hover for their names.</Hint></span></th>
              <th />
            </tr>
          </thead>
          <tbody>
            {rows.map(({ kind, name, obj, r }) => {
              const w = WHAT[kind] ?? { makes: "", to: "" };
              const provider = obj ? str(field(obj.spec, "provider")) : "";
              const problems = r?.detail?.problems ?? [];
              return (
                <tr key={kind + "/" + name} style={{ verticalAlign: "top" }}>
                  <td className="nowrap">
                    <Link to={kind === "OperationSchedule" ? `/operations?q=${encodeURIComponent(name)}` : objectPath(kind, name)} className="il" style={{ fontWeight: 600 }}>
                      <span className="faint"><Icon name={kindOf(kind).icon} size={14} /></span>{name}
                    </Link>
                    <div className="faint" style={{ fontSize: 12 }}>{kind}</div>
                  </td>
                  <td>{w.makes}</td>
                  <td>{provider ? <Link to={objectPath("Provider", provider)} className="il" style={{ fontWeight: 500 }}><Icon name="plug" size={13} />{provider}</Link> : <span className="faint">–</span>}</td>
                  <td>
                    {w.to === "Settings"
                      ? <span className="il" style={{ fontWeight: 500 }}><Icon name="history" size={13} />Settings</span>
                      : <span className="il" style={{ fontWeight: 500 }}><Icon name="commit" size={13} />Git{obj && str(field(obj.spec, "output")) && <Mono><span className="faint">{" "}{str(field(obj.spec, "output"))}</span></Mono>}</span>}
                  </td>
                  <td className="nowrap">
                    <StatusBadge r={r} />
                    {r && <div className="faint" style={{ fontSize: 12, marginTop: 3 }} title={stamp(r.ranAt)}>{ago(r.ranAt)} <Wrote r={r} /></div>}
                    {r?.error && <div className="error" style={{ fontSize: 12, marginTop: 3, maxWidth: 320, whiteSpace: "normal" }}>{r.error}</div>}
                  </td>
                  <td><Counts d={r?.detail} /></td>
                  <td>{problems.length > 0 && (
                    <details className="problems"><summary className="il" style={{ color: "var(--yellow)" }}><Icon name="alert" size={13} />{problems.length} left out</summary>
                      <ul>{problems.map((p) => <li key={p}>{p}</li>)}</ul>
                    </details>
                  )}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </Card>
  );
}

function People({ label, xs, color }: { label: string; xs?: string[]; color: string }) {
  if (!xs?.length) return null;
  return (
    <div className="dir-group">
      <span className="label-caps" style={{ color }}>{label} · {xs.length}</span>
      <ul>{xs.map((x) => <li key={x}>{x.replace("/member: ", " · member · ").replace("/approver: ", " · approver · ")}</li>)}</ul>
    </div>
  );
}

function DirectoryCard({ reports }: { reports: Report[] }) {
  if (reports.length === 0) return null;
  return (
    <Card title="Directory sync" icon="users"
      hint="For every team made from a directory group, Gaia reads who is in the group (and in its approvers' group) and keeps its accounts in step: new members get access, people who leave lose it, and accounts closed in the directory are closed in Gaia and signed out.">
      <table className="grid">
        <thead><tr><th>Directory</th><th>Last sync</th><th>Joined</th><th>Left</th><th>Closed</th><th>Opened</th><th /></tr></thead>
        <tbody>
          {reports.map((r) => {
            const provider = r.source.split("/")[1] ?? "";
            const d = r.detail ?? {};
            const n = (xs?: string[]) => (xs?.length ? xs.length : <span className="faint">–</span>);
            const any = [d.joined, d.left, d.disabled, d.enabled].some((x) => x?.length);
            return (
              <tr key={r.source} style={{ verticalAlign: "top" }}>
                <td className="nowrap"><Link to={objectPath("Provider", provider)} className="il" style={{ fontWeight: 600 }}><Icon name="plug" size={14} />{provider}</Link></td>
                <td className="nowrap"><StatusBadge r={r} /><div className="faint" style={{ fontSize: 12, marginTop: 3 }} title={stamp(r.ranAt)}>{ago(r.ranAt)}</div>{r.error && <div className="error" style={{ fontSize: 12 }}>{r.error}</div>}</td>
                <td style={{ color: "var(--green)" }}>{n(d.joined)}</td>
                <td style={{ color: "var(--yellow)" }}>{n(d.left)}</td>
                <td style={{ color: "var(--red)" }}>{n(d.disabled)}</td>
                <td>{n(d.enabled)}</td>
                <td>{any && (
                  <details className="problems"><summary>Who</summary>
                    <People label="Joined" xs={d.joined} color="var(--green)" />
                    <People label="Left" xs={d.left} color="var(--yellow)" />
                    <People label="Closed in the directory" xs={d.disabled} color="var(--red)" />
                    <People label="Opened again" xs={d.enabled} color="var(--fg3)" />
                  </details>
                )}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <div className="card-note">Counts are of the last sync only; an empty row means nobody joined or left since the one before.</div>
    </Card>
  );
}

// LeftInGit lists what Git still has of Gaia's settings: left out since
// the settings store took them over.
function LeftInGit({ ignored }: { ignored: { kind: string; name: string; source: string }[] }) {
  const byFile = new Map<string, { kind: string; name: string }[]>();
  for (const o of ignored) {
    const file = o.source.replace(/:\d+$/, "");
    byFile.set(file, [...(byFile.get(file) ?? []), o]);
  }
  return (
    <details className="card leftover">
      <summary>
        <span className="il" style={{ color: "var(--yellow)" }}><Icon name="alert" size={15} /></span>
        <span><b>{ignored.length} old copies of settings are still in Git.</b> <span className="muted">Gaia no longer reads them: these settings now live in Gaia. You can delete the files from the platform repository.</span></span>
      </summary>
      <table className="grid">
        <thead><tr><th>File in Git</th><th>Objects</th></tr></thead>
        <tbody>
          {[...byFile.entries()].map(([file, objs]) => (
            <tr key={file} style={{ verticalAlign: "top" }}>
              <td className="nowrap"><Mono>{file}</Mono></td>
              <td><span className="row" style={{ gap: 6 }}>{objs.map((o) => <span key={o.kind + o.name} className="badge gray" style={{ fontWeight: 500 }}><Icon name={kindOf(o.kind).icon} size={12} />{o.kind} {o.name}</span>)}</span></td>
            </tr>
          ))}
        </tbody>
      </table>
    </details>
  );
}

export function Sources() {
  const session = useSession();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["sources"], queryFn: () => api<Report[]>("/sources"), refetchInterval: 30_000 });
  const defs = useObjects(["NetworkSource", "TeamSource", "AnsibleRoleSource"]);
  const settings = useSettings();
  const [note, setNote] = useState<string | null>(null);
  const sync = async () => {
    setNote(null);
    try {
      await api("/sources/sync", { method: "POST" });
      setNote("Sync started; results show here in a few seconds.");
      setTimeout(() => void qc.invalidateQueries({ queryKey: ["sources"] }), 3000);
      setTimeout(() => void qc.invalidateQueries({ queryKey: ["sources"] }), 10000);
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e));
    }
  };
  const reports = new Map((q.data ?? []).map((r) => [r.source, r]));
  const directory = (q.data ?? []).filter((r) => r.source.startsWith("Directory/"));
  return (
    <>
      <TabHead icon="repeat" text="Sources keep Gaia's networks, teams and roles in step with the systems they come from."
        hint="A Source reads another system (IPAM, the directory, the configuration repository) every few minutes and makes Gaia's objects from it. You do not edit what a Source makes; you change it at its source."
        right={hasRole(session, "platform-admin") && <Button icon="sync" onClick={sync}>Sync now</Button>} />
      {note && <div className="notice" role="status">{note}</div>}
      {q.isPending || defs.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : (
        <>
          <SourcesCard defs={defs.data?.objects ?? []} reports={reports} />
          <DirectoryCard reports={directory} />
        </>
      )}
      {settings.data && settings.data.ignored.length > 0 && <LeftInGit ignored={settings.data.ignored} />}
    </>
  );
}
