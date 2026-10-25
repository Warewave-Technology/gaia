// What each state looks like, and the "?" legends that explain them.
import type { ReactNode } from "react";
import type { ChangeStatus, RunStatus } from "../api/types";
import { Shield, type IconName } from "./icons";
import { Badge, IL, Leg, Term, type Tone } from "./parts";

type Look = { tone: Tone; icon: IconName; label: string };

export const CHANGE: Record<ChangeStatus, Look> = {
  pending: { tone: "blue", icon: "clock", label: "Awaiting approval" },
  approved: { tone: "green", icon: "check", label: "Approved · queued" },
  running: { tone: "yellow", icon: "play", label: "Applying" },
  applied: { tone: "green", icon: "check", label: "Applied" },
  failed: { tone: "red", icon: "x", label: "Failed" },
  rejected: { tone: "red", icon: "x", label: "Rejected" },
  cancelled: { tone: "gray", icon: "stop", label: "Cancelled" },
  superseded: { tone: "gray", icon: "arrow", label: "Superseded" },
  expired: { tone: "gray", icon: "clock", label: "Expired" },
};

export const RUN: Record<RunStatus, Look> = {
  queued: { tone: "gray", icon: "clock", label: "Queued" },
  running: { tone: "yellow", icon: "play", label: "Running" },
  succeeded: { tone: "green", icon: "check", label: "Succeeded" },
  failed: { tone: "red", icon: "x", label: "Failed" },
  cancelled: { tone: "gray", icon: "stop", label: "Cancelled" },
};

export function ChangeBadge({ status }: { status: ChangeStatus }) {
  const l = CHANGE[status] ?? { tone: "gray", icon: "help", label: status };
  return <Badge tone={l.tone} icon={l.icon}>{l.label}</Badge>;
}

export function RunBadge({ status }: { status: RunStatus }) {
  const l = RUN[status] ?? { tone: "gray", icon: "help", label: status };
  return <Badge tone={l.tone} icon={l.icon}>{l.label}</Badge>;
}

export const SOURCE: Record<string, { icon: IconName; label: string; text: string }> = {
  commit: { icon: "commit", label: "commit", text: "a new commit came to the contract repository" },
  retry: { icon: "rerun", label: "retry", text: "a failed change was tried again" },
  revert: { icon: "undo", label: "revert", text: "an earlier version was brought back" },
  drift: { icon: "scan", label: "drift", text: "a change made by hand is being put right" },
  "drift-auto": { icon: "scan", label: "drift (automatic)", text: "drift is put right on its own (enforce)" },
  operation: { icon: "zap", label: "operation", text: "an Operation (a reboot, a patch…) is running" },
  schedule: { icon: "calendar", label: "schedule", text: "a scheduled Operation is being approved" },
  settings: { icon: "gear", label: "settings", text: "a settings change (site, network, team…) changed the contract's plan" },
};

export function SourceLabel({ source, children }: { source: string; children?: ReactNode }) {
  const s = SOURCE[source] ?? { icon: "help" as IconName, label: source };
  return <IL icon={s.icon} color="var(--fg4)" size={15} weight={400}>{children ?? s.label}</IL>;
}

export function SourceLegend() {
  return (
    <>
      What started the change or the run.
      {Object.entries(SOURCE).map(([k, s]) => (
        <Leg key={k} icon={s.icon} color={k.startsWith("drift") ? "var(--orange)" : "var(--fg2)"} name={s.label}>{s.text}</Leg>
      ))}
    </>
  );
}

export function ChangeStatusLegend() {
  return (
    <>
      Where the change is.
      <Leg icon="clock" color="var(--blue)" name="Awaiting approval">an approval it needs is not given yet</Leg>
      <Leg icon="check" color="var(--green)" name="Approved / Applied">approvals are complete; the run is queued or done</Leg>
      <Leg icon="play" color="var(--yellow)" name="Applying">its run is running</Leg>
      <Leg icon="x" color="var(--red)" name="Failed / Rejected">the run failed, or an approver rejected it</Leg>
      <Leg icon="arrow" color="var(--fg4)" name="Superseded">a newer commit came for the same contract</Leg>
    </>
  );
}

export const SYNC = {
  synced: { icon: "check-circle" as IconName, color: "var(--green)", label: "Synced" },
  "out-of-sync": { icon: "arrow-up-circle" as IconName, color: "var(--yellow)", label: "OutOfSync" },
  drifted: { icon: "scan" as IconName, color: "var(--orange)", label: "Drifted" },
  unknown: { icon: "help" as IconName, color: "var(--gray)", label: "Unknown" },
};

export type SyncState = keyof typeof SYNC;

export function Sync({ state, children, size = 15 }: { state: SyncState; children?: ReactNode; size?: number }) {
  const s = SYNC[state];
  return <IL icon={s.icon} color={s.color} size={size}>{children ?? s.label}</IL>;
}

export function SyncLegend() {
  return (
    <>
      The desired state in Git compared with what is applied.
      <Leg icon="check-circle" color="var(--green)" name="Synced">what Git says is applied</Leg>
      <Leg icon="arrow-up-circle" color="var(--yellow)" name="OutOfSync">Git has a commit that is not applied yet</Leg>
      <Leg icon="scan" color="var(--orange)" name="Drifted">Git is the same, but the real resource was changed by hand</Leg>
      <Leg icon="help" color="var(--gray)" name="Unknown">Gaia has not evaluated the contract yet</Leg>
    </>
  );
}

export const PLAN_ACTION: Record<string, { icon: IconName; color: string; label: string }> = {
  create: { icon: "plus", color: "var(--green)", label: "to create" },
  adopt: { icon: "plus", color: "var(--green)", label: "to adopt" },
  replace: { icon: "pencil", color: "var(--yellow)", label: "to replace" },
  keep: { icon: "same", color: "var(--gray)", label: "unchanged" },
  delete: { icon: "x", color: "var(--red)", label: "to delete" },
};

export function PlanLegend() {
  return (
    <>
      <Leg icon="plus" color="var(--green)" name="to create" />
      <Leg icon="pencil" color="var(--yellow)" name="to change" />
      <Leg icon="same" color="var(--gray)" name="unchanged">left alone</Leg>
      <Leg icon="x" color="var(--red)" name="to delete" />
    </>
  );
}

export function RecordLegend() {
  return (
    <>
      How the run ended.
      <span className="leg"><Badge tone="green" icon="check">Succeeded</Badge><span>every stage passed</span></span>
      <span className="leg"><Badge tone="red" icon="x">Failed</Badge><span>a stage failed; the ones after it did not run</span></span>
      <span className="leg"><Shield /><span>the run record: signed, unchangeable, chained to the one before. Hover for its details.</span></span>
      <span className="leg"><span /><span><Term>Note:</Term> the shield shows only on runs whose record is written.</span></span>
    </>
  );
}

export const HEALTH = {
  green: { icon: "heart" as IconName, color: "var(--green)", label: "Healthy", fill: "var(--green)" },
  yellow: { icon: "alert" as IconName, color: "var(--yellow)", label: "Warning", fill: "none" },
  red: { icon: "heart-crack" as IconName, color: "var(--red)", label: "Broken", fill: "none" },
};

export function HealthLabel({ h, children, size = 15 }: { h: keyof typeof HEALTH; children?: ReactNode; size?: number }) {
  const x = HEALTH[h];
  return <IL icon={x.icon} color={x.color} size={size} fill={x.fill}>{children ?? x.label}</IL>;
}

export function HealthLegend() {
  return (
    <>
      Worked out from what Gaia knows: did the last run succeed, are the servers active, is there open drift.
      <Leg icon="heart" color="var(--green)" name="Healthy" fill="var(--green)">the last run succeeded, servers are active, no drift</Leg>
      <Leg icon="alert" color="var(--yellow)" name="Warning">there is open drift, or a server is not active</Leg>
      <Leg icon="heart-crack" color="var(--red)" name="Broken">the last run failed</Leg>
    </>
  );
}
