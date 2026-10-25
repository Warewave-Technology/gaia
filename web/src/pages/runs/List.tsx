// Runs: every run, newest first; one contract's with ?contract=.
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../../api/client";
import type { Run } from "../../api/types";
import { Card, ErrorNote, Hint, Loading } from "../../ui/parts";
import { RunResult } from "../../ui/record";
import { Page } from "../../ui/shell";
import { RecordLegend } from "../../ui/status";
import { ago, duration } from "../../lib/time";

export function RunList() {
  const [params] = useSearchParams();
  const contract = params.get("contract") ?? "";
  const q = useQuery({
    queryKey: ["runs", contract],
    queryFn: () => api<Run[]>(`/runs?limit=200${contract ? "&contract=" + encodeURIComponent(contract) : ""}`),
    refetchInterval: 10_000,
  });
  return (
    <Page crumbs={contract ? <><Link to="/runs">Runs</Link> / {contract}</> : "Runs"}>
      <div className="title-row">
        <h1>Runs{contract && <span className="muted" style={{ fontWeight: 400 }}> · {contract}</span>}</h1>
        <Hint>A run applies a change to servers: addresses, VMs, monitoring and Ansible, in that order.</Hint>
      </div>
      <Card title={contract ? `Runs of ${contract}` : "Latest runs"} icon="play">
        {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : q.data.length === 0 ? <div className="empty">No runs.</div> : (
          <table className="grid">
            <thead>
              <tr>
                <th>Run</th>
                <th>Contract</th>
                <th><span className="th-hint">Result<Hint><RecordLegend /></Hint></span></th>
                <th>Requested by</th>
                <th>Duration</th>
                <th>When</th>
              </tr>
            </thead>
            <tbody>
              {q.data.map((r) => (
                <tr key={r.id}>
                  <td className="mono">{r.restricted ? r.id : <Link to={`/runs/${encodeURIComponent(r.id)}`}>{r.id}</Link>}</td>
                  <td>{r.contracts.join(", ")}{r.restricted && <span className="faint"> (no access)</span>}</td>
                  <td><RunResult run={r} /></td>
                  <td className="muted">{r.requestedBy}</td>
                  <td>{duration(r.startedAt, r.finishedAt)}</td>
                  <td className="muted">{ago(r.queuedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </Page>
  );
}
