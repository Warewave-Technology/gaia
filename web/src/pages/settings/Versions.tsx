// Settings → Versions: every version of Gaia's settings, who wrote it and
// why (design 17.2); a version's page shows what it changed, field by
// field, and the objects it holds.
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { api } from "../../api/client";
import type { SettingsVersion } from "../../api/objects";
import { Icon } from "../../ui/icons";
import { Badge, Card, ErrorNote, Hint, IL, Loading, Mono, short } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { TabHead } from "../../ui/tabs";
import { kindOf, objectPath } from "../../ui/kinds";
import { FieldDiff, fieldDiff, parseYAML } from "../../ui/spec";
import { YamlButton } from "../../ui/yaml";
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
      <TabHead icon="history" text="Every version of the settings: who wrote it, when and why."
        hint={<>Each applied settings change is a new version; earlier versions are kept. A change or a run carries the version it was planned with.
          <span style={{ display: "block", marginTop: 6 }}>A version is written by a person (an approved settings change), by Gaia (the import from Git), or by a Source (networks from IPAM, teams from the directory).</span></>} />
      <Card title="Versions" icon="history" right={<span className="faint">{q.data?.length ?? 0} versions</span>}>
        {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : q.data.length === 0 ? <div className="empty">This Gaia still reads its settings from Git.</div> : (
          <table className="grid">
            <thead><tr><th>Version</th><th>Written by</th><th>Why</th><th>Objects</th><th>When</th></tr></thead>
            <tbody>
              {q.data.map((v, i) => (
                <tr key={v.version}>
                  <td className="nowrap"><Link to={`/settings/versions/${v.version}`} style={{ fontWeight: 600 }}>v{v.version}</Link>{i === 0 && <span className="badge green" style={{ marginLeft: 8 }}>current</span>}</td>
                  <td className="nowrap"><Author who={v.author} /></td>
                  <td>{v.proposal ? <Link to={`/changes/settings/${encodeURIComponent(v.proposal)}`}>{v.reason}</Link> : v.reason}</td>
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

type VersionFile = { path: string; kind: string; yaml: string };
type VersionDetail = SettingsVersion & { files: VersionFile[] };

function nameOf(path: string): string {
  return path.slice(path.indexOf("/") + 1).replace(/\.yaml$/, "");
}

export function VersionPage() {
  const { version = "" } = useParams();
  const n = Number(version);
  const q = useQuery({ queryKey: ["settings", "versions", version], queryFn: () => api<VersionDetail>(`/settings/versions/${encodeURIComponent(version)}`) });
  const prev = useQuery({ queryKey: ["settings", "versions", String(n - 1)], queryFn: () => api<VersionDetail>(`/settings/versions/${n - 1}`), enabled: n > 1 });
  const before = new Map((prev.data?.files ?? []).map((f) => [f.path, f]));
  const changed = (q.data?.files ?? []).filter((f) => before.get(f.path)?.yaml !== f.yaml).map((f) => ({ f, op: before.has(f.path) ? "changed" : "added" }));
  const removed = n > 1 && q.data ? [...before.values()].filter((f) => !q.data.files.some((x) => x.path === f.path)) : [];
  return (
    <Page crumbs={<><Link to="/settings">Settings</Link> / <Link to="/settings/versions">Versions</Link> / v{version}</>}>
      <div className="title-row">
        <span style={{ color: "var(--blue)" }}><Icon name="history" size={22} /></span>
        <h1>Settings v{version}</h1>
        {q.data && <span className="il muted" style={{ fontWeight: 400 }}><Mono>{short(q.data.contentHash.replace("sha256:", ""), 12)}</Mono><Hint>The hash of the version's content. The same content gives the same hash.</Hint></span>}
      </div>
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : (
        <>
          <section className="status" aria-label="Version">
            <div className="status-cell"><div className="status-title"><Icon name="user" size={14} /><span>Written by</span></div><div className="status-value" style={{ fontSize: 15 }}><Author who={q.data.author} /></div></div>
            <div className="status-cell"><div className="status-title"><Icon name="info" size={14} /><span>Why</span></div><div className="status-value" style={{ fontSize: 15 }}>{q.data.proposal ? <Link to={`/changes/settings/${encodeURIComponent(q.data.proposal)}`}>{q.data.reason}</Link> : q.data.reason}</div></div>
            <div className="status-cell"><div className="status-title"><Icon name="clock" size={14} /><span>When</span></div><div className="status-value" style={{ fontSize: 15 }}>{ago(q.data.createdAt)}</div><div className="status-sub">{stamp(q.data.createdAt)}</div></div>
            <div className="status-cell"><div className="status-title"><Icon name="box" size={14} /><span>Objects</span></div><div className="status-value">{q.data.files.length}</div></div>
          </section>
          {n > 1 && (
            <Card title={`What v${n} changed`} icon="diff" hint={`Compared with v${n - 1}, field by field.`}>
              {prev.isPending ? <Loading /> : changed.length + removed.length === 0 ? <div className="empty">Nothing changed.</div> : (
                <div className="version-diff">
                  {changed.map(({ f, op }) => (
                    <section key={f.path}>
                      <div className="row version-obj">
                        <Icon name={kindOf(f.kind).icon} size={15} />
                        <Link to={objectPath(f.kind, nameOf(f.path))}><span className="faint">{f.kind} </span>{nameOf(f.path)}</Link>
                        <Badge tone={op === "added" ? "green" : "yellow"}>{op}</Badge>
                      </div>
                      <FieldDiff rows={fieldDiff(parseYAML(before.get(f.path)?.yaml)?.spec, parseYAML(f.yaml)?.spec)} />
                    </section>
                  ))}
                  {removed.map((f) => (
                    <section key={f.path}>
                      <div className="row version-obj"><Icon name={kindOf(f.kind).icon} size={15} /><span><span className="faint">{f.kind} </span>{nameOf(f.path)}</span><Badge tone="red">removed</Badge></div>
                    </section>
                  ))}
                </div>
              )}
            </Card>
          )}
          <Card title="Objects in this version" icon="box">
            <table className="grid">
              <thead><tr><th>Kind</th><th>Name</th><th /></tr></thead>
              <tbody>
                {q.data.files.map((f) => (
                  <tr key={f.path}>
                    <td className="nowrap"><span className="il" style={{ fontWeight: 500 }}><span className="faint"><Icon name={kindOf(f.kind).icon} size={14} /></span>{f.kind}</span></td>
                    <td>{nameOf(f.path)}{f.path.endsWith(".patch.yaml") && <span className="faint"> (patch)</span>}</td>
                    <td style={{ textAlign: "right" }}><YamlButton yaml={f.yaml} title={`${f.kind} ${nameOf(f.path)} in v${version}`} label="YAML" /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        </>
      )}
    </Page>
  );
}
