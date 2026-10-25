// A settings change (design 17.3): what it does to each object, which
// contracts' plans it changes, and the decision.
import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { api } from "../../api/client";
import type { ObjectChange as ObjectChangeT, Proposal, ProposalStatus } from "../../api/proposals";
import { FieldDiff, fieldDiff, parseYAML } from "../../ui/spec";
import { useSession } from "../../api/session";
import { lineDiff } from "../../lib/linediff";
import { ago, stamp } from "../../lib/time";
import { Icon, type IconName } from "../../ui/icons";
import { Badge, Button, Card, ErrorNote, Hint, Leg, Loading, StatusBar, type Tone } from "../../ui/parts";
import { Modal } from "../../ui/modal";
import { Page } from "../../ui/shell";
import { useStrong } from "../../ui/strong";
import { kindOf, objectPath } from "../../ui/kinds";
import { Author } from "./Versions";

export const PROPOSAL: Record<ProposalStatus, { tone: Tone; icon: IconName; label: string; text: string }> = {
  pending: { tone: "blue", icon: "clock", label: "Awaiting approval", text: "an approver will decide" },
  applied: { tone: "green", icon: "check", label: "Applied", text: "it became a new settings version" },
  rejected: { tone: "red", icon: "x", label: "Rejected", text: "an approver rejected it, saying why" },
  stale: { tone: "gray", icon: "history", label: "Stale", text: "the same objects changed meanwhile; propose it again" },
  withdrawn: { tone: "gray", icon: "undo", label: "Withdrawn", text: "the proposer took it back" },
};

export function ProposalBadge({ status }: { status: ProposalStatus }) {
  const l = PROPOSAL[status] ?? { tone: "gray" as Tone, icon: "help" as IconName, label: status };
  return <Badge tone={l.tone} icon={l.icon}>{l.label}</Badge>;
}

export function ProposalLegend() {
  return <>{Object.entries(PROPOSAL).map(([k, l]) => <Leg key={k} icon={l.icon} color={`var(--${l.tone === "gray" ? "fg4" : l.tone})`} name={l.label}>{l.text}</Leg>)}</>;
}

const OP: Record<string, { tone: Tone; label: string }> = {
  add: { tone: "green", label: "added" },
  change: { tone: "yellow", label: "changed" },
  remove: { tone: "red", label: "removed" },
};

function YamlDiff({ before, after }: { before: string; after: string }) {
  const lines = lineDiff(before, after);
  return (
    <pre className="mono diff" style={{ margin: 0, padding: "12px 0", overflowX: "auto", lineHeight: 1.6 }}>
      {lines.map((l, i) => (
        <div key={i} className={`diff-line ${l.op}`}><span className="diff-sign">{l.op === "add" ? "+" : l.op === "del" ? "−" : " "}</span>{l.text}</div>
      ))}
    </pre>
  );
}

function Decide({ p, approve, onClose }: { p: Proposal; approve: boolean; onClose: () => void }) {
  const [comment, setComment] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const strong = useStrong();
  const qc = useQueryClient();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await strong.run(() => api(`/settings/proposals/${encodeURIComponent(p.id)}/${approve ? "approve" : "reject"}`, { method: "POST", body: { contentHash: p.contentHash, comment } }));
      void qc.invalidateQueries({ queryKey: ["proposals"] });
      void qc.invalidateQueries({ queryKey: ["settings"] });
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Modal title={approve ? "Approve the settings change" : "Reject the settings change"} icon={approve ? "check" : "x"} tone={approve ? undefined : "red"} onClose={onClose} onSubmit={submit}
        footer={<><Button onClick={onClose} disabled={busy}>Cancel</Button><Button type="submit" kind={approve ? "primary" : "solid-danger"} icon={approve ? "check" : "x"} disabled={busy}>{busy ? "Waiting…" : approve ? "Approve" : "Reject"}</Button></>}>
        <p className="muted" style={{ margin: 0 }}>
          {approve
            ? <>Your approval is bound to the content on this page. Once approved it becomes a new settings version{p.impact.length > 0 && <>, and the {p.impact.length} contracts whose plan changes get their own changes</>}.</>
            : "A rejection needs a reason; the proposer sees it."}
        </p>
        <label className="field"><span>{approve ? "Note (optional)" : "Reason"}</span><input className="input" value={comment} onChange={(e) => setComment(e.target.value)} required={!approve} /></label>
        {error && <div className="error" role="alert">{error}</div>}
      </Modal>
      {strong.prompt}
    </>
  );
}

function ObjectChange({ o }: { o: ObjectChangeT }) {
  const [yaml, setYaml] = useState(false);
  const rows = fieldDiff(parseYAML(o.before)?.spec, parseYAML(o.after)?.spec);
  return (
    <section className="card" aria-label={`${o.kind} ${o.name}`}>
      <div className="card-head">
        <div className="card-title">
          <Icon name={kindOf(o.kind).icon} size={16} />
          <h2>{o.op === "remove" ? <>{o.kind} {o.name}</> : <Link to={objectPath(o.kind, o.name)}><span className="faint" style={{ fontWeight: 400 }}>{o.kind} </span>{o.name}</Link>}</h2>
          <Badge tone={OP[o.op]?.tone ?? "gray"}>{OP[o.op]?.label ?? o.op}</Badge>
        </div>
        <Button icon="code" onClick={() => setYaml(!yaml)}>{yaml ? "Fields" : "YAML diff"}</Button>
      </div>
      {yaml ? <YamlDiff before={o.before ?? ""} after={o.after ?? ""} /> : o.op === "remove" ? <div className="empty">The object is removed.</div> : <FieldDiff rows={rows} />}
    </section>
  );
}

export function ProposalPage() {
  const { id = "" } = useParams();
  const { me } = useSession();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["proposals", id], queryFn: () => api<Proposal>(`/settings/proposals/${encodeURIComponent(id)}`) });
  const [deciding, setDeciding] = useState<"approve" | "reject" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const withdraw = async () => {
    setError(null);
    try {
      await api(`/settings/proposals/${encodeURIComponent(id)}/withdraw`, { method: "POST" });
      void qc.invalidateQueries({ queryKey: ["proposals"] });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  const p = q.data;
  return (
    <Page crumbs={<><Link to="/changes">Changes</Link> / Settings change / {id}</>}>
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : p && (
        <>
          <div className="spread">
            <div className="title-row">
              <span style={{ color: "var(--blue)" }}><Icon name="gear" size={22} /></span>
              <h1>{p.title}</h1>
              <ProposalBadge status={p.status} />
              {p.critical && <Badge tone="orange" icon="shield">platform admin</Badge>}
              <Hint width={340}>A settings change: a proposed change to Gaia's settings (objects that are not in Git). Its proposer may not approve it; once approved it becomes a new settings version.<ProposalLegend /></Hint>
            </div>
            {p.status === "pending" && (
              <span className="row">
                {p.mayDecide && <Button kind="primary" icon="check" onClick={() => setDeciding("approve")}>Approve</Button>}
                {p.mayDecide && <Button kind="danger" icon="x" onClick={() => setDeciding("reject")}>Reject</Button>}
                {me?.username === p.requestedBy && <Button icon="undo" onClick={withdraw}>Withdraw</Button>}
              </span>
            )}
          </div>
          {error && <div className="notice err" role="alert">{error}</div>}
          {p.status === "pending" && !p.mayDecide && p.why && <div className="notice">{p.why}</div>}
          {p.status === "stale" && <div className="notice warn">This change is stale: {p.staleReason}. Propose it again on the current settings.</div>}
          {p.critical && p.whyCritical && <div className="notice warn"><b>Only a platform admin decides:</b> {p.whyCritical.join("; ")}</div>}
          <StatusBar items={[
            { icon: "user", title: "Requested by", value: <span style={{ fontSize: 15 }}><Author who={p.requestedBy} /></span>, sub: `${ago(p.createdAt)} · ${stamp(p.createdAt)}` },
            {
              icon: "history", title: "Version", hint: "The settings version it was proposed on; once applied, the version it made.",
              value: p.appliedVersion ? <Link to={`/settings/versions/${p.appliedVersion}`} style={{ fontSize: 15 }}>v{p.baseVersion} → v{p.appliedVersion}</Link> : <span style={{ fontSize: 15 }}>on v{p.baseVersion}</span>,
            },
            { icon: "file", title: "Affected", hint: "Contracts whose plan changes. Once applied, each gets its own change, approved as usual.", value: p.impact.length, sub: p.impact.length ? "contracts" : "no contract changes" },
            {
              icon: "check-circle", title: "Decision", value: <span style={{ fontSize: 15 }}>{p.decidedBy ?? "–"}</span>,
              sub: p.decidedAt ? `${ago(p.decidedAt)}${p.comment ? ` · ${p.comment}` : ""}` : "not decided yet",
            },
          ]} />
          <div className="row" style={{ alignItems: "flex-start", gap: 16 }}>
            <div style={{ flex: "2 1 520px", minWidth: 0, display: "flex", flexDirection: "column", gap: 16 }}>
              {(p.objects ?? []).map((o) => <ObjectChange key={o.path} o={o} />)}
            </div>
            <div style={{ flex: "1 1 280px", display: "flex", flexDirection: "column", gap: 16 }}>
              <Card title="Why" icon="info">
                <p style={{ margin: 0, padding: "12px 16px", whiteSpace: "pre-wrap" }}>{p.reason || <span className="faint">No reason given.</span>}</p>
              </Card>
              <Card title="Affected contracts" icon="file">
                {p.impact.length === 0 ? <div className="empty">No contract's plan changes.</div> : (
                  <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
                    {p.impact.map((i, n) => (
                      <li key={i.contract} className="row" style={{ gap: 10, padding: "10px 16px", borderTop: n ? "1px solid var(--bg1)" : undefined }}>
                        <span className="faint"><Icon name="file" size={15} /></span>
                        <Link to={objectPath("Server", i.contract)}>{i.contract}</Link>
                      </li>
                    ))}
                  </ul>
                )}
              </Card>
            </div>
          </div>
          {deciding && <Decide p={p} approve={deciding === "approve"} onClose={() => setDeciding(null)} />}
        </>
      )}
    </Page>
  );
}
