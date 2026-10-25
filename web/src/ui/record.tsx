// The run record's shield and its tooltip (design 10.1): what the record
// pins down, shown on hover.
import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, ApiError } from "../api/client";
import type { Run } from "../api/types";
import { Shield } from "./icons";
import { Badge, Tip, short } from "./parts";
import { RUN } from "./status";

function Row({ k, v, why }: { k: string; v: ReactNode; why: string }) {
  return (
    <>
      <dt className="muted">{k}</dt>
      <dd style={{ margin: 0, overflowWrap: "anywhere" }}>
        <span className="mono" style={{ fontSize: 12 }}>{v || "–"}</span>
        <span className="faint" style={{ display: "block" }}>{why}</span>
      </dd>
    </>
  );
}

export function RecordBody({ run }: { run: Run }) {
  const r = run.record;
  if (!r) return <span className="muted">This run's record is not written yet.</span>;
  return (
    <>
      <span style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, marginBottom: 10 }}>
        <span className="il"><Shield />Run record</span>
        <span className="faint" style={{ fontSize: 12 }}>chain #{r.seq}</span>
      </span>
      <dl style={{ margin: 0, display: "grid", gridTemplateColumns: "max-content minmax(0, 1fr)", gap: "8px 14px" }}>
        <Row k="Plan" v={short(run.planHash, 19) + "…"} why="the approved plan's fingerprint" />
        <Row k="Record" v={short(r.recordHash, 19) + "…"} why="this record's own hash" />
        <Row k="Previous" v={r.prevHash ? short(r.prevHash, 19) + "…" : "the chain's first record"} why="the record before it in the chain" />
        <Row k="EE" v={run.eeImage} why="the image Ansible ran in" />
        <Row k="Config" v={run.configCommit ? `ansible/ @ ${short(run.configCommit)}` : ""} why="the Ansible repository's commit" />
        <dt className="muted">Archive</dt>
        <dd style={{ margin: 0, overflowWrap: "anywhere" }}>
          {r.archived ? <span className="mono" style={{ fontSize: 12 }}>{r.archived}</span> : <span className="muted">not archived yet</span>}
          <span className="faint" style={{ display: "block" }}>an Object Lock archive: it cannot be deleted</span>
        </dd>
      </dl>
      <span className="faint" style={{ display: "block", marginTop: 10, paddingTop: 8, borderTop: "1px solid var(--bg1)", fontSize: 12 }}>
        Signed and unchangeable; chained to the one before.
      </span>
    </>
  );
}

// RecordTip is the shield for a run already loaded in full.
export function RecordTip({ run, right }: { run: Run; right?: boolean }) {
  if (!run.record) return null;
  return (
    <Tip trigger={<Shield />} label="Run record" right={right} width={360}>
      <RecordBody run={run} />
    </Tip>
  );
}

// LazyRecordTip is the shield in lists: the run is read when the tooltip
// first opens.
export function LazyRecordTip({ id, right }: { id: string; right?: boolean }) {
  const [open, setOpen] = useState(false);
  const q = useQuery({ queryKey: ["run", id], queryFn: () => api<Run>(`/runs/${encodeURIComponent(id)}`), enabled: open, staleTime: 60_000 });
  return (
    <span onMouseEnter={() => setOpen(true)} onFocus={() => setOpen(true)} style={{ display: "inline-flex" }}>
      <Tip trigger={<Shield />} label="Run record" right={right} width={360}>
        {q.data ? <RecordBody run={q.data} /> : q.error ? <span className="muted">{q.error instanceof ApiError && q.error.status === 403 ? "You may not see this run." : q.error.message}</span> : <span className="muted">Loading…</span>}
      </Tip>
    </span>
  );
}

// RunResult is a run's status badge, with the shield once it has a record.
export function RunResult({ run, lazy = true }: { run: Run; lazy?: boolean }) {
  const look = RUN[run.status];
  const recorded = run.status === "succeeded" || run.status === "failed";
  return (
    <span className="row" style={{ gap: 8, flexWrap: "nowrap" }}>
      <Badge tone={look?.tone ?? "gray"} icon={look?.icon}>{look?.label ?? run.status}</Badge>
      {recorded && !run.restricted && (lazy ? <LazyRecordTip id={run.id} /> : <RecordTip run={run} />)}
    </span>
  );
}
