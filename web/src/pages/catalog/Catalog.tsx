// Katalog: the desired catalog by kind, with label filters; one object's
// YAML, its references and what uses it.
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { api, ApiError } from "../../api/client";
import { Icon } from "../../ui/icons";
import { Card, ErrorNote, Hint, Loading, Mono, short, StatusBar } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { kindOf } from "../../ui/kinds";

type Obj = { kind: string; name: string; labels?: Record<string, string>; source: string; generatedBy?: string; patches?: number; usedBy: number };
type Ref = { kind: string; name: string; field: string };
type Detail = Obj & { annotations?: Record<string, string>; patchFiles?: string[]; yaml: string; refs: Ref[]; usedByRefs: Ref[] };

const CONTRACT_KINDS = new Set(["Server", "Operation", "OperationSchedule"]);

export function refLink(r: { kind: string; name: string }): string {
  return CONTRACT_KINDS.has(r.kind) ? `/contracts/${encodeURIComponent(r.name)}` : `/catalog/${encodeURIComponent(r.kind)}/${encodeURIComponent(r.name)}`;
}

function Labels({ labels }: { labels?: Record<string, string> }) {
  return (
    <span className="row" style={{ gap: 4 }}>
      {Object.entries(labels ?? {}).map(([k, v]) => <span key={k} className="badge gray" style={{ fontWeight: 500 }}>{k}={v}</span>)}
    </span>
  );
}

// matches reads "key=value" (or just text) against an object.
function matches(o: Obj, q: string): boolean {
  const t = q.trim().toLowerCase();
  if (!t) return true;
  return t.split(/\s+/).every((part) => {
    const [k, v] = part.split("=");
    if (v !== undefined) return (o.labels?.[k ?? ""] ?? "").toLowerCase() === v;
    return o.name.toLowerCase().includes(part) || Object.entries(o.labels ?? {}).some(([a, b]) => `${a}=${b}`.toLowerCase().includes(part));
  });
}

export function CatalogList() {
  const [params, setParams] = useSearchParams();
  const kind = params.get("kind") ?? "";
  const [q, setQ] = useState(params.get("q") ?? "");
  const all = useQuery({ queryKey: ["catalog"], queryFn: () => api<{ commit: string; objects: Obj[] }>("/catalog") });
  const counts = useMemo(() => {
    const m = new Map<string, number>();
    for (const o of all.data?.objects ?? []) m.set(o.kind, (m.get(o.kind) ?? 0) + 1);
    return m;
  }, [all.data]);
  const shown = (all.data?.objects ?? []).filter((o) => (!kind || o.kind === kind) && matches(o, q));
  return (
    <Page crumbs="Katalog">
      <div className="title-row">
        <h1>Katalog</h1>
        <Hint>Katalog, platform reposundaki nesnelerdir: provider'lar, site'lar, ağlar, imajlar, roller… Kontratlar bunlara sadece adıyla referans verir.</Hint>
        {all.data && <span className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={15} />platform @ <Mono>{short(all.data.commit)}</Mono></span>}
      </div>
      {all.isPending ? <Loading /> : all.error ? <CatalogError e={all.error} /> : (
        <div className="row" style={{ alignItems: "flex-start", gap: 16 }}>
          <nav className="card" aria-label="Türler" style={{ flex: "0 0 220px", padding: "8px 0" }}>
            <button type="button" className="kindrow" aria-pressed={!kind} onClick={() => setParams(q ? { q } : {})}>
              <Icon name="grid" size={15} /><span>Hepsi</span><span className="faint">{all.data.objects.length}</span>
            </button>
            {[...counts.entries()].map(([k, n]) => (
              <button key={k} type="button" className="kindrow" aria-pressed={kind === k} title={kindOf(k).text} onClick={() => setParams(q ? { kind: k, q } : { kind: k })}>
                <Icon name={kindOf(k).icon} size={15} /><span>{k}</span><span className="faint">{n}</span>
              </button>
            ))}
          </nav>
          <div style={{ flex: "1 1 520px", minWidth: 0, display: "flex", flexDirection: "column", gap: 12 }}>
            <label className="search" style={{ width: "100%", height: 36 }}>
              <Icon name="filter" size={14} />
              <span className="sr-only">Filtre</span>
              <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Ad ya da etiket: owner=team-platform env=prod" />
              <Hint right width={300}>Ada göre ya da <b className="term">anahtar=değer</b> biçiminde etiketlere göre süzer. Boşlukla ayrılan koşulların hepsi sağlanmalı.</Hint>
            </label>
            <Card title={kind || "Bütün nesneler"} icon={kind ? kindOf(kind).icon : "book"} hint={kind ? kindOf(kind).text : undefined} right={<span className="faint">{shown.length} nesne</span>}>
              {shown.length === 0 ? <div className="empty">Eşleşen nesne yok.</div> : (
                <table className="grid">
                  <thead>
                    <tr>
                      <th>Nesne</th><th>Etiketler</th>
                      <th><span className="th-hint">Kaynak<Hint>Nesnenin yazıldığı dosya; bir Source ürettiyse o Source.</Hint></span></th>
                      <th><span className="th-hint">Kullanan<Hint right>Bu nesneye referans veren nesne sayısı.</Hint></span></th>
                    </tr>
                  </thead>
                  <tbody>
                    {shown.map((o) => (
                      <tr key={o.kind + "/" + o.name}>
                        <td><Link to={refLink(o)} className="il" style={{ fontWeight: 600 }}><span className="faint"><Icon name={kindOf(o.kind).icon} size={15} /></span><span className="faint" style={{ fontWeight: 400 }}>{o.kind}/</span>{o.name}</Link></td>
                        <td><Labels labels={o.labels} /></td>
                        <td className="muted">{o.generatedBy ? <span className="il" style={{ fontWeight: 400 }}><Icon name="repeat" size={14} />{o.generatedBy}{o.patches ? ` + ${o.patches} patch` : ""}</span> : <Mono>{o.source}</Mono>}</td>
                        <td>{o.usedBy}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </Card>
          </div>
        </div>
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

export function CatalogObject() {
  const { kind = "", name = "" } = useParams();
  const q = useQuery({ queryKey: ["catalog", kind, name], queryFn: () => api<Detail>(`/catalog/${encodeURIComponent(kind)}/${encodeURIComponent(name)}`) });
  const k = kindOf(kind);
  return (
    <Page crumbs={<><Link to="/catalog">Katalog</Link> / <Link to={`/catalog?kind=${encodeURIComponent(kind)}`}>{kind}</Link> / {name}</>}>
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
            { icon: "file", title: "Dosya", hint: "Nesnenin platform reposundaki yeri.", value: <Mono>{q.data.source}</Mono> },
            {
              icon: "repeat", title: "Üreten", hint: "Nesneyi bir Source ürettiyse o Source; elle yazılmışsa boş. Patch'ler üretilen nesneye elle eklenen değişikliklerdir.",
              value: <span style={{ fontSize: 15 }}>{q.data.generatedBy ?? "elle yazılmış"}</span>,
              sub: q.data.patchFiles?.length ? `${q.data.patchFiles.length} patch: ${q.data.patchFiles.join(", ")}` : "",
            },
            { icon: "arrow", title: "Referansları", hint: "Bu nesnenin adıyla gösterdiği nesneler.", value: q.data.refs.length },
            { icon: "network", title: "Kullanan", hint: "Bu nesneye referans veren nesneler ve kontratlar.", value: q.data.usedByRefs.length },
          ]} />
          <div className="row" style={{ alignItems: "flex-start", gap: 16 }}>
            <section className="card" style={{ flex: "2 1 520px", minWidth: 0 }} aria-label="YAML">
              <div className="card-head"><div className="card-title"><Icon name="code" size={16} /><h2>YAML</h2></div></div>
              <pre className="mono" style={{ margin: 0, padding: 16, overflowX: "auto", color: "var(--fg2)", lineHeight: 1.6 }}>{q.data.yaml}</pre>
            </section>
            <div style={{ flex: "1 1 320px", display: "flex", flexDirection: "column", gap: 16 }}>
              <Card title="Referansları" icon="arrow"><RefList refs={q.data.refs} empty="Başka nesneye referans vermiyor." /></Card>
              <Card title="Kullananlar" icon="network"><RefList refs={q.data.usedByRefs} empty="Bu nesneyi kullanan yok." /></Card>
            </div>
          </div>
        </>
      )}
    </Page>
  );
}

