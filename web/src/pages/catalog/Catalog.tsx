// Katalog: what can be installed on servers (design 17.8): roles, apps,
// agents and baselines as cards. ObjectPage is any catalog object's page:
// its YAML, its references and what uses it.
import { useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { api, ApiError } from "../../api/client";
import { field, fromSettings, matches, str, strs, useObjects, type CatalogObj } from "../../api/objects";
import { Icon } from "../../ui/icons";
import { Badge, Card, ErrorNote, Hint, Leg, Loading, Mono, short, StatusBar } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { kindOf, listPath, objectPath, SECTIONS, sectionOf } from "../../ui/kinds";

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
  const lines = req.map((p) => `      ${p.name}: ${p.secret ? "bao://kv/…#alan" : p.choices?.length ? show(p.choices[0]) : p.type === "int" ? "0" : p.type === "bool" ? "true" : "…"}`);
  return `${field}:\n  - name: ${name}\n    params:\n${lines.join("\n")}\n`;
}

function Constraints({ p }: { p: Param }) {
  const parts: string[] = [];
  if (p.choices?.length) parts.push(`şunlardan biri: ${p.choices.map(show).join(", ")}`);
  if (p.pattern) parts.push(`desen: ${p.pattern}`);
  if (p.format) parts.push(`biçim: ${p.format}`);
  if (p.min !== undefined && p.max !== undefined) parts.push(`${p.min} – ${p.max}`);
  else if (p.min !== undefined) parts.push(`en az ${p.min}`);
  else if (p.max !== undefined) parts.push(`en çok ${p.max}`);
  return parts.length ? <span className="mono" style={{ fontSize: 12 }}>{parts.join(" · ")}</span> : <span className="faint">–</span>;
}

function Params({ kind, params, rules }: { kind: string; params: Param[]; rules: Detail["rules"] }) {
  return (
    <Card title="Parametreler" icon="plan" hint={<>Kontratta bu {kind === "App" ? "uygulamaya" : "role"} verilebilecek değerler. Gaia bunları plandan önce doğrular; hatalı bir değer Change'i açmadan reddedilir.
      <Leg icon="check" color="var(--yellow)" name="zorunlu">kontrat vermek zorunda</Leg>
      <Leg icon="lock" color="var(--purple)" name="gizli">değer düz yazılmaz; bao:// referansı verilir, loglarda maskelenir</Leg></>}>
      {params.length === 0 ? <div className="empty">Parametre almıyor.</div> : (
        <table className="grid">
          <thead><tr><th>Ad</th><th>Tip</th><th>Varsayılan</th><th>Kısıtlar</th><th>Açıklama</th></tr></thead>
          <tbody>
            {params.map((p) => (
              <tr key={p.name}>
                <td className="nowrap">
                  <span className="mono" style={{ paddingLeft: (p.name.split(".").length - 1) * 16, fontWeight: 600 }}>{p.name.split(".").pop()}</span>
                  {p.required && <> <Badge tone="yellow">zorunlu</Badge></>}
                  {p.secret && <> <Badge tone="gray" icon="lock">gizli</Badge></>}
                  {p.deprecated && <> <Badge tone="orange">eskidi</Badge></>}
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
          <div className="label-caps row" style={{ gap: 6, marginBottom: 8 }}>Kurallar<Hint>Parametreler arasındaki koşullar (CEL). Sağlanmazsa mesajla reddedilir.</Hint></div>
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
    <Card title="Rol" icon="wrench" hint="Rolün config reposundaki adı ve nerede kullanılabileceği.">
      <dl style={{ margin: 0, padding: 16, display: "grid", gridTemplateColumns: "max-content minmax(0, 1fr)", gap: "8px 14px" }}>
        {row("Kaynak", <><span className="mono">{role.role}</span> <span className="faint">· {role.provider}</span></>)}
        {row("İşletim sistemi", list(role.os))}
        {row("Ortamlar", role.allowedEnvironments?.length ? list(role.allowedEnvironments) : <span className="muted">hepsi</span>)}
        {row("İzleme", list(role.monitoring))}
        {row("Çakışır", list(role.conflicts))}
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
  { kind: "AnsibleRole", label: "Rol", plural: "Roller" },
  { kind: "App", label: "Uygulama", plural: "Uygulamalar" },
  { kind: "Agent", label: "Ajan", plural: "Ajanlar" },
  { kind: "Baseline", label: "Baseline", plural: "Baseline'lar" },
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
      <div className="tile-text">{text || <span className="faint">Açıklama yok.</span>}</div>
      <div className="row" style={{ gap: 6, minHeight: 24 }}>
        {o.labels?.category && <span className="badge blue">{o.labels.category}</span>}
        {os.map((x) => <span key={x} className="badge gray" title="İşletim sistemi"><Icon name="disc" size={12} />{x}</span>)}
        {field(o.spec, "mandatory") === true && <span className="badge yellow" title="Her sunucuya kurulur">zorunlu</span>}
      </div>
      <div className="tile-foot">
        <span className="il" style={{ fontWeight: 500 }} title="Parametre"><Icon name="plan" size={14} />{o.params ?? 0}</span>
        <span className="il" style={{ fontWeight: 500 }} title="Kullanan kontrat"><Icon name="file" size={14} />{o.contracts}</span>
        {o.generatedBy && <span className="il push" style={{ fontWeight: 500 }} title={o.generatedBy}><Icon name="repeat" size={14} />üretildi</span>}
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
    <Page crumbs="Katalog">
      <div className="title-row">
        <h1>Katalog</h1>
        <Hint width={340}>Sunuculara kurulabilecekler: Ansible rolleri, uygulamalar, ajanlar ve baseline'lar. Kontrat bunları adıyla ister; parametrelerini ve örneğini kartın sayfasında görürsün.
          <Leg icon="plan" color="var(--fg4)" name="parametre">kontratın verebileceği değer sayısı</Leg>
          <Leg icon="file" color="var(--fg4)" name="kontrat">bunu kullanan kontrat sayısı</Leg>
          <Leg icon="repeat" color="var(--fg4)" name="üretildi">bir Source config reposundan üretti</Leg>
          <Leg icon="disc" color="var(--fg4)" name="işletim sistemi">kurulabildiği işletim sistemleri</Leg></Hint>
        {all.data && <span className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={15} />platform @ <Mono>{short(all.data.commit)}</Mono></span>}
      </div>
      {all.isPending ? <Loading /> : all.error ? <CatalogError e={all.error} /> : (
        <>
          <div className="spread">
            <div className="chips" role="group" aria-label="Türler">
              <button type="button" className="chip" aria-pressed={!kind} onClick={() => pick("")}><Icon name="grid" size={14} />Hepsi<span className="tab-count">{all.data.objects.length}</span></button>
              {INSTALL.map((i) => (
                <button key={i.kind} type="button" className="chip" aria-pressed={kind === i.kind} title={kindOf(i.kind).text} onClick={() => pick(i.kind)}>
                  <Icon name={kindOf(i.kind).icon} size={14} />{i.plural}<span className="tab-count">{counts.get(i.kind) ?? 0}</span>
                </button>
              ))}
            </div>
            <label className="search" style={{ width: 340, height: 36 }}>
              <Icon name="filter" size={14} />
              <span className="sr-only">Filtre</span>
              <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Ad, açıklama ya da category=web" />
              <Hint right width={300}>Ada, açıklamaya, işletim sistemine ya da <b className="term">anahtar=değer</b> biçiminde etiketlere göre süzer. Boşlukla ayrılan koşulların hepsi sağlanmalı.</Hint>
            </label>
          </div>
          {shown.length === 0 ? <div className="card empty">Eşleşen bir şey yok.</div> : <div className="tiles">{shown.map((o) => <Tile key={o.kind + "/" + o.name} o={o} />)}</div>}
        </>
      )}
    </Page>
  );
}

function CatalogError({ e }: { e: unknown }) {
  if (e instanceof ApiError && e.status === 503) return <div className="notice warn">Bu sunucu bir repo izlemiyor; katalog yok.</div>;
  if (e instanceof ApiError && e.status === 404) return <div className="notice warn">Katalogda böyle bir nesne yok.</div>;
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
  const k = kindOf(kind);
  const sec = SECTIONS[sectionOf(kind)];
  return (
    <Page crumbs={<><Link to={sec.path}>{sec.label}</Link> / <Link to={listPath(kind)}>{kind}</Link> / {name}</>}>
      <div className="title-row">
        <span style={{ color: "var(--blue)" }}><Icon name={k.icon} size={22} /></span>
        <h1>{name}</h1>
        <span className="badge gray">{kind}</span>
        {k.text && <Hint>{k.text}</Hint>}
        {q.data && <Labels labels={q.data.labels} />}
      </div>
      {q.isPending ? <Loading /> : q.error ? <CatalogError e={q.error} /> : (
        <>
          <StatusBar items={[
            fromSettings(q.data.source)
              ? { icon: "history", title: "Nerede", hint: "Bu nesne Gaia'nın ayarıdır: Git'te değil, Gaia'nın ayar deposunda sürümlü durur.", value: <span style={{ fontSize: 15 }}>Ayar deposu</span>, sub: <Mono>{q.data.source.replace(/^settings:/, "").replace(/:\d+$/, "")}</Mono> }
              : { icon: "file", title: "Dosya", hint: "Nesnenin platform reposundaki yeri.", value: <Mono>{q.data.source}</Mono> },
            {
              icon: "repeat", title: "Üreten", hint: "Nesneyi bir Source ürettiyse o Source; elle yazılmışsa boş. Patch'ler üretilen nesneye elle eklenen değişikliklerdir.",
              value: <span style={{ fontSize: 15 }}>{q.data.generatedBy ?? "elle yazılmış"}</span>,
              sub: q.data.patchFiles?.length ? `${q.data.patchFiles.length} patch: ${q.data.patchFiles.join(", ")}` : "",
            },
            { icon: "arrow", title: "Referansları", hint: "Bu nesnenin adıyla gösterdiği nesneler.", value: q.data.refs.length },
            { icon: "network", title: "Kullanan", hint: "Bu nesneye referans veren nesneler ve kontratlar.", value: q.data.usedByRefs.length },
          ]} />
          <div className="row" style={{ alignItems: "flex-start", gap: 16 }}>
            <div style={{ flex: "2 1 520px", minWidth: 0, display: "flex", flexDirection: "column", gap: 16 }}>
              {q.data.params && <Params kind={kind} params={q.data.params} rules={q.data.rules} />}
              <section className="card" aria-label="YAML">
                <div className="card-head"><div className="card-title"><Icon name="code" size={16} /><h2>YAML</h2></div></div>
                <pre className="mono" style={{ margin: 0, padding: 16, overflowX: "auto", color: "var(--fg2)", lineHeight: 1.6 }}>{q.data.yaml}</pre>
              </section>
            </div>
            <div style={{ flex: "1 1 320px", display: "flex", flexDirection: "column", gap: 16 }}>
              {q.data.role && <RoleCard role={q.data.role} />}
              {q.data.params && (
                <Card title="Kontratta kullanımı" icon="file" hint="Kontratın spec'ine eklenecek kısım; zorunlu parametreler doldurulmalı.">
                  <pre className="mono" style={{ margin: 0, padding: 16, color: "var(--fg2)", lineHeight: 1.6 }}>{usage(kind, name, q.data.params)}</pre>
                </Card>
              )}
              <Card title="Referansları" icon="arrow"><RefList refs={q.data.refs} empty="Başka nesneye referans vermiyor." /></Card>
              <Card title="Kullananlar" icon="network"><RefList refs={q.data.usedByRefs} empty="Bu nesneyi kullanan yok." /></Card>
            </div>
          </div>
        </>
      )}
    </Page>
  );
}

