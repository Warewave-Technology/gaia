// The list screens' parts: a table of catalog objects with the kind's own
// columns, and the small cells they are made of.
import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { ApiError } from "../api/client";
import type { CatalogObj } from "../api/objects";
import { Icon } from "./icons";
import { ErrorNote, Hint, IL } from "./parts";
import { kindOf, objectPath } from "./kinds";

export type Col = { title: string; hint?: string; cell: (o: CatalogObj) => ReactNode; nowrap?: boolean };

export const dash = <span className="faint">–</span>;

export function Ref({ kind, name }: { kind: string; name: string }) {
  if (!name) return dash;
  return <Link to={objectPath(kind, name)} className="il" style={{ fontWeight: 500 }}><Icon name={kindOf(kind).icon} size={14} />{name}</Link>;
}

export function List({ xs, mono }: { xs: string[]; mono?: boolean }) {
  if (xs.length === 0) return dash;
  return <span className="row" style={{ gap: 4 }}>{xs.map((x) => <span key={x} className={mono ? "badge gray mono" : "badge gray"} style={{ fontWeight: 500 }}>{x}</span>)}</span>;
}

// ObjectTable is a tab's list: the name, the kind's own columns, and how
// many contracts use each object.
export function ObjectTable({ kind, title, hint, cols, objs, right }: { kind: string; title: string; hint: ReactNode; cols: Col[]; objs: CatalogObj[]; right?: ReactNode }) {
  return (
    <section className="card" aria-label={title}>
      <div className="card-head">
        <div className="card-title"><Icon name={kindOf(kind).icon} size={16} /><h2>{title}</h2><Hint>{hint}</Hint></div>
        {right}
      </div>
      {objs.length === 0 ? <div className="empty">None yet.</div> : (
        <table className="grid">
          <thead>
            <tr>
              <th>Name</th>
              {cols.map((c) => <th key={c.title}>{c.hint ? <span className="th-hint">{c.title}<Hint>{c.hint}</Hint></span> : c.title}</th>)}
              <th><span className="th-hint">Used by<Hint right>How many contracts use it.</Hint></span></th>
            </tr>
          </thead>
          <tbody>
            {objs.map((o) => (
              <tr key={o.name}>
                <td className="nowrap">
                  <Link to={objectPath(o.kind, o.name)} style={{ fontWeight: 600 }}>{o.name}</Link>
                  {o.generatedBy && <span className="faint" title={`Written by ${o.generatedBy}`} style={{ marginLeft: 6, verticalAlign: "middle", display: "inline-block" }}><Icon name="repeat" size={13} /></span>}
                </td>
                {cols.map((c) => <td key={c.title} className={c.nowrap ? "nowrap" : undefined}>{c.cell(o)}</td>)}
                <td><IL icon="file" size={14} weight={500} color="var(--fg4)">{o.contracts}</IL></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}


export function ObjectsError({ error }: { error: unknown }) {
  if (error instanceof ApiError && error.status === 503) return <div className="notice warn">This server follows no repository; there is nothing to show.</div>;
  return <ErrorNote error={error} />;
}
