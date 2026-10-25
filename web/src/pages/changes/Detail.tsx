// A change: what it does, what it waits for, and the decision on it.
import { useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { api, ApiError } from "../../api/client";
import { useSession } from "../../api/session";
import type { ApprovalGroup, Change, Commit, Gate } from "../../api/types";
import { Icon, type IconName } from "../../ui/icons";
import { Badge, Button, Card, ErrorNote, Hint, IL, Leg, LinkButton, Loading, Mono, short, shortLabel, StatusBar, type StatusItem } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { ChangeBadge, ChangeStatusLegend, PLAN_ACTION, PlanLegend, Sync, SyncLegend } from "../../ui/status";
import { areas, baselineName, isServerPlan, servers, total, type Counts, type Plan, type PlanAccess } from "../../lib/plan";
import { decide, stepUp, stepUpTOTP } from "../../lib/stepup";
import { Modal } from "../../ui/modal";
import { YamlDiff } from "./Diff";
import { clock } from "../../lib/time";

type Step = "idle" | "approve" | "reject";

export function ChangeDetail() {
  const { id = "" } = useParams();
  const q = useQuery({
    queryKey: ["change", id],
    queryFn: () => api<Change>(`/changes/${encodeURIComponent(id)}`),
    refetchInterval: (query) => (query.state.data && ["pending", "approved", "running"].includes(query.state.data.status) ? 10_000 : false),
  });
  const crumbs = <><Link to="/changes">Changes</Link> / {q.data?.contract ?? id}</>;
  if (q.isPending) return <Page crumbs={crumbs}><Loading /></Page>;
  if (q.error) {
    const restricted = q.error instanceof ApiError && q.error.status === 403;
    return (
      <Page crumbs={crumbs}>
        {restricted ? <div className="notice warn">You may see this contract's name only; its details are closed to you.</div> : <ErrorNote error={q.error} />}
      </Page>
    );
  }
  return <Body c={q.data} crumbs={crumbs} />;
}

function gate(c: Change, name: string): Gate | undefined {
  return c.gates?.find((g) => g.gate === name);
}

function Body({ c, crumbs }: { c: Change; crumbs: ReactNode }) {
  const { me } = useSession();
  const [step, setStep] = useState<Step>("idle");
  const [tab, setTab] = useState<"plan" | "diff" | "commits" | "raw">("plan");
  const plan = isServerPlan(c.plan) ? c.plan : null;
  const commits = (c.commits ?? []) as Commit[];
  const canDecide = !!me && !!c.awaitingMe;
  return (
    <Page crumbs={crumbs}>
      <div className="title-row">
        <span style={{ color: "var(--blue)" }}><Icon name="pr" size={22} /></span>
        <h1>{c.contract}</h1>
        <ChangeBadge status={c.status} />
        <Hint><ChangeStatusLegend /></Hint>
        {c.label && <span className="muted mono">{shortLabel(c.label)}</span>}
      </div>

      <div className="spread">
        <div className="row">
          <LinkButton to={`/contracts/${encodeURIComponent(c.contract)}`} icon="file">Contract</LinkButton>
          {c.runId && <LinkButton to={`/runs/${encodeURIComponent(c.runId)}`} icon="play">Run</LinkButton>}
        </div>
        {canDecide && step === "idle" && (
          <div className="row">
            <Button kind="danger" icon="x" onClick={() => setStep("reject")}>Reject</Button>
            <Button kind="primary" icon="check" onClick={() => setStep("approve")}>Approve</Button>
          </div>
        )}
      </div>

      <StatusBar items={statusItems(c, plan)} />

      {step !== "idle" && <Decision c={c} approve={step === "approve"} onDone={() => setStep("idle")} />}

      <div className="row" style={{ alignItems: "flex-start", gap: 16 }}>
        <Gates c={c} />
        <section className="card" style={{ flex: "2 1 520px", minWidth: 0 }} aria-label="What the change does">
          <div role="tablist" aria-label="View" className="row" style={{ gap: 4, padding: "0 8px", borderBottom: "1px solid var(--bg1)" }}>
            <Tab on={tab === "plan"} icon="plan" onClick={() => setTab("plan")}>Plan</Tab>
            <Tab on={tab === "diff"} icon="diff" onClick={() => setTab("diff")}>Git diff</Tab>
            <Tab on={tab === "commits"} icon="commit" onClick={() => setTab("commits")}>Commits</Tab>
            <Tab on={tab === "raw"} icon="code" onClick={() => setTab("raw")}>Raw plan</Tab>
          </div>
          {tab === "plan" && (plan ? <PlanView plan={plan} /> : <div className="card-body muted">This change's plan is not a server plan; see the raw plan.</div>)}
          {tab === "diff" && <YamlDiff id={c.id} contract={c.contract} />}
          {tab === "commits" && <Commits commits={commits} />}
          {tab === "raw" && <pre className="mono" style={{ margin: 0, padding: 16, overflowX: "auto", color: "var(--fg2)" }}>{JSON.stringify(c.plan, null, 2)}</pre>}
        </section>
      </div>

      <Activity c={c} />
    </Page>
  );
}

function Tab({ on, icon, onClick, children }: { on: boolean; icon: IconName; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" role="tab" aria-selected={on} onClick={onClick} className="il"
      style={{ fontSize: 12, letterSpacing: "0.06em", textTransform: "uppercase", height: 44, padding: "0 16px", border: 0, background: "transparent", cursor: "pointer",
        borderBottom: `2px solid ${on ? "var(--yellow)" : "transparent"}`, color: on ? "var(--yellow)" : "var(--fg4)" }}>
      <Icon name={icon} size={15} />{children}
    </button>
  );
}

function CountsView({ c, size = 15 }: { c: Counts; size?: number }) {
  return (
    <span className="row" style={{ gap: 10 }}>
      {c.add > 0 && <IL icon="plus" color="var(--green)" size={size}>{c.add}</IL>}
      {c.change > 0 && <IL icon="pencil" color="var(--yellow)" size={size}>{c.change}</IL>}
      {c.same > 0 && <IL icon="same" color="var(--gray)" size={size}>{c.same}</IL>}
      {c.remove > 0 && <IL icon="x" color="var(--red)" size={size}>{c.remove}</IL>}
      {c.add + c.change + c.same + c.remove === 0 && <span className="muted">–</span>}
    </span>
  );
}

function approvalNeed(c: Change): { groups: ApprovalGroup[]; excluded: string[]; via?: string } {
  const d = (gate(c, "approval")?.detail ?? {}) as { groups?: ApprovalGroup[]; excluded?: string[]; via?: string };
  return { groups: d.groups ?? [], excluded: d.excluded ?? [], via: d.via };
}

function statusItems(c: Change, plan: Plan | null): StatusItem[] {
  const open = ["pending", "approved", "running"].includes(c.status);
  const need = approvalNeed(c);
  const approved = need.groups.filter((g) => g.approvedBy).length;
  const rejected = c.status === "rejected";
  const ag = gate(c, "approval");
  const sg = gate(c, "signature");
  const signers = ((sg?.detail ?? {}) as { signers?: string[] }).signers ?? c.requestedBy ?? [];
  const items: StatusItem[] = [
    {
      icon: "sync", title: "Sync", hint: <SyncLegend />,
      value: open ? <Sync state="out-of-sync" size={20} /> : c.status === "applied" ? <Sync state="synced" size={20} /> : <ChangeBadge status={c.status} />,
      sub: <>{shortLabel(c.label)}{c.commits ? ` · ${(c.commits as Commit[]).length} commit` : ""}</>,
    },
    {
      icon: "user", title: "Approval",
      hint: <>The approvals it needs and who gave them. An approval is bound to the plan hash; it falls if the plan changes.
        <Leg icon="clock" color="var(--blue)" name="Waiting">not approved yet</Leg>
        <Leg icon="check-circle" color="var(--green)" name="Approved">the approvals it needs are given</Leg>
        <Leg icon="x-circle" color="var(--red)" name="Rejected">an approver rejected it</Leg></>,
      value: !ag ? <span className="muted" style={{ fontSize: 15 }}>not needed</span>
        : rejected ? <IL icon="x-circle" color="var(--red)" size={20}>Rejected</IL>
        : ag.status === "ok" ? <IL icon="check-circle" color="var(--green)" size={20}>{need.via ? "Approved" : `${approved}/${need.groups.length}`}</IL>
        : <IL icon="clock" color="var(--blue)" size={20}>{approved}/{need.groups.length}</IL>,
      sub: need.via ? `approved by ${need.via}` : need.groups.map((g) => g.approvedBy ?? `waiting for: ${g.candidates.join(", ") || "nobody"}`).join(" · "),
    },
  ];
  if (plan) {
    items.push({
      icon: "plan", title: "Plan", hint: <>What Gaia worked out for this change. The hash is the plan's fingerprint.<PlanLegend /></>,
      value: <><CountsView c={total(plan)} size={18} /><span className="muted" style={{ fontSize: 15, fontWeight: 500 }}>servers</span></>,
      sub: <Mono>{short(c.planHash, 19)}…</Mono>,
    });
  }
  items.push({
    icon: "key", title: "Signature", hint: "The commits' signatures, and the person Gaia knows the signing key as.",
    value: !sg ? <span className="muted" style={{ fontSize: 15 }}>–</span>
      : sg.status === "ok" ? <IL icon="check-circle" color="var(--green)" size={20}>Verified</IL>
      : sg.status === "failed" ? <IL icon="x-circle" color="var(--red)" size={20}>Unsigned</IL>
      : <IL icon="clock" color="var(--blue)" size={20}>Waiting</IL>,
    sub: signers.join(", "),
  });
  return items;
}

const GATES: Record<string, { title: string; icon: IconName; hint: string }> = {
  validation: { title: "Validation", icon: "check", hint: "The contract fits the catalog, and its parameters fit their schemas and CEL rules." },
  signature: { title: "Signature", icon: "key", hint: "The commit is signed with an SSH or OpenPGP key its author registered in Gaia." },
  approval: { title: "Approval", icon: "user", hint: "The approvals the Policy asks for. An approval is bound to the plan hash and falls if the plan changes. Whoever asked for a change may not approve it." },
  window: { title: "Window", icon: "clock", hint: "When the Operation may run." },
};

function GateMark({ status }: { status: Gate["status"] }) {
  if (status === "ok") return <span style={{ color: "var(--green)" }} aria-label="Done"><Icon name="check-circle" size={18} /></span>;
  if (status === "failed") return <span style={{ color: "var(--red)" }} aria-label="Failed"><Icon name="x-circle" size={18} /></span>;
  return <span style={{ color: "var(--blue)" }} aria-label="Waiting"><Icon name="clock" size={18} /></span>;
}

function gateDetail(c: Change, g: Gate): ReactNode {
  const d = (g.detail ?? {}) as Record<string, unknown>;
  if (typeof d.error === "string") return <span className="error">{d.error}</span>;
  switch (g.gate) {
    case "validation":
      return g.status === "ok" ? "References, parameters and CEL rules pass" : "Validation failed";
    case "signature": {
      if (g.status === "ok") return `Signed by ${((d.signers as string[]) ?? []).join(", ")}`;
      const bad = (d.commits as Commit[] | undefined) ?? [];
      return <>Unsigned or unknown: {bad.map((x) => <Mono key={x.commit}>{short(x.commit)} </Mono>)}{bad[0]?.problem && <span className="muted"> · {bad[0].problem}</span>}</>;
    }
    case "approval": {
      const need = approvalNeed(c);
      if (need.via) return `Approved by ${need.via}`;
      return (
        <>
          {need.groups.map((gr) => (
            <div key={gr.name}>
              One of {gr.teams.join(", ")}'s {gr.role === "approver" ? "approvers" : "members"}:{" "}
              {gr.approvedBy ? <b style={{ color: "var(--green)" }}>{gr.approvedBy} approved</b> : <span>waiting ({gr.candidates.join(", ") || "nobody may approve"})</span>}
            </div>
          ))}
          {need.excluded.length > 0 && <div className="faint" style={{ fontSize: 13 }}>{need.excluded.join(", ")} may not approve this change</div>}
        </>
      );
    }
    case "window":
      return d.start || d.end ? `${String(d.start ?? "")} – ${String(d.end ?? "")}` : "Any time";
    default:
      return null;
  }
}

function Gates({ c }: { c: Change }) {
  const gates = c.gates ?? [];
  const waiting = gates.some((g) => g.status !== "ok");
  return (
    <section className="card" style={{ flex: "1 1 320px" }} aria-labelledby="h-gates">
      <div className="card-head">
        <div className="card-title"><Icon name="check-circle" size={16} /><h2 id="h-gates">What it needs to be applied</h2>
          <Hint>Gaia applies the change on its own once all of these are met. While one is missing, the change waits.</Hint></div>
      </div>
      <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
        {gates.map((g, i) => {
          const m = GATES[g.gate] ?? { title: g.gate, icon: "info" as IconName, hint: "" };
          return (
            <li key={g.gate} style={{ display: "flex", gap: 12, padding: "14px 16px", borderBottom: i < gates.length - 1 ? "1px solid var(--bg1)" : undefined }}>
              <GateMark status={g.status} />
              <div style={{ minWidth: 0 }}>
                <div className="row" style={{ gap: 6, fontWeight: 600 }}><span className="faint"><Icon name={m.icon} size={15} /></span>{m.title}{m.hint && <Hint>{m.hint}</Hint>}</div>
                <div className="muted">{gateDetail(c, g)}</div>
              </div>
            </li>
          );
        })}
      </ul>
      <div style={{ padding: "12px 16px", background: "var(--bg0)", borderTop: "1px solid var(--bg1)", color: "var(--fg4)", fontSize: 13, borderRadius: "0 0 6px 6px" }}>
        {c.status === "pending" && waiting ? "Gaia starts applying it as soon as these are met."
          : c.status === "rejected" ? "Rejected. A new commit to the contract opens a new change."
          : c.status === "approved" || c.status === "running" ? "Everything is met; Gaia is applying it."
          : <ChangeBadge status={c.status} />}
      </div>
    </section>
  );
}

function PlanView({ plan }: { plan: Plan }) {
  const rows = servers(plan);
  const AREA_ICON: Record<string, IconName> = { ipam: "network", provisioning: "vm", monitoring: "activity", configuration: "wrench" };
  const AREA_HINT: Record<string, string> = {
    ipam: "IP addresses.",
    provisioning: "VMs: the machines Pulumi creates, changes or deletes.",
    monitoring: "Host entries in the monitoring system (Zabbix).",
    configuration: "The servers Ansible runs on. Configuration is checked again on every server.",
  };
  const contract = plan.contracts?.[0];
  return (
    <div className="card-body" style={{ display: "flex", flexDirection: "column", gap: 16 }}>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(4, minmax(0, 1fr))", gap: 12 }}>
        {areas(plan).map((a) => (
          <div key={a.key} style={{ background: "var(--bg0)", border: "1px solid var(--bg2)", borderRadius: 6, padding: "10px 12px" }}>
            <div className="label-caps row" style={{ gap: 6 }}><Icon name={AREA_ICON[a.key] ?? "info"} size={14} />{a.label}<Hint width={240}>{AREA_HINT[a.key]}<PlanLegend /></Hint></div>
            <div className="row" style={{ marginTop: 4, gap: 10, fontWeight: 600 }}>
              {a.key === "configuration" ? <span>{a.counts.same} servers</span> : <CountsView c={a.counts} />}
              {a.providers.length > 0 && <span className="muted" style={{ fontWeight: 400 }}>{a.providers.join(", ")}</span>}
            </div>
          </div>
        ))}
      </div>
      <table className="grid">
        <thead><tr><th style={{ width: 32 }} /><th>Server</th><th>Placement</th><th>Size</th><th>IP</th><th>UID</th></tr></thead>
        <tbody>
          {rows.map((s) => {
            const a = PLAN_ACTION[s.action] ?? PLAN_ACTION.keep!;
            const size = s.contract.size;
            return (
              <tr key={s.uid ?? s.hostname} className={s.action === "create" || s.action === "adopt" ? "added" : undefined}>
                <td title={a.label} style={{ color: a.color }}><Icon name={a.icon} size={14} width={2.6} /></td>
                <td style={{ fontWeight: 600 }}>{s.hostname}</td>
                <td className="nowrap">{s.site} / {s.network}</td>
                <td className="nowrap">{size ? `${size.name} · ${size.cpu ?? "?"} vCPU, ${size.memoryGiB ?? "?"} GiB` : ""}</td>
                <td className="muted">{s.ipam?.pool ? `from IPAM (${s.ipam.pool})` : ""}</td>
                <td className="mono">{s.uid ? short(s.uid, 8) + "…" : <span className="muted">when applied</span>}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {contract?.access && contract.access.length > 0 && <AccessView access={contract.access} />}
      {contract && (
        <div className="row muted" style={{ gap: "6px 16px" }}>
          {contract.monitoring && <span className="il" style={{ fontWeight: 400 }}><Icon name="activity" size={14} />Monitoring: {contract.monitoring.provider}{contract.monitoring.templates?.length ? ` · ${contract.monitoring.templates.join(", ")}` : ""}</span>}
          {contract.baseline && <span className="il" style={{ fontWeight: 400 }}><Icon name="layers" size={14} />Configuration: baseline {baselineName(contract)}{contract.roles?.length ? ` + ${contract.roles.map((r) => r.name).join(", ")}` : ""}</span>}
        </div>
      )}
    </div>
  );
}

function AccessView({ access }: { access: PlanAccess[] }) {
  return (
    <div>
      <div className="label-caps row" style={{ gap: 6, marginBottom: 8 }}><Icon name="user" size={14} />Access
        <Hint width={300}>Who may log in to the servers. People come from teams; usernames never go to Git.
          <Leg icon="key" color="var(--red)" name="full sudo">may run any command as root</Leg>
          <Leg icon="key" color="var(--yellow)" name="limited sudo">only the listed commands</Leg>
          <Leg icon="user" color="var(--fg4)" name="no sudo">log in only</Leg></Hint></div>
      <table className="grid">
        <thead><tr><th>Who</th><th>Teams</th><th>Sudo</th></tr></thead>
        <tbody>
          {access.map((a) => (
            <tr key={a.via}>
              <td className="nowrap"><span className="mono">{a.via}</span>{a.privileged && <> <Badge tone="orange">privileged</Badge></>}</td>
              <td>{a.teams.join(", ")}</td>
              <td>
                {a.sudo ? <IL icon="key" color="var(--red)" size={14}>full sudo</IL>
                  : a.sudoCommands?.length ? (
                    <span>
                      <IL icon="key" color="var(--yellow)" size={14}>limited sudo</IL>
                      <ul style={{ margin: "4px 0 0", paddingLeft: 18 }}>{a.sudoCommands.map((c) => <li key={c} className="mono">{c}</li>)}</ul>
                    </span>
                  ) : <span className="muted">none</span>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Commits({ commits }: { commits: Commit[] }) {
  if (commits.length === 0) return <div className="empty">No commits.</div>;
  return (
    <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
      {commits.map((x, i) => (
        <li key={x.repo + x.commit} className="row" style={{ gap: 12, padding: "14px 16px", borderTop: i ? "1px solid var(--bg1)" : undefined }}>
          <span className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={15} /><Mono>{short(x.commit)}</Mono></span>
          <span className="muted">{x.repo}</span>
          {x.signer ? <IL icon="key" color="var(--green)" size={14}>{x.signer}</IL> : <IL icon="x-circle" color="var(--red)" size={14}>unsigned</IL>}
          {x.reverts && <span className="muted">reverts <Mono>{short(x.reverts)}</Mono></span>}
          {x.problem && <span className="error">{x.problem}</span>}
        </li>
      ))}
    </ul>
  );
}

type Note = { id: number; trigger: string; channel: string; recipient: string; status: string; createdAt: string };

function Activity({ c }: { c: Change }) {
  const notes = useQuery({ queryKey: ["change", c.id, "notifications"], queryFn: () => api<Note[]>(`/changes/${encodeURIComponent(c.id)}/notifications`) });
  type Item = { at: string; icon: IconName; color: string; text: ReactNode };
  const items: Item[] = [{ at: c.createdAt, icon: "plan", color: "var(--yellow)", text: <>Change opened; plan <Mono>{short(c.planHash, 15)}…</Mono></> }];
  for (const a of c.approvals ?? []) {
    items.push(a.decision === "approve"
      ? { at: a.createdAt, icon: "check-circle", color: "var(--green)", text: <b>{a.username} approved</b> }
      : { at: a.createdAt, icon: "x-circle", color: "var(--red)", text: <><b>{a.username} rejected</b>{a.comment && <span className="muted">: {a.comment}</span>}</> });
  }
  for (const n of notes.data ?? []) {
    items.push({ at: n.createdAt, icon: "bell", color: "var(--aqua)", text: <>Notification ({n.trigger}): {n.channel} → {n.recipient} <span className="faint">· {n.status}</span></> });
  }
  if (c.runId) items.push({ at: c.updatedAt, icon: "play", color: "var(--yellow)", text: <>Run: <Link to={`/runs/${encodeURIComponent(c.runId)}`}><Mono>{c.runId}</Mono></Link></> });
  if (c.finishedAt) items.push({ at: c.finishedAt, icon: "history", color: "var(--fg4)", text: <>Closed: <ChangeBadge status={c.status} /></> });
  items.sort((a, b) => a.at.localeCompare(b.at));
  return (
    <Card title="Activity" icon="history" hint="What happened to this change, in order: its plan, notifications, decisions and run.">
      <ol style={{ listStyle: "none", margin: 0, padding: "10px 16px 16px", display: "flex", flexDirection: "column", gap: 10 }}>
        {items.map((it, i) => (
          <li key={i} className="row" style={{ gap: 12, flexWrap: "nowrap" }}>
            <span className="mono faint" style={{ width: 56, flex: "0 0 auto" }} title={it.at}>{clock(it.at)}</span>
            <span style={{ color: it.color }}><Icon name={it.icon} size={15} /></span>
            <span>{it.text}</span>
          </li>
        ))}
      </ol>
    </Card>
  );
}

// Decision is the approve or reject panel. It always confirms with a
// passkey (or a TOTP code) first, then sends the decision with the plan
// hash it was made on.
function Decision({ c, approve, onDone }: { c: Change; approve: boolean; onDone: () => void }) {
  const qc = useQueryClient();
  const [comment, setComment] = useState("");
  const [totp, setTotp] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const send = () => api<Change>(`/changes/${encodeURIComponent(c.id)}/${approve ? "approve" : "reject"}`, {
    method: "POST", body: { planHash: c.planHash, comment: comment.trim() },
  });

  const finish = async () => {
    await decide(send, async () => { throw new Error("The confirmation expired; try again."); });
    await qc.invalidateQueries({ queryKey: ["change", c.id] });
    await qc.invalidateQueries({ queryKey: ["changes"] });
    onDone();
  };

  const run = async (e?: FormEvent) => {
    e?.preventDefault();
    if (!approve && !comment.trim()) {
      setError("Say why you reject.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      if (totp !== null) {
        await stepUpTOTP(totp);
      } else {
        const r = await stepUp();
        if (r.kind === "totp") {
          setTotp("");
          setBusy(false);
          return;
        }
      }
      await finish();
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={approve ? "Approve the plan" : "Reject the plan"} icon={approve ? "check-circle" : "x-circle"} tone={approve ? "yellow" : "red"} onClose={onDone} onSubmit={run} width={560}
      footer={<>
        <Button onClick={onDone} disabled={busy}>Cancel</Button>
        <Button type="submit" kind={approve ? "primary" : "solid-danger"} icon="key" disabled={busy}>
          {busy ? "Waiting…" : totp !== null ? (approve ? "Approve with the code" : "Reject with the code") : approve ? "Approve with a passkey" : "Reject with a passkey"}
        </Button>
      </>}>
      <p style={{ margin: 0 }}>
        You decide on this plan for <b>{c.contract}</b>: <Mono>{short(c.planHash, 19)}…</Mono>
      </p>
      <p className="muted" style={{ margin: 0 }}>The decision is bound to this plan; if the plan changes, the approval falls. Confirm with your passkey to go on.</p>
      <label className="field">
        <span>{approve ? "Note (optional)" : "Why you reject"}</span>
        <textarea className="input" rows={3} value={comment} onChange={(e) => setComment(e.target.value)} placeholder={approve ? "" : "Goes to whoever asked for it and to the channel"} />
      </label>
      {totp !== null && (
        <label className="field">
          <span>You have no passkey; enter the code from your authenticator app</span>
          <input className="input mono" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} value={totp} onChange={(e) => setTotp(e.target.value)} autoFocus required />
        </label>
      )}
      {error && <div className="error" role="alert">{error}</div>}
    </Modal>
  );
}

function message(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 409) return `${err.message} — reload the page and look again.`;
    if (err.status === 403) return `You may not decide on this change: ${err.message}`;
    if (err.status === 401) return "The code or passkey was not accepted.";
    return err.message;
  }
  if (err instanceof Error && err.name === "NotAllowedError") return "The passkey prompt was cancelled or timed out.";
  return err instanceof Error ? err.message : String(err);
}

