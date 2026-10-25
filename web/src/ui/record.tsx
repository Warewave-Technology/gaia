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
  if (!r) return <span className="muted">Bu run'ın kaydı henüz yazılmadı.</span>;
  return (
    <>
      <span style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, marginBottom: 10 }}>
        <span className="il"><Shield />Run kaydı</span>
        <span className="faint" style={{ fontSize: 12 }}>zincir #{r.seq}</span>
      </span>
      <dl style={{ margin: 0, display: "grid", gridTemplateColumns: "max-content minmax(0, 1fr)", gap: "8px 14px" }}>
        <Row k="Plan" v={short(run.planHash, 19) + "…"} why="onaylanan planın parmak izi" />
        <Row k="Kayıt" v={short(r.recordHash, 19) + "…"} why="bu kaydın kendi hash'i" />
        <Row k="Önceki" v={r.prevHash ? short(r.prevHash, 19) + "…" : "zincirin ilk kaydı"} why="zincirde bir önceki kayıt" />
        <Row k="EE" v={run.eeImage} why="Ansible'ın çalıştığı imaj" />
        <Row k="Config" v={run.configCommit ? `ansible/ @ ${short(run.configCommit)}` : ""} why="Ansible reposunun commit'i" />
        <dt className="muted">Arşiv</dt>
        <dd style={{ margin: 0, overflowWrap: "anywhere" }}>
          {r.archived ? <span className="mono" style={{ fontSize: 12 }}>{r.archived}</span> : <span className="muted">henüz arşivlenmedi</span>}
          <span className="faint" style={{ display: "block" }}>Object Lock'lu arşiv: silinemez</span>
        </dd>
      </dl>
      <span className="faint" style={{ display: "block", marginTop: 10, paddingTop: 8, borderTop: "1px solid var(--bg1)", fontSize: 12 }}>
        İmzalı, değiştirilemez; bir öncekine zincirli.
      </span>
    </>
  );
}

// RecordTip is the shield for a run already loaded in full.
export function RecordTip({ run, right }: { run: Run; right?: boolean }) {
  if (!run.record) return null;
  return (
    <Tip trigger={<Shield />} label="Run kaydı" right={right} width={360}>
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
      <Tip trigger={<Shield />} label="Run kaydı" right={right} width={360}>
        {q.data ? <RecordBody run={q.data} /> : q.error ? <span className="muted">{q.error instanceof ApiError && q.error.status === 403 ? "Bu run'ı göremezsiniz." : q.error.message}</span> : <span className="muted">Yükleniyor…</span>}
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
