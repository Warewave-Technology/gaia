// Settings → Discovery: what the last discovery found at the compute and monitoring
// providers (design 8.3): servers Gaia manages, ones it does not, ones
// that are gone.
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, ApiError } from "../../api/client";
import { hasRole, useSession } from "../../api/session";
import { Icon } from "../../ui/icons";
import { Badge, Button, Card, ErrorNote, IL, Loading, Mono, StatusBar, type Tone } from "../../ui/parts";
import { TabHead } from "../../ui/tabs";
import { ago, stamp } from "../../lib/time";

type VM = { provider: string; id: string; name: string; powerState: string; ips?: string[]; category: "managed" | "orphan" | "unmanaged" | "template"; uid?: string; hostname?: string; contract?: string };
type Report = { takenAt: string; vms: VM[]; missing: { provider: string; id: string; uid: string; hostname: string; contract: string }[]; hosts: { provider: string; name: string; uid: string }[]; counts: Record<string, number>; errors?: Record<string, string> };

const CATEGORY: Record<VM["category"], { tone: Tone; label: string; text: string }> = {
  managed: { tone: "green", label: "managed", text: "belongs to one of Gaia's contracts" },
  orphan: { tone: "orange", label: "orphan", text: "carries a Gaia UID that Gaia no longer knows" },
  unmanaged: { tone: "blue", label: "unmanaged", text: "created outside Gaia; it can be adopted" },
  template: { tone: "gray", label: "template", text: "an image template" },
};

export function Discovery() {
  const session = useSession();
  const qc = useQueryClient();
  const operator = hasRole(session, "operator");
  const q = useQuery({ queryKey: ["discovery"], queryFn: () => api<Report>("/discovery"), enabled: operator });
  const [note, setNote] = useState<string | null>(null);
  const [cat, setCat] = useState<VM["category"] | "">("unmanaged");
  const request = async () => {
    try {
      await api("/discovery", { method: "POST" });
      setNote("Discovery requested; the report refreshes here when it is done.");
      setTimeout(() => void qc.invalidateQueries({ queryKey: ["discovery"] }), 5000);
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e));
    }
  };
  const vms = (q.data?.vms ?? []).filter((v) => !cat || v.category === cat);
  return (
    <>
      <TabHead icon="compass" text="What the providers have, compared with what Gaia knows."
        hint="Discovery compares everything at the compute and monitoring providers with what Gaia knows: the servers Gaia manages, the ones it does not, and the ones gone missing."
        right={operator && <Button icon="compass" onClick={request}>Discover now</Button>} />
      {note && <div className="notice" role="status">{note}</div>}
      {!operator ? <div className="notice warn">Discovery reports are for operators and platform admins.</div>
        : q.isPending ? <Loading />
        : q.error ? (q.error instanceof ApiError && q.error.status === 404 ? <div className="notice">There is no discovery report yet.</div> : <ErrorNote error={q.error} />)
        : (
          <>
            <StatusBar items={[
              { icon: "clock", title: "Report", value: <span style={{ fontSize: 15 }}>{ago(q.data.takenAt)}</span>, sub: stamp(q.data.takenAt) },
              ...(["managed", "unmanaged", "orphan"] as const).map((k) => ({
                icon: (k === "managed" ? "check-circle" : k === "unmanaged" ? "plus" : "alert") as "check-circle",
                title: CATEGORY[k].label, hint: CATEGORY[k].text,
                value: <IL icon={k === "managed" ? "check-circle" : k === "unmanaged" ? "plus" : "alert"} color={`var(--${CATEGORY[k].tone})`} size={18}>{q.data.vms.filter((v) => v.category === k).length}</IL>,
              })),
              { icon: "x-circle", title: "Missing", hint: "VMs Gaia knows that the provider does not have.", value: <IL icon="x-circle" color={q.data.missing.length ? "var(--red)" : "var(--fg4)"} size={18}>{q.data.missing.length}</IL> },
            ]} />
            {q.data.errors && Object.keys(q.data.errors).length > 0 && Object.entries(q.data.errors).map(([p, e]) => <div key={p} className="notice err"><b>{p}</b>: <Mono>{e}</Mono></div>)}
            {q.data.missing.length > 0 && (
              <Card title="Missing servers" icon="x-circle" hint="VMs Gaia knows that the provider does not have. They may have been deleted or moved.">
                <table className="grid">
                  <thead><tr><th>Server</th><th>Contract</th><th>Provider</th><th>VM</th></tr></thead>
                  <tbody>{q.data.missing.map((m) => <tr key={m.uid}><td>{m.hostname} <Mono>{m.uid}</Mono></td><td><Link to={`/contracts/${encodeURIComponent(m.contract)}`}>{m.contract}</Link></td><td>{m.provider}</td><td className="mono">{m.id}</td></tr>)}</tbody>
                </table>
              </Card>
            )}
            <Card title="VMs" icon="vm" right={
              <span className="row" style={{ gap: 6 }}>
                {(["", "unmanaged", "orphan", "managed", "template"] as const).map((k) => (
                  <button key={k} type="button" className={k === cat ? "btn primary" : "btn"} style={{ height: 30 }} onClick={() => setCat(k)}>{k ? CATEGORY[k].label : "all"}</button>
                ))}
              </span>}>
              {vms.length === 0 ? <div className="empty">No VMs of this kind.</div> : (
                <table className="grid">
                  <thead><tr><th>Name</th><th>Kind</th><th>Provider</th><th>State</th><th>IP</th><th>Contract</th></tr></thead>
                  <tbody>
                    {vms.map((v) => (
                      <tr key={v.provider + v.id}>
                        <td><span className="il"><Icon name="vm" size={14} />{v.name}</span>{v.uid && <div className="mono faint">{v.uid}</div>}</td>
                        <td><Badge tone={CATEGORY[v.category].tone}>{CATEGORY[v.category].label}</Badge></td>
                        <td>{v.provider} <span className="mono faint">{v.id}</span></td>
                        <td className="muted">{v.powerState}</td>
                        <td className="mono">{(v.ips ?? []).join(", ") || "–"}</td>
                        <td>{v.contract ? <Link to={`/contracts/${encodeURIComponent(v.contract)}`}>{v.contract}</Link> : <span className="faint">–</span>}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </Card>
          </>
        )}
    </>
  );
}
