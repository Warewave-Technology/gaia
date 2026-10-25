// Editing Gaia's settings (design 17.3) without YAML: a form made from the
// kind's schema, then a review of what changes field by field, which
// contracts' plans it changes, and a summary for the approver. The YAML
// is there for whoever asks for it.
import { useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { parse, stringify } from "yaml";
import { api, ApiError } from "../api/client";
import { hasRole, useSession } from "../api/session";
import { useSettings } from "../api/objects";
import { useSchema, type Field } from "../api/schema";
import { checkSettings, propose, type Check, type Edit } from "../api/proposals";
import { Icon } from "./icons";
import { Badge, Button, Loading } from "./parts";
import { Modal } from "./modal";
import { kindOf, objectPath } from "./kinds";
import { FieldDiff, fieldDiff, prune, SpecForm } from "./spec";

type Obj = Record<string, unknown>;

// useCanPropose says whether the person may propose settings changes:
// operators and platform admins, once the settings store is in use.
export function useCanPropose(): boolean {
  const session = useSession();
  const settings = useSettings();
  return !!settings.data?.current && (hasRole(session, "operator") || hasRole(session, "platform-admin"));
}

const LABELS_FIELD: Field = { key: "labels", type: "map", items: { type: "string" } };

function CheckResult({ c }: { c: Check }) {
  if (c.errors.length > 0) {
    return (
      <div className="notice err" role="alert" style={{ margin: 0 }}>
        <b>This change cannot be submitted yet.</b>
        <ul style={{ margin: "6px 0 0", paddingLeft: 18 }}>{c.errors.map((e) => <li key={e}>{e}</li>)}</ul>
      </div>
    );
  }
  return (
    <div className="review-grid">
      <div className="review-cell">
        <span className="label-caps">Validation</span>
        <span className="il"><Icon name="check-circle" size={15} color="var(--green)" />The settings load with this change.</span>
      </div>
      <div className="review-cell">
        <span className="label-caps">Affected contracts</span>
        {c.impact.length === 0 ? <span className="muted">No contract's plan changes.</span> : (
          <>
            <span className="row" style={{ gap: 6 }}>{c.impact.map((i) => <Link key={i.contract} to={objectPath("Server", i.contract)} className="badge gray"><Icon name="file" size={12} />{i.contract}</Link>)}</span>
            <span className="faint" style={{ fontSize: 12 }}>Once applied, each gets its own change, approved as usual.</span>
          </>
        )}
      </div>
      <div className="review-cell">
        <span className="label-caps">Approval</span>
        {c.critical
          ? <span><Badge tone="orange" icon="shield">platform admin</Badge> <span className="muted" style={{ fontSize: 13 }}>{c.whyCritical?.join("; ")}</span></span>
          : <span className="muted">A settings approver or a platform admin, not you.</span>}
      </div>
    </div>
  );
}

// SettingsEditor adds (no name) or changes an object of a settings kind.
export function SettingsEditor({ kind, name, onClose }: { kind: string; name?: string; onClose: () => void }) {
  const current = useQuery({
    queryKey: ["catalog", kind, name],
    queryFn: () => api<{ yaml: string }>(`/catalog/${encodeURIComponent(kind)}/${encodeURIComponent(name ?? "")}`),
    enabled: !!name,
  });
  const schema = useSchema(kind);
  if ((name && current.isPending) || schema.isPending) return <Modal title={name ? `Edit ${kind} ${name}` : `New ${kind}`} icon={kindOf(kind).icon} onClose={onClose} width={880}><Loading /></Modal>;
  const initial: Obj = name ? (parse(current.data?.yaml ?? "") as Obj) : { apiVersion: "gaia/v1alpha1", kind, metadata: { name: "" }, spec: {} };
  return <Editor kind={kind} name={name} initial={initial} schema={schema.data ?? { type: "object" }} onClose={onClose} />;
}

function Editor({ kind, name, initial, schema, onClose }: { kind: string; name?: string; initial: Obj; schema: Field; onClose: () => void }) {
  const [obj, setObj] = useState<Obj>(initial);
  const [step, setStep] = useState<"edit" | "review">("edit");
  const [yaml, setYaml] = useState<string | null>(null);
  const [yamlError, setYamlError] = useState<string | null>(null);
  const [summary, setSummary] = useState("");
  const [reason, setReason] = useState("");
  const [check, setCheck] = useState<Check | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const qc = useQueryClient();
  const nav = useNavigate();
  const meta = (obj.metadata ?? {}) as Obj;
  const spec = (obj.spec ?? {}) as Obj;
  const objName = String(meta.name ?? "");
  const final = (): string => stringify(prune(obj));
  const edits = (): Edit[] => [{ yaml: final() }];

  const showYAML = (on: boolean) => {
    if (on) {
      setYaml(final());
      return;
    }
    try {
      const v = parse(yaml ?? "");
      setObj(v as Obj);
      setYaml(null);
      setYamlError(null);
    } catch (e) {
      setYamlError(e instanceof Error ? e.message : String(e));
    }
  };
  const review = async () => {
    let current = obj;
    if (yaml !== null) {
      try {
        current = parse(yaml) as Obj;
        setObj(current);
        setYaml(null);
      } catch (e) {
        setYamlError(e instanceof Error ? e.message : String(e));
        return;
      }
    }
    setBusy(true);
    setError(null);
    try {
      setCheck(await checkSettings([{ yaml: stringify(prune(current)) }]));
      setStep("review");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (step === "edit") {
      await review();
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const p = await propose(summary.trim() || defaultSummary, reason, edits());
      void qc.invalidateQueries({ queryKey: ["proposals"] });
      onClose();
      nav(`/changes/settings/${encodeURIComponent(p.id)}`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 422) setCheck((e.data as { check: Check }).check);
      else setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const k = kindOf(kind);
  const defaultSummary = `${name ? "Update" : "Add"} ${kind} ${objName || "…"}`;
  const diff = [...fieldDiff(initial.spec, prune(spec)), ...fieldDiff({ labels: (initial.metadata as Obj | undefined)?.labels }, { labels: meta.labels })];
  const footer: ReactNode = step === "edit" ? (
    <>
      <Button icon="code" onClick={() => showYAML(yaml === null)} title="See or edit this object as YAML">{yaml === null ? "YAML" : "Form"}</Button>
      <span style={{ flex: 1 }} />
      <Button onClick={onClose} disabled={busy}>Cancel</Button>
      <Button type="submit" kind="primary" icon="arrow" disabled={busy || !objName}>{busy ? "Checking…" : "Review"}</Button>
    </>
  ) : (
    <>
      <Button icon="back" onClick={() => setStep("edit")} disabled={busy}>Back</Button>
      <span style={{ flex: 1 }} />
      <Button onClick={onClose} disabled={busy}>Cancel</Button>
      <Button type="submit" kind="primary" icon="pr" disabled={busy || !check || check.errors.length > 0}>{busy ? "Submitting…" : "Submit for approval"}</Button>
    </>
  );
  return (
    <Modal title={step === "edit" ? (name ? `Edit ${kind} ${name}` : `New ${kind}`) : "Review the change"} icon={k.icon} onClose={onClose} onSubmit={submit} width={880} footer={footer}>
      <ol className="steps" aria-label="Steps">
        <li className={step === "edit" ? "on" : "done"}><span>1</span>Edit</li>
        <li className={step === "review" ? "on" : ""}><span>2</span>Review and submit</li>
      </ol>
      {step === "edit" ? (
        yaml !== null ? (
          <label className="field">
            <span>YAML <span className="faint">— the same object, as it is stored</span></span>
            <textarea className="input mono" rows={18} spellCheck={false} value={yaml} onChange={(e) => setYaml(e.target.value)} />
            {yamlError && <span className="error">{yamlError}</span>}
          </label>
        ) : (
          <>
            <section className="sf-section" style={{ borderTop: 0, paddingTop: 0 }}>
              <div className="sf-head"><h3>{kind}</h3><p className="sf-help">{k.text}</p></div>
              <div className="sf-grid">
                <div className="sf-field">
                  <label className="sf-label" htmlFor="obj-name">Name<span className="sf-req">*</span></label>
                  <input id="obj-name" className="input mono" value={objName} readOnly={!!name} required pattern="[a-z0-9]([a-z0-9.\-]*[a-z0-9])?"
                    onChange={(e) => setObj({ ...obj, metadata: { ...meta, name: e.target.value } })} />
                  <p className="sf-help">{name ? "An object keeps its name." : "Lowercase letters, digits, dots and dashes."}</p>
                </div>
              </div>
            </section>
            <SpecForm kind={kind} schema={schema} spec={spec} onChange={(s) => setObj({ ...obj, spec: s })} />
            <SpecForm kind={kind} schema={{ type: "object", fields: [LABELS_FIELD] }} spec={meta} onChange={(m) => setObj({ ...obj, metadata: m })} />
          </>
        )
      ) : (
        <>
          <section className="review-section">
            <h3>What changes</h3>
            <div className="card"><FieldDiff rows={diff} /></div>
          </section>
          {check && <section className="review-section"><h3>Checks</h3><CheckResult c={check} /></section>}
          <section className="review-section">
            <h3>Describe it for the approver</h3>
            <div className="sf-grid">
              <div className="sf-field">
                <label className="sf-label" htmlFor="summary">Summary</label>
                <input id="summary" className="input" value={summary} onChange={(e) => setSummary(e.target.value)} placeholder={defaultSummary} />
                <p className="sf-help">One line; it is how the change is listed.</p>
              </div>
              <div className="sf-field">
                <label className="sf-label" htmlFor="reason">Reason</label>
                <input id="reason" className="input" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Why this change is needed" />
                <p className="sf-help">What the approver should know before deciding.</p>
              </div>
            </div>
          </section>
        </>
      )}
      {error && <div className="error" role="alert">{error}</div>}
    </Modal>
  );
}

// RemoveSetting proposes removing an object, after showing what uses it.
export function RemoveSetting({ kind, name, onClose }: { kind: string; name: string; onClose: () => void }) {
  const [reason, setReason] = useState("");
  const edits: Edit[] = [{ kind, name, delete: true }];
  const check = useQuery({ queryKey: ["check-remove", kind, name], queryFn: () => checkSettings(edits) });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const nav = useNavigate();
  const qc = useQueryClient();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const p = await propose(`Remove ${kind} ${name}`, reason, edits);
      void qc.invalidateQueries({ queryKey: ["proposals"] });
      onClose();
      nav(`/changes/settings/${encodeURIComponent(p.id)}`);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title={`Remove ${kind} ${name}`} icon="x" tone="red" onClose={onClose} onSubmit={submit} width={640}
      footer={<><Button onClick={onClose} disabled={busy}>Cancel</Button><Button type="submit" kind="solid-danger" icon="pr" disabled={busy || !check.data || check.data.errors.length > 0}>{busy ? "Submitting…" : "Submit for approval"}</Button></>}>
      <p className="muted" style={{ margin: 0 }}>Removing is a settings change too: once approved, the next settings version does not have this object. Gaia does not let you remove what something still uses.</p>
      {check.isPending ? <Loading /> : check.data && <CheckResult c={check.data} />}
      <div className="sf-field">
        <label className="sf-label" htmlFor="remove-reason">Reason</label>
        <input id="remove-reason" className="input" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Why it is no longer needed" />
      </div>
      {error && <div className="error" role="alert">{error}</div>}
    </Modal>
  );
}

// AddButton opens the editor for a new object of a kind, on a card's top
// right; it is not there for who may not propose.
export function AddButton({ kind, label }: { kind: string; label?: string }) {
  const can = useCanPropose();
  const [open, setOpen] = useState(false);
  if (!can) return null;
  return (
    <>
      <Button icon="plus" onClick={() => setOpen(true)}>{label ?? `Add ${kind}`}</Button>
      {open && <SettingsEditor kind={kind} onClose={() => setOpen(false)} />}
    </>
  );
}

// SINGLE are the kinds there is one of: changed, never removed.
const SINGLE = new Set(["AuthConfig", "Platform", "Policy"]);

// EditButtons are an object's edit and remove, for settings kinds.
export function EditButtons({ kind, name, generatedBy }: { kind: string; name: string; generatedBy?: string }): ReactNode {
  const can = useCanPropose();
  const [open, setOpen] = useState<"edit" | "remove" | null>(null);
  if (!can) return null;
  const locked = generatedBy ? `${generatedBy} writes this object; change it at its source` : undefined;
  return (
    <span className="row">
      <Button icon="pencil" onClick={() => setOpen("edit")} disabled={!!locked} title={locked}>Edit</Button>
      {!SINGLE.has(kind) && <Button kind="danger" icon="x" onClick={() => setOpen("remove")} disabled={!!locked} title={locked}>Remove</Button>}
      {open === "edit" && <SettingsEditor kind={kind} name={name} onClose={() => setOpen(null)} />}
      {open === "remove" && <RemoveSetting kind={kind} name={name} onClose={() => setOpen(null)} />}
    </span>
  );
}
