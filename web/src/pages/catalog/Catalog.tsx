// Catalog: what can be installed on servers (design 17.8): roles, apps,
// agents and baselines as cards. ObjectPage is any catalog object's page:
// its configuration, its references and what uses it.
import { useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { api, ApiError } from "../../api/client";
import { field, fromSettings, matches, str, strs, useObjects, type CatalogObj } from "../../api/objects";
import { Icon } from "../../ui/icons";
import { Badge, Card, ErrorNote, Hint, Leg, Loading, Mono, short, StatusBar } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { kindOf, listPath, objectPath, SECTIONS, sectionOf } from "../../ui/kinds";
import { EditButtons } from "../../ui/settings-editor";
import { SpecView } from "../../ui/spec";
import { YamlButton } from "../../ui/yaml";
import { useSchema } from "../../api/schema";

type Ref = { kind: string; name: string; field: string };
type Param = {
  name: string; type?: string; elements?: string; required?: boolean; default?: unknown; choices?: unknown[]; description?: string;
  secret?: boolean; pattern?: string; format?: string; min?: number; max?: number; deprecated?: string;
};
type RoleInfo = { provider: string; role: string; os?: string[]; monitoring?: string[]; conflicts?: string[]; allowedEnvironments?: string[]; checkMode?: string; runbook?: boolean };
type Detail = CatalogObj & {
  annotations?: Record<string, string>; patchFiles?: string[]; yaml: string; refs: Ref[]; usedByRefs: Ref[];
  params?: Param[]; rules?: { expression: string; message?: string }[]; role?: RoleInfo;
};

function show(v: unknown): string {
  return typeof v === "string" ? v : JSON.stringify(v);
}

// usage is how a contract names the role or app, with its required params.
function usage(kind: string, name: string, params: Param[]): string {
  const field = kind === "App" ? "apps" : "ansibleRoles";
  const top = params.filter((p) => !p.name.includes("."));
  const req = top.filter((p) => p.required);
  if (req.length === 0) return kind === "App" ? `${field}:\n  - name: ${name}\n` : `${field}:\n  - ${name}\n`;
  const lines = req.map((p) => `      ${p.name}: ${p.secret ? "bao://kv/…#field" : p.choices?.length ? show(p.choices[0]) : p.type === "int" ? "0" : p.type === "bool" ? "true" : "…"}`);
  return `${field}:\n  - name: ${name}\n    params:\n${lines.join("\n")}\n`;
}

function Constraints({ p }: { p: Param }) {
  const parts: string[] = [];
  if (p.choices?.length) parts.push(`one of: ${p.choices.map(show).join(", ")}`);
  if (p.pattern) parts.push(`pattern: ${p.pattern}`);
  if (p.format) parts.push(`format: ${p.format}`);
  if (p.min !== undefined && p.max !== undefined) parts.push(`${p.min} – ${p.max}`);
  else if (p.min !== undefined) parts.push(`at least ${p.min}`);
  else if (p.max !== undefined) parts.push(`at most ${p.max}`);
  return parts.length ? <span className="mono" style={{ fontSize: 12 }}>{parts.join(" · ")}</span> : <span className="faint">–</span>;
}

function Params({ kind, params, rules }: { kind: string; params: Param[]; rules: Detail["rules"] }) {
  return (
    <Card title="Parameters" icon="plan" hint={<>What a contract may give this {kind === "App" ? "application" : "role"}. Gaia checks them before planning; a wrong value is refused before a change opens.
      <Leg icon="check" color="var(--yellow)" name="required">a contract must give it</Leg>
      <Leg icon="lock" color="var(--purple)" name="secret">never written in clear: a bao:// reference, masked in logs</Leg></>}>
      {params.length === 0 ? <div className="empty">It takes no parameters.</div> : (
        <table className="grid">
          <thead><tr><th>Name</th><th>Type</th><th>Default</th><th>Constraints</th><th>Description</th></tr></thead>
          <tbody>
            {params.map((p) => (
              <tr key={p.name}>
                <td className="nowrap">
                  <span className="mono" style={{ paddingLeft: (p.name.split(".").length - 1) * 16, fontWeight: 600 }}>{p.name.split(".").pop()}</span>
                  {p.required && <> <Badge tone="yellow">required</Badge></>}
                  {p.secret && <> <Badge tone="gray" icon="lock">secret</Badge></>}
                  {p.deprecated && <> <Badge tone="orange">deprecated</Badge></>}
                </td>
                <td className="mono nowrap">{p.type ?? "str"}{p.elements ? `<${p.elements}>` : ""}</td>
                <td className="mono">{p.secret ? <span className="faint">–</span> : p.default === undefined || p.default === null ? <span className="faint">–</span> : show(p.default)}</td>
                <td><Constraints p={p} /></td>
                <td className="muted">{p.description}{p.deprecated && <div style={{ color: "var(--orange)" }}>{p.deprecated}</div>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {rules && rules.length > 0 && (
        <div style={{ padding: "12px 16px", borderTop: "1px solid var(--bg1)" }}>
          <div className="label-caps row" style={{ gap: 6, marginBottom: 8 }}>Rules<Hint>Conditions between parameters (CEL). A contract that breaks one is refused with its message.</Hint></div>
          <ul style={{ margin: 0, paddingLeft: 18, display: "flex", flexDirection: "column", gap: 6 }}>
            {rules.map((r) => <li key={r.expression}><span className="mono" style={{ fontSize: 12 }}>{r.expression}</span>{r.message && <span className="muted"> — {r.message}</span>}</li>)}
          </ul>
        </div>
      )}
    </Card>
  );
}

function RoleCard({ role }: { role: RoleInfo }) {
  const row = (k: string, v: ReactNode) => <><dt className="faint">{k}</dt><dd style={{ margin: 0 }}>{v}</dd></>;
  const list = (xs?: string[]) => (xs?.length ? <span className="row" style={{ gap: 4 }}>{xs.map((x) => <span key={x} className="badge gray">{x}</span>)}</span> : <span className="faint">–</span>);
  return (
    <Card title="Role" icon="wrench" hint="The role's name in the configuration repository, and where it may be used.">
      <dl style={{ margin: 0, padding: 16, display: "grid", gridTemplateColumns: "max-content minmax(0, 1fr)", gap: "8px 14px" }}>
        {row("Source", <><span className="mono">{role.role}</span> <span className="faint">· {role.provider}</span></>)}
        {row("Operating systems", list(role.os))}
        {row("Environments", role.allowedEnvironments?.length ? list(role.allowedEnvironments) : <span className="muted">all</span>)}
        {row("Monitoring", list(role.monitoring))}
        {row("Conflicts with", list(role.conflicts))}
        {row("Check mode", role.checkMode ? <span>{role.checkMode}</span> : <span className="faint">–</span>)}
      </dl>
    </Card>
  );
}

const CONTRACT_KINDS = new Set(["Server", "Operation", "OperationSchedule"]);

export function refLink(r: { kind: string; name: string }): string {
  return objectPath(r.kind, r.name);
}

export function Labels({ labels }: { labels?: Record<string, string> }) {
  return (
    <span className="row" style={{ gap: 4 }}>
      {Object.entries(labels ?? {}).map(([k, v]) => <span key={k} className="badge gray" style={{ fontWeight: 500 }}>{k}={v}</span>)}
    </span>
  );
}

// What the catalog offers, in the order a contract adds them.
const INSTALL: { kind: string; label: string; plural: string }[] = [
  { kind: "AnsibleRole", label: "Role", plural: "Roles" },
  { kind: "App", label: "App", plural: "Apps" },
  { kind: "Agent", label: "Agent", plural: "Agents" },
  { kind: "Baseline", label: "Baseline", plural: "Baselines" },
];
const INSTALL_KINDS = INSTALL.map((k) => k.kind);

function osOf(o: CatalogObj): string[] {
  return strs(field(o.spec, "os"));
}

function Tile({ o }: { o: CatalogObj }) {
  const k = kindOf(o.kind);
  const text = str(field(o.spec, "description"));
  const os = osOf(o);
  return (
    <Link to={objectPath(o.kind, o.name)} className="tile" aria-label={`${o.kind} ${o.name}`}>
      <div className="tile-head">
        <span className="tile-icon"><Icon name={k.icon} size={18} /></span>
        <h3 title={o.name}>{o.name}</h3>
        <span className="badge gray">{INSTALL.find((i) => i.kind === o.kind)?.label ?? o.kind}</span>
      </div>
      <div className="tile-text">{text || <span className="faint">No description.</span>}</div>
      <div className="row" style={{ gap: 6, minHeight: 24 }}>
        {o.labels?.category && <span className="badge blue">{o.labels.category}</span>}
        {os.map((x) => <span key={x} className="badge gray" title="Operating system"><Icon name="disc" size={12} />{x}</span>)}
        {field(o.spec, "mandatory") === true && <span className="badge yellow" title="Installed on every server">mandatory</span>}
      </div>
      <div className="tile-foot">
        <span className="il" style={{ fontWeight: 500 }} title="Parameters"><Icon name="plan" size={14} />{o.params ?? 0}</span>
        <span className="il" style={{ fontWeight: 500 }} title="Contracts using it"><Icon name="file" size={14} />{o.contracts}</span>
        {o.generatedBy && <span className="il push" style={{ fontWeight: 500 }} title={o.generatedBy}><Icon name="repeat" size={14} />generated</span>}
      </div>
    </Link>
  );
}

export function CatalogList() {
  const [params, setParams] = useSearchParams();
  const kind = params.get("kind") ?? "";
  const [q, setQ] = useState(params.get("q") ?? "");
  const all = useObjects(INSTALL_KINDS);
  const counts = useMemo(() => {
    const m = new Map<string, number>();
    for (const o of all.data?.objects ?? []) m.set(o.kind, (m.get(o.kind) ?? 0) + 1);
    return m;
  }, [all.data]);
  const shown = (all.data?.objects ?? []).filter((o) => (!kind || o.kind === kind) && matches(o, q, [str(field(o.spec, "description")), ...osOf(o)].join(" ")));
  const pick = (k: string) => setParams(k ? { kind: k } : {});
  return (
    <Page crumbs="Catalog">
      <div className="title-row">
        <h1>Catalog</h1>
        <Hint width={340}>What can be installed on servers: Ansible roles, applications, agents and baselines. A contract asks for them by name; each card's page shows its parameters.
          <Leg icon="plan" color="var(--fg4)" name="parameters">how many values a contract may give</Leg>
          <Leg icon="file" color="var(--fg4)" name="contracts">how many contracts use it</Leg>
          <Leg icon="repeat" color="var(--fg4)" name="generated">a Source made it from the configuration repository</Leg>
          <Leg icon="disc" color="var(--fg4)" name="operating system">where it can be installed</Leg></Hint>
        {all.data && <span className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={15} />platform @ <Mono>{short(all.data.commit)}</Mono></span>}
      </div>
      {all.isPending ? <Loading /> : all.error ? <CatalogError e={all.error} /> : (
        <>
          <div className="spread">
            <div className="chips" role="group" aria-label="Kinds">
              <button type="button" className="chip" aria-pressed={!kind} onClick={() => pick("")}><Icon name="grid" size={14} />All<span className="tab-count">{all.data.objects.length}</span></button>
              {INSTALL.map((i) => (
                <button key={i.kind} type="button" className="chip" aria-pressed={kind === i.kind} title={kindOf(i.kind).text} onClick={() => pick(i.kind)}>
                  <Icon name={kindOf(i.kind).icon} size={14} />{i.plural}<span className="tab-count">{counts.get(i.kind) ?? 0}</span>
                </button>
              ))}
            </div>
            <label className="search" style={{ width: 340, height: 36 }}>
              <Icon name="filter" size={14} />
              <span className="sr-only">Filter</span>
              <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Name, description or category=web" />
              <Hint right width={300}>Filters by name, description, operating system, or labels as <b className="term">key=value</b>. Every space-separated condition must hold.</Hint>
            </label>
          </div>
          {shown.length === 0 ? <div className="card empty">Nothing matches.</div> : <div className="tiles">{shown.map((o) => <Tile key={o.kind + "/" + o.name} o={o} />)}</div>}
        </>
      )}
    </Page>
  );
}

function CatalogError({ e }: { e: unknown }) {
  if (e instanceof ApiError && e.status === 503) return <div className="notice warn">This server follows no repository; there is no catalog.</div>;
  if (e instanceof ApiError && e.status === 404) return <div className="notice warn">There is no such object.</div>;
  return <ErrorNote error={e} />;
}

function RefList({ refs, empty }: { refs: Ref[]; empty: string }) {
  if (refs.length === 0) return <div className="empty">{empty}</div>;
  return (
    <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
      {refs.map((r, i) => (
        <li key={i} className="row" style={{ gap: 10, padding: "10px 16px", borderTop: i ? "1px solid var(--bg1)" : undefined }}>
          <span className="faint"><Icon name={CONTRACT_KINDS.has(r.kind) ? "file" : kindOf(r.kind).icon} size={15} /></span>
          <Link to={refLink(r)}><span className="faint">{r.kind}/</span>{r.name}</Link>
          <span className="mono faint">{r.field}</span>
        </li>
      ))}
    </ul>
  );
}

export function ObjectPage() {
  const { kind = "", name = "" } = useParams();
  const q = useQuery({ queryKey: ["catalog", kind, name], queryFn: () => api<Detail>(`/catalog/${encodeURIComponent(kind)}/${encodeURIComponent(name)}`) });
  const schema = useSchema(kind);
  const k = kindOf(kind);
  const sec = SECTIONS[sectionOf(kind)];
  return (
    <Page crumbs={<><Link to={sec.path}>{sec.label}</Link> / <Link to={listPath(kind)}>{kind}</Link> / {name}</>}>
      <div className="spread">
        <div className="title-row">
          <span style={{ color: "var(--blue)" }}><Icon name={k.icon} size={22} /></span>
          <h1>{name}</h1>
          <span className="badge gray">{kind}</span>
          {k.text && <Hint>{k.text}</Hint>}
          {q.data && <Labels labels={q.data.labels} />}
        </div>
        {q.data && (
          <span className="row">
            <YamlButton yaml={q.data.yaml} title={`${kind} ${name}`} />
            {fromSettings(q.data.source) && <EditButtons kind={kind} name={name} generatedBy={q.data.generatedBy} />}
          </span>
        )}
      </div>
      {q.isPending ? <Loading /> : q.error ? <CatalogError e={q.error} /> : (
        <>
          <StatusBar items={[
            fromSettings(q.data.source)
              ? { icon: "history", title: "Stored in", hint: "This object is one of Gaia's settings: kept in Gaia's settings store, in versions, not in Git.", value: <span style={{ fontSize: 15 }}>Settings store</span> }
              : { icon: "file", title: "File", hint: "Where the object is in the platform repository.", value: <Mono>{q.data.source}</Mono> },
            {
              icon: "repeat", title: "Written by", hint: "The Source that wrote it, if one did. Patches are hand-made changes on top of a generated object.",
              value: <span style={{ fontSize: 15 }}>{q.data.generatedBy ?? "by hand"}</span>,
              sub: q.data.patchFiles?.length ? `${q.data.patchFiles.length} patches` : "",
            },
            { icon: "arrow", title: "Refers to", hint: "The objects it names.", value: q.data.refs.length },
            { icon: "network", title: "Used by", hint: "The objects and contracts that name it.", value: q.data.usedByRefs.length },
          ]} />
          <div className="row" style={{ alignItems: "flex-start", gap: 16 }}>
            <div style={{ flex: "2 1 520px", minWidth: 0, display: "flex", flexDirection: "column", gap: 16 }}>
              <Card title="Configuration" icon="plan" hint="The object's settings, field by field.">
                <SpecView kind={kind} schema={schema.data} spec={q.data.spec} />
              </Card>
              {q.data.params && <Params kind={kind} params={q.data.params} rules={q.data.rules} />}
            </div>
            <div style={{ flex: "1 1 320px", display: "flex", flexDirection: "column", gap: 16 }}>
              {q.data.role && <RoleCard role={q.data.role} />}
              {q.data.params && (
                <Card title="In a contract" icon="file" hint="What to add to a contract's spec to use it; fill in the required parameters.">
                  <details className="snippet"><summary>Show the contract snippet</summary><pre className="mono">{usage(kind, name, q.data.params)}</pre></details>
                </Card>
              )}
              <Card title="Refers to" icon="arrow"><RefList refs={q.data.refs} empty="It names no other object." /></Card>
              <Card title="Used by" icon="network"><RefList refs={q.data.usedByRefs} empty="Nothing uses it." /></Card>
            </div>
          </div>
        </>
      )}
    </Page>
  );
}
