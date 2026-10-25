// An object's spec without YAML: SpecView shows it as labelled values and
// tables, SpecForm edits it as a form, both from the kind's schema
// (GET /schema/{kind}). fieldDiff says what a change does field by field.
import { useId, useState, type KeyboardEvent, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { parse, stringify } from "yaml";
import { help, label, type Field } from "../api/schema";
import { useObjects } from "../api/objects";
import { Icon } from "./icons";
import { Button } from "./parts";
import { Switch } from "./modal";
import { objectPath } from "./kinds";

type Obj = Record<string, unknown>;
type Path = (string | number)[];

const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const scalar = (f: Field) => ["string", "int", "float", "bool"].includes(f.type);
const empty = (v: unknown) => v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0) || (isObj(v) && Object.keys(v).length === 0);

function show(v: unknown): string {
  if (v === undefined || v === null) return "";
  if (typeof v === "boolean") return v ? "Yes" : "No";
  if (typeof v === "string" || typeof v === "number") return String(v);
  return stringify(v).trim();
}

// helpPath is a path as the help texts name it: list items as [].
function helpPath(path: Path): string {
  return path.map((p) => (typeof p === "number" ? "[]" : p)).join(".").replace(/\.\[\]/g, "[]");
}

// helpAt finds a field's help; inside a map the entry's own key is not
// part of the help's path (Policy.environments.approval).
function helpAt(kind: string, path: Path): string | undefined {
  const h = help(kind, helpPath(path));
  if (h) return h;
  for (let i = path.length - 2; i > 0; i--) {
    const x = help(kind, helpPath([...path.slice(0, i), ...path.slice(i + 1)]));
    if (x) return x;
  }
  return undefined;
}

// ---------------------------------------------------------------- view

function Value({ f, v }: { f: Field; v: unknown }) {
  if (f.type === "bool") return <span>{v ? "Yes" : "No"}</span>;
  if (f.ref && typeof v === "string") return <Link to={objectPath(f.ref, v)} className="il" style={{ fontWeight: 500 }}>{v}</Link>;
  if (f.type === "list" && Array.isArray(v) && f.items && scalar(f.items)) {
    return <span className="row" style={{ gap: 4 }}>{v.map((x, i) => (f.items!.ref ? <Link key={i} to={objectPath(f.items!.ref, String(x))} className="badge gray">{String(x)}</Link> : <span key={i} className="badge gray">{String(x)}</span>))}</span>;
  }
  return <span className={typeof v === "string" && /^(bao|ldaps?|https?|file):\/\//.test(v) ? "mono" : undefined}>{show(v)}</span>;
}

function ObjectView({ f, v, kind, path }: { f: Field; v: Obj; kind: string; path: Path }) {
  const fields = f.fields ?? [];
  const known = new Set(fields.map((x) => x.key));
  const extra = Object.keys(v).filter((k) => !known.has(k));
  const rows = [...fields.filter((x) => !empty(v[x.key!])), ...extra.map((k): Field => ({ key: k, type: "any" }))];
  if (rows.length === 0) return <span className="faint">–</span>;
  return (
    <dl className="spec">
      {rows.map((x) => {
        const val = v[x.key!];
        return (
          <div key={x.key} className="spec-row">
            <dt>{label(x.key!)}</dt>
            <dd><FieldView f={x} v={val} kind={kind} path={[...path, x.key!]} /></dd>
          </div>
        );
      })}
    </dl>
  );
}

function FieldView({ f, v, kind, path }: { f: Field; v: unknown; kind: string; path: Path }) {
  if (f.type === "object" && isObj(v)) return <ObjectView f={f} v={v} kind={kind} path={path} />;
  if (f.type === "list" && Array.isArray(v) && f.items?.type === "object") return <ListTable f={f.items} v={v} kind={kind} path={path} />;
  if (f.type === "map" && isObj(v)) {
    const item = f.items ?? { type: "any" as const };
    return (
      <dl className="spec">
        {Object.entries(v).map(([k, x]) => (
          <div key={k} className="spec-row"><dt className="mono">{k}</dt><dd><FieldView f={item} v={x} kind={kind} path={[...path, k]} /></dd></div>
        ))}
      </dl>
    );
  }
  if (f.type === "any" && (isObj(v) || Array.isArray(v))) return <pre className="mono spec-pre">{show(v)}</pre>;
  return <Value f={f} v={v} />;
}

function ListTable({ f, v, kind, path }: { f: Field; v: unknown[]; kind: string; path: Path }) {
  const cols = (f.fields ?? []).filter((c) => v.some((x) => isObj(x) && !empty(x[c.key!])));
  return (
    <table className="grid spec-table">
      <thead><tr>{cols.map((c) => <th key={c.key}>{label(c.key!)}</th>)}</tr></thead>
      <tbody>
        {v.map((x, i) => (
          <tr key={i}>{cols.map((c) => <td key={c.key}>{isObj(x) && !empty(x[c.key!]) ? <FieldView f={c} v={x[c.key!]} kind={kind} path={[...path, i, c.key!]} /> : <span className="faint">–</span>}</td>)}</tr>
        ))}
      </tbody>
    </table>
  );
}

// SpecView shows a spec as labelled values, nested objects indented and
// lists of objects as tables.
export function SpecView({ kind, schema, spec }: { kind: string; schema?: Field; spec?: Obj }) {
  if (!spec || Object.keys(spec).length === 0) return <div className="empty">No settings.</div>;
  return <div className="spec-view"><ObjectView f={schema ?? { type: "object" }} v={spec} kind={kind} path={[]} /></div>;
}

// ---------------------------------------------------------------- form

export function setIn(root: Obj, path: Path, value: unknown): Obj {
  const [head, ...rest] = path;
  const out: Obj | unknown[] = Array.isArray(root) ? [...root] : { ...root };
  const cur = (out as Record<string | number, unknown>)[head!];
  const next = rest.length === 0 ? value : setIn((cur ?? (typeof rest[0] === "number" ? [] : {})) as Obj, rest, value);
  if (next === undefined && !Array.isArray(out)) delete (out as Obj)[head as string];
  else (out as Record<string | number, unknown>)[head!] = next;
  return out as Obj;
}

export function getIn(root: unknown, path: Path): unknown {
  let v = root;
  for (const p of path) {
    if (v === null || typeof v !== "object") return undefined;
    v = (v as Record<string | number, unknown>)[p];
  }
  return v;
}

// prune drops what a form leaves empty, so that the YAML says only what
// was set.
export function prune(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(prune).filter((x) => x !== undefined);
  if (isObj(v)) {
    const out: Obj = {};
    for (const [k, x] of Object.entries(v)) {
      const p = prune(x);
      if (!empty(p)) out[k] = p;
    }
    return out;
  }
  return v === "" ? undefined : v;
}

type Ctx = { kind: string; root: Obj; set: (path: Path, v: unknown) => void };

function useRefNames(kind?: string): string[] {
  const q = useObjects(kind ? [kind] : []);
  return kind ? (q.data?.objects ?? []).filter((o) => o.kind === kind).map((o) => o.name) : [];
}

function TagInput({ value, onChange, options, mono, id }: { value: string[]; onChange: (v: string[]) => void; options?: string[]; mono?: boolean; id: string }) {
  const [text, setText] = useState("");
  const add = (t: string) => {
    const x = t.trim();
    if (x && !value.includes(x)) onChange([...value, x]);
    setText("");
  };
  const key = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      add(text);
    } else if (e.key === "Backspace" && text === "" && value.length) {
      onChange(value.slice(0, -1));
    }
  };
  return (
    <div className="tags input">
      {value.map((x) => (
        <span key={x} className={mono ? "tag mono" : "tag"}>{x}<button type="button" aria-label={`Remove ${x}`} onClick={() => onChange(value.filter((y) => y !== x))}><Icon name="x" size={12} /></button></span>
      ))}
      <input id={id} value={text} list={options ? id + "-list" : undefined} onChange={(e) => setText(e.target.value)} onKeyDown={key} onBlur={() => add(text)}
        placeholder={value.length ? "" : "Type and press Enter"} className={mono ? "mono" : undefined} />
      {options && <datalist id={id + "-list"}>{options.filter((o) => !value.includes(o)).map((o) => <option key={o} value={o} />)}</datalist>}
    </div>
  );
}

function ScalarInput({ f, value, onChange, id }: { f: Field; value: unknown; onChange: (v: unknown) => void; id: string }) {
  const names = useRefNames(f.ref);
  if (f.type === "bool") return <Switch on={value === true} label={label(f.key ?? "")} onChange={(on) => onChange(on ? true : undefined)} />;
  const options = f.enum ?? (f.ref ? names : undefined);
  if (options) {
    const cur = value === undefined || value === null ? "" : String(value);
    return (
      <select id={id} className="input" value={cur} onChange={(e) => onChange(e.target.value || undefined)}>
        <option value="">{f.required ? "Choose…" : "—"}</option>
        {!options.includes(cur) && cur && <option value={cur}>{cur}</option>}
        {options.map((o) => <option key={o} value={o}>{o}</option>)}
      </select>
    );
  }
  const num = f.type === "int" || f.type === "float";
  return (
    <input id={id} className={num ? "input" : "input"} type={num ? "number" : "text"} value={value === undefined || value === null ? "" : String(value)}
      onChange={(e) => { const t = e.target.value; onChange(t === "" ? undefined : num ? Number(t) : t); }} />
  );
}

function FieldLabel({ f, htmlFor, extra }: { f: Field; htmlFor?: string; extra?: ReactNode }) {
  return (
    <label className="sf-label" htmlFor={htmlFor}>
      {label(f.key ?? "")}{f.required && <span className="sf-req" title="Required">*</span>}{extra}
    </label>
  );
}

function Leaf({ f, path, ctx }: { f: Field; path: Path; ctx: Ctx }) {
  const id = useId();
  const v = getIn(ctx.root, path);
  const h = helpAt(ctx.kind, path);
  const names = useRefNames(f.items?.ref);
  let input: ReactNode;
  if (f.type === "list") {
    input = <TagInput id={id} value={Array.isArray(v) ? v.map(String) : []} onChange={(xs) => ctx.set(path, xs.length ? xs.map((x) => (f.items?.type === "int" ? Number(x) : x)) : undefined)} options={f.items?.enum ?? (f.items?.ref ? names : undefined)} />;
  } else if (f.type === "any") {
    input = <AnyInput id={id} value={v} onChange={(x) => ctx.set(path, x)} />;
  } else {
    input = <ScalarInput id={id} f={f} value={v} onChange={(x) => ctx.set(path, x)} />;
  }
  return (
    <div className={f.type === "bool" ? "sf-field sf-bool" : f.type === "any" ? "sf-field sf-wide" : "sf-field"}>
      <FieldLabel f={f} htmlFor={id} />
      {input}
      {h && <p className="sf-help">{h}</p>}
    </div>
  );
}

function AnyInput({ value, onChange, id }: { value: unknown; onChange: (v: unknown) => void; id: string }) {
  const [text, setText] = useState(value === undefined ? "" : stringify(value));
  const [err, setErr] = useState<string | null>(null);
  return (
    <>
      <textarea id={id} className="input mono" rows={4} value={text} spellCheck={false} onChange={(e) => setText(e.target.value)}
        onBlur={() => { try { onChange(text.trim() ? parse(text) : undefined); setErr(null); } catch (e) { setErr(e instanceof Error ? e.message : String(e)); } }} />
      {err && <span className="error">{err}</span>}
    </>
  );
}

// leafy fields sit in a section's grid; the rest are sections of their own.
const leafy = (f: Field) => scalar(f) || f.type === "any" || (f.type === "list" && !!f.items && scalar(f.items));

function Fields({ fields, path, ctx, depth }: { fields: Field[]; path: Path; ctx: Ctx; depth: number }) {
  const leaves = fields.filter(leafy);
  const groups = fields.filter((f) => !leafy(f));
  return (
    <>
      {leaves.length > 0 && <div className="sf-grid">{leaves.map((f) => <Leaf key={f.key} f={f} path={[...path, f.key!]} ctx={ctx} />)}</div>}
      {groups.map((f) => <Group key={f.key} f={f} path={[...path, f.key!]} ctx={ctx} depth={depth} />)}
    </>
  );
}

function Group({ f, path, ctx, depth }: { f: Field; path: Path; ctx: Ctx; depth: number }) {
  const h = helpAt(ctx.kind, path);
  const head = (
    <div className="sf-head">
      <h3>{label(f.key ?? "")}{f.required && <span className="sf-req">*</span>}</h3>
      {h && <p className="sf-help">{h}</p>}
    </div>
  );
  let body: ReactNode;
  if (f.type === "object") body = <Fields fields={f.fields ?? []} path={path} ctx={ctx} depth={depth + 1} />;
  else if (f.type === "list") body = <ListItems f={f} path={path} ctx={ctx} depth={depth} />;
  else body = <MapItems f={f} path={path} ctx={ctx} depth={depth} />;
  return <section className={depth === 0 ? "sf-section" : "sf-sub"}>{head}{body}</section>;
}

function itemTitle(item: Field, v: unknown, i: number): string {
  if (isObj(v)) {
    for (const k of ["name", "provider", "kind", "type"]) if (typeof v[k] === "string" && v[k]) return `${v[k]}`;
  }
  return `${label(item.key ?? "Item")} ${i + 1}`;
}

function ListItems({ f, path, ctx, depth }: { f: Field; path: Path; ctx: Ctx; depth: number }) {
  const items = (getIn(ctx.root, path) as unknown[] | undefined) ?? [];
  const item = f.items ?? { type: "any" as const };
  return (
    <div className="sf-items">
      {items.map((x, i) => (
        <div key={i} className="sf-item">
          <div className="sf-item-head">
            <span className="sf-item-title">{itemTitle(item, x, i)}</span>
            <button type="button" className="icon-btn" aria-label="Remove" title="Remove" onClick={() => ctx.set(path, items.filter((_, j) => j !== i))}><Icon name="x" size={14} /></button>
          </div>
          {item.type === "object" ? <Fields fields={item.fields ?? []} path={[...path, i]} ctx={ctx} depth={depth + 1} /> : <Leaf f={{ ...item, key: "value" }} path={[...path, i]} ctx={ctx} />}
        </div>
      ))}
      <div><Button icon="plus" onClick={() => ctx.set(path, [...items, item.type === "object" ? {} : ""])}>Add</Button></div>
    </div>
  );
}

function MapItems({ f, path, ctx, depth }: { f: Field; path: Path; ctx: Ctx; depth: number }) {
  const m = (getIn(ctx.root, path) as Obj | undefined) ?? {};
  const item = f.items ?? { type: "any" as const };
  const [key, setKey] = useState("");
  const rename = (from: string, to: string) => {
    if (!to || to === from || to in m) return;
    const next: Obj = {};
    for (const [k, v] of Object.entries(m)) next[k === from ? to : k] = v;
    ctx.set(path, next);
  };
  return (
    <div className="sf-items">
      {Object.keys(m).map((k) => (
        <div key={k} className={scalar(item) || item.type === "list" ? "sf-maprow" : "sf-item"}>
          <div className="sf-item-head">
            <input className="input mono sf-key" defaultValue={k} aria-label="Key" onBlur={(e) => rename(k, e.target.value.trim())} />
            <button type="button" className="icon-btn" aria-label="Remove" title="Remove" onClick={() => { const next = { ...m }; delete next[k]; ctx.set(path, next); }}><Icon name="x" size={14} /></button>
          </div>
          {item.type === "object" ? <Fields fields={item.fields ?? []} path={[...path, k]} ctx={ctx} depth={depth + 1} /> : <Leaf f={{ ...item, key: "value" }} path={[...path, k]} ctx={ctx} />}
        </div>
      ))}
      <div className="row">
        <input className="input mono" style={{ width: 220 }} placeholder="New key" value={key} onChange={(e) => setKey(e.target.value)} />
        <Button icon="plus" disabled={!key.trim() || key.trim() in m} onClick={() => { ctx.set(path, { ...m, [key.trim()]: item.type === "object" ? {} : item.type === "list" ? [] : "" }); setKey(""); }}>Add</Button>
      </div>
    </div>
  );
}

// SpecForm edits a spec: the object's own fields first, then a section
// for each nested part.
export function SpecForm({ kind, schema, spec, onChange }: { kind: string; schema: Field; spec: Obj; onChange: (spec: Obj) => void }) {
  const ctx: Ctx = { kind, root: spec, set: (path, v) => onChange(setIn(spec, path, v)) };
  return <div className="sf"><Fields fields={schema.fields ?? []} path={[]} ctx={ctx} depth={0} /></div>;
}

// ---------------------------------------------------------------- diff

export type FieldChange = { path: string; before?: string; after?: string };

function flatten(v: unknown, prefix: string, out: Map<string, string>) {
  if (isObj(v)) {
    for (const [k, x] of Object.entries(v)) flatten(x, prefix ? `${prefix}.${k}` : k, out);
  } else if (Array.isArray(v) && v.some((x) => isObj(x) || Array.isArray(x))) {
    v.forEach((x, i) => flatten(x, `${prefix}[${i}]`, out));
  } else if (Array.isArray(v)) {
    if (v.length) out.set(prefix, v.map(String).join(", "));
  } else if (v !== undefined && v !== null && v !== "") {
    out.set(prefix, show(v));
  }
}

// fieldDiff compares two objects field by field.
export function fieldDiff(before: unknown, after: unknown): FieldChange[] {
  const a = new Map<string, string>(), b = new Map<string, string>();
  flatten(before ?? {}, "", a);
  flatten(after ?? {}, "", b);
  const keys = [...new Set([...a.keys(), ...b.keys()])].sort();
  return keys.filter((k) => a.get(k) !== b.get(k)).map((k) => ({ path: k, before: a.get(k), after: b.get(k) }));
}

// pathLabel reads a flattened path in words: compute.storage.default →
// Compute › Storage › Default.
export function pathLabel(path: string): string {
  return path.split(".").map((p) => {
    const m = /^(.*?)\[(\d+)\]$/.exec(p);
    return m ? `${label(m[1]!)} ${Number(m[2]) + 1}` : label(p);
  }).join(" › ");
}

export function FieldDiff({ rows }: { rows: FieldChange[] }) {
  if (rows.length === 0) return <div className="empty">No field changes.</div>;
  return (
    <table className="grid">
      <thead><tr><th>Field</th><th>Before</th><th>After</th></tr></thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.path} style={{ verticalAlign: "top" }}>
            <td className="nowrap" style={{ fontWeight: 500 }}>{pathLabel(r.path)}</td>
            <td>{r.before === undefined ? <span className="faint">—</span> : <span className="diff-del-text">{r.before}</span>}</td>
            <td>{r.after === undefined ? <span className="faint">removed</span> : <span className="diff-add-text">{r.after}</span>}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// parseYAML reads an object's YAML, or nothing.
export function parseYAML(text: string | undefined): Obj | undefined {
  if (!text) return undefined;
  try {
    const v = parse(text);
    return isObj(v) ? v : undefined;
  } catch {
    return undefined;
  }
}
