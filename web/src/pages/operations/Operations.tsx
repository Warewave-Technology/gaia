// Operations: one-off jobs on servers and the schedules that write them
// (design 6.4): what each does, when, and how it went.
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../../api/client";
import { hasRole, useSession } from "../../api/session";
import { Icon, type IconName } from "../../ui/icons";
import { Badge, Card, ErrorNote, Hint, Leg, Loading, Mono, type Tone } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { stamp } from "../../lib/time";

type Operation = {
  name: string; status?: string; change?: string; run?: string; updatedAt?: string; finishedAt?: string;
  action?: string; selector?: string; window?: { start: string; end: string }; notAfter?: string; hosts?: string[]; schedule?: string;
};
type Schedule = { name: string; recurrence: string; start: string; timezone?: string; duration: string; createAhead?: string; approval?: string; action: string; selector: string; operations: string[] };

const STATUS: Record<string, { tone: Tone; icon: IconName; label: string; text: string }> = {
  "": { tone: "gray", icon: "clock", label: "Waiting", text: "Gaia has not taken it up; it waits for its change's approval or its window" },
  scheduled: { tone: "blue", icon: "calendar", label: "Scheduled", text: "approved; it starts when its window comes" },
  running: { tone: "yellow", icon: "play", label: "Running", text: "running on the servers, batch by batch" },
  succeeded: { tone: "green", icon: "check", label: "Succeeded", text: "succeeded on every server" },
  partial: { tone: "orange", icon: "alert", label: "Partial", text: "failed on some servers" },
  failed: { tone: "red", icon: "x", label: "Failed", text: "did not run, or failed on too many servers" },
  cancelled: { tone: "gray", icon: "stop", label: "Cancelled", text: "someone stopped it" },
  expired: { tone: "gray", icon: "clock", label: "Expired", text: "its window or notAfter date passed" },
};

const ACTION: Record<string, string> = { reboot: "reboot", patch: "patch", runbook: "runbook", replace: "replace" };

const DAYS: Record<string, string> = { MO: "Monday", TU: "Tuesday", WE: "Wednesday", TH: "Thursday", FR: "Friday", SA: "Saturday", SU: "Sunday" };

// recurrence reads the RRULEs these schedules use; others show as written.
function ordinal(n: number): string {
  return n === 1 ? "1st" : n === 2 ? "2nd" : n === 3 ? "3rd" : `${n}th`;
}

export function recurrence(rrule: string): string {
  const r = Object.fromEntries(rrule.replace(/^RRULE:/, "").split(";").map((kv) => kv.split("=") as [string, string]));
  const days = (r.BYDAY ?? "").split(",").filter(Boolean).map((d) => {
    const m = /^(-?\d)?([A-Z]{2})$/.exec(d);
    if (!m) return d;
    const day = DAYS[m[2]!] ?? m[2]!;
    return m[1] ? (m[1] === "-1" ? `last ${day}` : `${ordinal(Number(m[1]))} ${day}`) : day;
  });
  const every = r.INTERVAL && r.INTERVAL !== "1" ? `${r.INTERVAL} ` : "";
  switch (r.FREQ) {
    case "DAILY": return every ? `Every ${every}days` : "Every day";
    case "WEEKLY": return `${every ? `Every ${every}weeks` : "Every week"}${days.length ? " on " + days.join(", ") : ""}`;
    case "MONTHLY": return `${every ? `Every ${every}months` : "Every month"}${r.BYMONTHDAY ? ` on day ${r.BYMONTHDAY}` : days.length ? " on the " + days.join(", ") : ""}`;
    default: return rrule;
  }
}

function StatusBadge({ s }: { s?: string }) {
  const l = STATUS[s ?? ""] ?? { tone: "gray" as Tone, icon: "help" as IconName, label: s ?? "" };
  return <Badge tone={l.tone} icon={l.icon}>{l.label}</Badge>;
}

export function Operations() {
  const session = useSession();
  const operator = hasRole(session, "operator");
  const [params] = useSearchParams();
  const [q, setQ] = useState(params.get("q") ?? "");
  const ops = useQuery({ queryKey: ["operations"], queryFn: () => api<Operation[]>("/operations"), enabled: operator, refetchInterval: 30_000 });
  const scheds = useQuery({ queryKey: ["schedules"], queryFn: () => api<Schedule[]>("/schedules"), enabled: operator });
  const t = q.trim().toLowerCase();
  const shown = (ops.data ?? []).filter((o) => !t || [o.name, o.action, o.selector, o.schedule].some((x) => x?.toLowerCase().includes(t)));
  return (
    <Page crumbs="Operations">
      <div className="title-row">
        <h1>Operations</h1>
        <Hint width={340}>An Operation is a one-off job on servers: a reboot, a patch, a runbook. It is written to the contract repository, approved like a change, and runs in its window, in batches. An OperationSchedule writes Operations at set times.</Hint>
      </div>
      {!operator ? <div className="notice warn">Operations are for operators and platform admins.</div> : (
        <>
          <Card title="Schedules" icon="calendar" hint="The OperationSchedules in the contract repository. Each writes Operations ahead of its next windows." right={<span className="faint">{scheds.data?.length ?? 0} schedules</span>}>
            {scheds.isPending ? <Loading /> : scheds.error ? <ErrorNote error={scheds.error} /> : scheds.data.length === 0 ? <div className="empty">No schedules.</div> : (
              <table className="grid">
                <thead>
                  <tr>
                    <th>Name</th><th>When</th><th>Duration</th><th>Action</th><th>Selector</th>
                    <th><span className="th-hint">Approval<Hint><b className="term">once</b>: the schedule is approved once and the Operations it writes run without approval. <b className="term">perInstance</b>: each Operation is approved on its own.</Hint></span></th>
                    <th><span className="th-hint">Written<Hint right>The schedule's Operations in the contract repository now.</Hint></span></th>
                  </tr>
                </thead>
                <tbody>
                  {scheds.data.map((s) => (
                    <tr key={s.name}>
                      <td className="nowrap"><span className="il"><Icon name="calendar" size={14} />{s.name}</span></td>
                      <td className="nowrap" title={s.recurrence}>{recurrence(s.recurrence)} <span className="muted">· {s.start}{s.timezone ? ` ${s.timezone}` : " UTC"}</span></td>
                      <td>{s.duration}</td>
                      <td className="nowrap">{ACTION[s.action] ?? s.action}</td>
                      <td className="nowrap"><Mono>{s.selector}</Mono></td>
                      <td>{s.approval || "perInstance"}</td>
                      <td>{s.operations.length}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Card>
          <Card title="Operations" icon="zap" hint={<>The Operations in the contract repository and the ones Gaia took up, newest first.
            {Object.entries(STATUS).map(([k, l]) => <Leg key={k} icon={l.icon} color={`var(--${l.tone === "gray" ? "fg4" : l.tone})`} name={l.label}>{l.text}</Leg>)}</>}
            right={
              <label className="search" style={{ width: 260 }}>
                <Icon name="filter" size={14} />
                <span className="sr-only">Filter</span>
                <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Name, action or selector" />
              </label>
            }>
            {ops.isPending ? <Loading /> : ops.error ? <ErrorNote error={ops.error} /> : shown.length === 0 ? <div className="empty">No operations.</div> : (
              <table className="grid">
                <thead><tr><th>Name</th><th>Action</th><th>Selector</th><th>Window</th><th>Status</th><th>Change</th><th>Run</th></tr></thead>
                <tbody>
                  {shown.map((o) => (
                    <tr key={o.name}>
                      <td className="nowrap">
                        <span className="il"><Icon name="zap" size={14} />{o.name}</span>
                        {o.schedule && <div className="faint" style={{ fontSize: 12, marginTop: 2 }} title="The schedule that wrote it"><span className="il" style={{ fontWeight: 400 }}><Icon name="calendar" size={12} />{o.schedule.replace(/^OperationSchedule\//, "")}</span></div>}
                      </td>
                      <td className="nowrap">{o.action ? ACTION[o.action] ?? o.action : <span className="faint">–</span>}{o.hosts?.length ? <div className="faint" style={{ fontSize: 12 }}>{o.hosts.join(", ")}</div> : null}</td>
                      <td className="nowrap">{o.selector ? <Mono>{o.selector}</Mono> : <span className="faint">not in the repository</span>}</td>
                      <td className="muted nowrap">{o.window ? <>{stamp(o.window.start)}<div>{stamp(o.window.end)}</div></> : o.notAfter ? `by ${stamp(o.notAfter)}` : "–"}</td>
                      <td><StatusBadge s={o.status} /></td>
                      <td className="nowrap">{o.change ? <Link to={`/changes/${encodeURIComponent(o.change)}`}><Mono>{o.change}</Mono></Link> : <span className="faint">–</span>}</td>
                      <td className="nowrap">{o.run ? <Link to={`/runs/${encodeURIComponent(o.run)}`}><Mono>{o.run}</Mono></Link> : <span className="faint">–</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Card>
        </>
      )}
    </Page>
  );
}
