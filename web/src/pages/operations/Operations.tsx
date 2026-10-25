// Operasyonlar: one-off jobs on servers and the schedules that write them
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
  "": { tone: "gray", icon: "clock", label: "Bekliyor", text: "Gaia henüz almadı; Change'inin onayını ya da penceresini bekliyor" },
  scheduled: { tone: "blue", icon: "calendar", label: "Zamanlandı", text: "onaylandı; penceresi gelince başlar" },
  running: { tone: "yellow", icon: "play", label: "Çalışıyor", text: "sunucularda sırayla çalışıyor" },
  succeeded: { tone: "green", icon: "check", label: "Bitti", text: "bütün sunucularda başarılı" },
  partial: { tone: "orange", icon: "alert", label: "Kısmen", text: "bazı sunucularda başarısız" },
  failed: { tone: "red", icon: "x", label: "Başarısız", text: "çalışmadı ya da çok sunucuda başarısız oldu" },
  cancelled: { tone: "gray", icon: "stop", label: "İptal edildi", text: "biri durdurdu" },
  expired: { tone: "gray", icon: "clock", label: "Süresi doldu", text: "penceresi ya da notAfter tarihi geçti" },
};

const ACTION: Record<string, string> = { reboot: "yeniden başlatma", patch: "yama", runbook: "runbook", replace: "yeniden kurma" };

const DAYS: Record<string, string> = { MO: "Pazartesi", TU: "Salı", WE: "Çarşamba", TH: "Perşembe", FR: "Cuma", SA: "Cumartesi", SU: "Pazar" };

// recurrence reads the RRULEs these schedules use; others show as written.
export function recurrence(rrule: string): string {
  const r = Object.fromEntries(rrule.replace(/^RRULE:/, "").split(";").map((kv) => kv.split("=") as [string, string]));
  const days = (r.BYDAY ?? "").split(",").filter(Boolean).map((d) => {
    const m = /^(-?\d)?([A-Z]{2})$/.exec(d);
    if (!m) return d;
    const day = DAYS[m[2]!] ?? m[2]!;
    return m[1] ? (m[1] === "-1" ? `son ${day}` : `${m[1]}. ${day}`) : day;
  });
  const every = r.INTERVAL && r.INTERVAL !== "1" ? `${r.INTERVAL} ` : "";
  switch (r.FREQ) {
    case "DAILY": return `Her ${every}gün`;
    case "WEEKLY": return `Her ${every}hafta${days.length ? " " + days.join(", ") : ""}`;
    case "MONTHLY": return `Her ${every}ay${r.BYMONTHDAY ? ` ${r.BYMONTHDAY}. gün` : days.length ? " ayın " + days.join(", ") : ""}`;
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
    <Page crumbs="Operasyonlar">
      <div className="title-row">
        <h1>Operasyonlar</h1>
        <Hint width={340}>Operation, sunucularda bir kez yapılacak bir iştir: yeniden başlatma, yama, runbook. Kontrat reposuna yazılır, bir Change gibi onaylanır ve penceresinde, gruplar halinde çalışır. OperationSchedule belli aralıklarla Operation yazar.</Hint>
      </div>
      {!operator ? <div className="notice warn">Operasyonları operator ve platform-admin rolleri görür.</div> : (
        <>
          <Card title="Zamanlamalar" icon="calendar" hint="Kontrat reposundaki OperationSchedule'lar. Her biri, sıradaki pencereler için önceden Operation yazar." right={<span className="faint">{scheds.data?.length ?? 0} zamanlama</span>}>
            {scheds.isPending ? <Loading /> : scheds.error ? <ErrorNote error={scheds.error} /> : scheds.data.length === 0 ? <div className="empty">Zamanlama yok.</div> : (
              <table className="grid">
                <thead>
                  <tr>
                    <th>Ad</th><th>Ne zaman</th><th>Süre</th><th>İş</th><th>Seçici</th>
                    <th><span className="th-hint">Onay<Hint><b className="term">once</b>: zamanlama bir kez onaylanır, yazdığı Operation'lar onaysız çalışır. <b className="term">perInstance</b>: her Operation ayrı onaylanır.</Hint></span></th>
                    <th><span className="th-hint">Yazdığı<Hint right>Zamanlamanın şu an kontrat reposunda duran Operation'ları.</Hint></span></th>
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
          <Card title="Operasyonlar" icon="zap" hint={<>Kontrat reposundaki Operation'lar ve Gaia'nın aldıkları, yeniden eskiye.
            {Object.entries(STATUS).map(([k, l]) => <Leg key={k} icon={l.icon} color={`var(--${l.tone === "gray" ? "fg4" : l.tone})`} name={l.label}>{l.text}</Leg>)}</>}
            right={
              <label className="search" style={{ width: 260 }}>
                <Icon name="filter" size={14} />
                <span className="sr-only">Filtre</span>
                <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Ad, iş ya da seçici" />
              </label>
            }>
            {ops.isPending ? <Loading /> : ops.error ? <ErrorNote error={ops.error} /> : shown.length === 0 ? <div className="empty">Operasyon yok.</div> : (
              <table className="grid">
                <thead><tr><th>Ad</th><th>İş</th><th>Seçici</th><th>Pencere</th><th>Durum</th><th>Change</th><th>Run</th></tr></thead>
                <tbody>
                  {shown.map((o) => (
                    <tr key={o.name}>
                      <td className="nowrap">
                        <span className="il"><Icon name="zap" size={14} />{o.name}</span>
                        {o.schedule && <div className="faint" style={{ fontSize: 12, marginTop: 2 }} title="Bunu yazan zamanlama"><span className="il" style={{ fontWeight: 400 }}><Icon name="calendar" size={12} />{o.schedule.replace(/^OperationSchedule\//, "")}</span></div>}
                      </td>
                      <td className="nowrap">{o.action ? ACTION[o.action] ?? o.action : <span className="faint">–</span>}{o.hosts?.length ? <div className="faint" style={{ fontSize: 12 }}>{o.hosts.join(", ")}</div> : null}</td>
                      <td className="nowrap">{o.selector ? <Mono>{o.selector}</Mono> : <span className="faint">repoda yok</span>}</td>
                      <td className="muted nowrap">{o.window ? <>{stamp(o.window.start)}<div>{stamp(o.window.end)}</div></> : o.notAfter ? `en geç ${stamp(o.notAfter)}` : "–"}</td>
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
