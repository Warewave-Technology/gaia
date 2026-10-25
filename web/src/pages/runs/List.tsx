// Run'lar: every run, newest first; one contract's with ?contract=.
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
    <Page crumbs={contract ? <><Link to="/runs">Run'lar</Link> / {contract}</> : "Run'lar"}>
      <div className="title-row">
        <h1>Run'lar{contract && <span className="muted" style={{ fontWeight: 400 }}> · {contract}</span>}</h1>
        <Hint>Run, bir değişikliğin sunuculara uygulanmasıdır: IP ayırma, VM, izleme kaydı ve Ansible, sırayla.</Hint>
      </div>
      <Card title={contract ? `${contract} kontratının run'ları` : "Son run'lar"} icon="play">
        {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : q.data.length === 0 ? <div className="empty">Run yok.</div> : (
          <table className="grid">
            <thead>
              <tr>
                <th>Run</th>
                <th>Kontrat</th>
                <th><span className="th-hint">Sonuç<Hint><RecordLegend /></Hint></span></th>
                <th>İsteyen</th>
                <th>Süre</th>
                <th>Zaman</th>
              </tr>
            </thead>
            <tbody>
              {q.data.map((r) => (
                <tr key={r.id}>
                  <td className="mono">{r.restricted ? r.id : <Link to={`/runs/${encodeURIComponent(r.id)}`}>{r.id}</Link>}</td>
                  <td>{r.contracts.join(", ")}{r.restricted && <span className="faint"> (yetkiniz yok)</span>}</td>
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
