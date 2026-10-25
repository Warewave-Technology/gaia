// A run: its stages, the server × task matrix and the live log.
import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { api, ApiError } from "../../api/client";
import { hasRole, useSession } from "../../api/session";
import type { Run, RunEvent, Stage } from "../../api/types";
import { Icon, type IconName } from "../../ui/icons";
import { Button, Card, ErrorNote, IL, Leg, LinkButton, Loading, Mono, short, StatusBar, Term } from "../../ui/parts";
import { RecordTip } from "../../ui/record";
import { Page } from "../../ui/shell";
import { RecordLegend, RUN, RunBadge } from "../../ui/status";
import { line, matrix, type Line, type Mark } from "../../lib/events";
import { clock, duration } from "../../lib/time";

const STAGES: Record<string, { icon: IconName; text: string }> = {
  ipam: { icon: "network", text: "IP adresleri IPAM'dan ayrılır." },
  restore: { icon: "rerun", text: "Durdurulmuş sunucular geri açılır." },
  decommission: { icon: "x", text: "Kaldırılan sunucular kapatılır." },
  deregister: { icon: "activity", text: "Kaldırılan sunucular izlemeden çıkarılır." },
  provision: { icon: "vm", text: "Pulumi VM'leri oluşturur veya günceller." },
  release: { icon: "network", text: "Boşalan IP'ler IPAM'a geri verilir." },
  register: { icon: "activity", text: "Sunucular izlemeye (Zabbix), DNS'e ve AD'ye kaydedilir." },
  configure: { icon: "wrench", text: "Ansible sunucuları yapılandırır." },
  access: { icon: "user", text: "Erişim (kullanıcılar, sudo) dağıtılır." },
  record: { icon: "lock", text: "Run kaydı imzalanıp değiştirilemez arşive yazılır." },
  operation: { icon: "wrench", text: "Operation'ın aksiyonu (ör. yeniden başlatma) çalışır." },
};

const STAGE_COLOR: Record<Stage["status"], string> = {
  running: "var(--yellow)",
  succeeded: "var(--green)",
  failed: "var(--red)",
  cancelled: "var(--gray)",
};

type RecordBody = { approvedBy?: string[]; changes?: { stage: string; action: string }[] };

export function RunDetail() {
  const { id = "" } = useParams();
  const q = useQuery({
    queryKey: ["run", id],
    queryFn: () => api<Run>(`/runs/${encodeURIComponent(id)}`),
    refetchInterval: (query) => (query.state.data && ["queued", "running"].includes(query.state.data.status) ? 3000 : false),
  });
  const crumbs = <><Link to="/runs">Run'lar</Link> / <Mono>{id}</Mono></>;
  if (q.isPending) return <Page crumbs={crumbs}><Loading /></Page>;
  if (q.error) {
    return (
      <Page crumbs={crumbs}>
        {q.error instanceof ApiError && q.error.status === 403 ? <div className="notice warn">Bu run'ın kontratını sadece adıyla görebilirsiniz.</div> : <ErrorNote error={q.error} />}
      </Page>
    );
  }
  return <Body key={q.data.id} run={q.data} crumbs={crumbs} />;
}

function useEvents(id: string) {
  const [events, setEvents] = useState<RunEvent[]>([]);
  const [end, setEnd] = useState<string | null>(null);
  const [broken, setBroken] = useState(false);
  const qc = useQueryClient();
  // A new run id mounts a new Body (see key below), so the state starts
  // empty for each run.
  useEffect(() => {
    let last = 0;
    let buf: RunEvent[] = [];
    let timer: ReturnType<typeof setTimeout> | undefined;
    const flush = () => {
      timer = undefined;
      if (buf.length === 0) return;
      const b = buf;
      buf = [];
      setEvents((prev) => prev.concat(b));
    };
    const es = new EventSource(`/api/v1/runs/${encodeURIComponent(id)}/events/stream`);
    const on = (m: MessageEvent<string>) => {
      const e = JSON.parse(m.data) as RunEvent;
      if (e.seq <= last) return;
      last = e.seq;
      buf.push(e);
      timer ??= setTimeout(flush, 100);
    };
    es.addEventListener("log", on);
    es.addEventListener("ansible", on);
    es.addEventListener("end", (m: MessageEvent<string>) => {
      flush();
      es.close();
      setEnd((JSON.parse(m.data) as { status: string }).status);
      void qc.invalidateQueries({ queryKey: ["run", id] });
    });
    es.onerror = () => {
      if (es.readyState === EventSource.CLOSED) setBroken(true);
    };
    return () => {
      es.close();
      if (timer) clearTimeout(timer);
    };
  }, [id, qc]);
  return { events, end, broken };
}

function Body({ run, crumbs }: { run: Run; crumbs: React.ReactNode }) {
  const session = useSession();
  const { events, end, broken } = useEvents(run.id);
  const hosts = run.hosts ?? {};
  const m = useMemo(() => matrix(events), [events]);
  const rec = useQuery({
    queryKey: ["run", run.id, "record"],
    queryFn: () => api<{ body: RecordBody }>(`/runs/${encodeURIComponent(run.id)}/record`),
    enabled: !!run.record,
  });
  const [verify, setVerify] = useState<string | null>(null);
  const live = !end && ["queued", "running"].includes(run.status);
  const changed = Object.values(m.summary).reduce((n, s) => n + s.changed, 0);
  const failed = Object.values(m.summary).reduce((n, s) => n + s.failed, 0);
  // The record names each server the run creates in its plan stage.
  const created = (rec.data?.body.changes ?? []).filter((c) => c.stage === "plan" && (c.action === "create" || c.action === "adopt")).length;
  const approvers = rec.data?.body.approvedBy ?? [];
  const contract = run.contracts[0];

  const cancel = async () => {
    await api(`/runs/${encodeURIComponent(run.id)}/cancel`, { method: "POST" }).catch(() => undefined);
  };
  const check = async () => {
    setVerify("Zincir doğrulanıyor…");
    try {
      const r = await api<{ records: number; intact: boolean; broken?: { seq: number; run: string; reason: string } }>("/records/verify");
      setVerify(r.intact ? `Zincir sağlam: ${r.records} kayıt.` : `Zincir bozuk: #${r.broken?.seq} (${r.broken?.run}): ${r.broken?.reason}`);
    } catch (e) {
      setVerify(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <Page crumbs={crumbs}>
      <div className="title-row">
        <span style={{ color: RUN[run.status]?.tone === "red" ? "var(--red)" : "var(--green)" }}><Icon name="play" size={22} /></span>
        <h1 className="mono" style={{ fontSize: 22 }}>{run.id}</h1>
        <RunBadge status={run.status} />
        {live && <span className="badge yellow" role="status"><Icon name="activity" size={13} />canlı</span>}
      </div>

      <div className="spread">
        <div className="row">
          {contract && <LinkButton to={`/contracts/${encodeURIComponent(contract)}`} icon="file">Kontrat</LinkButton>}
          {run.change && <LinkButton to={`/changes/${encodeURIComponent(run.change)}`} icon="pr">Change</LinkButton>}
          <a className="btn" href={`/api/v1/runs/${encodeURIComponent(run.id)}/events?limit=5000`} target="_blank" rel="noreferrer" title="Run'ın bütün olayları (JSON)"><Icon name="terminal" size={15} />Ham olaylar</a>
        </div>
        <div className="row">
          {run.record && <Button icon="lock" onClick={check} title="Bütün run kayıtları zincirinin bozulmadığını kontrol et">Zinciri doğrula</Button>}
          {live && hasRole(session, "operator") && <Button kind="danger" icon="stop" onClick={cancel}>Durdur</Button>}
        </div>
      </div>
      {verify && <div className={verify.startsWith("Zincir sağlam") ? "notice" : "notice warn"} role="status">{verify}</div>}

      <StatusBar items={[
        {
          icon: "play", title: "Sonuç", hint: <RecordLegend />,
          value: <span className="row" style={{ gap: 8 }}>
            <IL icon={run.status === "succeeded" ? "check-circle" : run.status === "failed" ? "x-circle" : RUN[run.status]?.icon ?? "help"} color={`var(--${RUN[run.status]?.tone === "gray" ? "fg4" : RUN[run.status]?.tone ?? "fg4"})`} size={20}>{RUN[run.status]?.label}</IL>
            <RecordTip run={run} />
          </span>,
          sub: <>{duration(run.startedAt, run.finishedAt)}{run.startedAt && ` · ${clock(run.startedAt)}${run.finishedAt ? " – " + clock(run.finishedAt) : ""}`}{run.error && <span className="error"> · {run.error}</span>}</>,
        },
        {
          icon: "file", title: "Kontrat", hint: "Run'ın uyguladığı kontrat(lar) ve commit'i.",
          value: <span className="row">{run.contracts.map((c) => <Link key={c} to={`/contracts/${encodeURIComponent(c)}`} style={{ color: "var(--fg)" }}>{c}</Link>)}</span>,
          sub: <>commit <Mono>{short(run.commit)}</Mono></>,
        },
        {
          icon: "user", title: "Kişiler", hint: <><Term>İstedi</Term>: commit'in sahibi (imzasından).<br /><Term>Onayladı</Term>: planı passkey ile onaylayan.</>,
          value: <span className="row" style={{ fontSize: 15 }}>{run.requestedBy || "gaia"}{approvers.length > 0 && <><span className="faint"><Icon name="arrow" size={14} /></span>{approvers.join(", ")}</>}</span>,
          sub: approvers.length ? "istedi · onayladı" : "istedi",
        },
        {
          icon: "diff", title: "Değişen", hint: <>Run'ın gerçekte değiştirdikleri: oluşturulan sunucular ve Ansible'ın <Term>changed</Term> saydığı task'lar.</>,
          value: <span className="row" style={{ gap: 10 }}>
            {created > 0 && <IL icon="plus" color="var(--green)" size={16}>{created} sunucu</IL>}
            <IL icon="pencil" color="var(--yellow)" size={15}>{changed} task</IL>
          </span>,
          sub: `failed ${failed}`,
        },
      ]} />

      <Stages run={run} />
      <MatrixCard m={m} hosts={hosts} live={live} />
      <Log events={events} hosts={hosts} live={live} broken={broken} />
    </Page>
  );
}

function Stages({ run }: { run: Run }) {
  const stages = run.stages ?? [];
  const many = run.contracts.length > 1;
  return (
    <Card title="Aşamalar" icon="layers" hint={<>Run'ın sırayla geçtiği adımlar; biri başarısız olursa sonrakiler çalışmaz.
      {Object.entries(STAGES).map(([k, s]) => <Leg key={k} icon={s.icon} color="var(--fg2)" name={k}>{s.text}</Leg>)}</>}>
      {stages.length === 0 ? <div className="empty">Henüz bir aşama başlamadı.</div> : (
        <ol style={{ listStyle: "none", margin: 0, padding: 16, display: "flex", flexWrap: "wrap", alignItems: "stretch" }}>
          {stages.map((s, i) => {
            const meta = STAGES[s.stage] ?? { icon: "info" as IconName, text: "" };
            const color = STAGE_COLOR[s.status];
            return (
              <Fragment key={`${s.contract}/${s.stage}`}>
                {i > 0 && <div aria-hidden="true" style={{ flex: "1 1 16px", minWidth: 16, height: 2, background: STAGE_COLOR[stages[i - 1]!.status], alignSelf: "center", opacity: 0.7 }} />}
                <li title={meta.text} style={{ flex: "0 1 180px", minWidth: 132, background: "var(--bg0)", border: "1px solid var(--bg2)", borderLeft: `4px solid ${color}`, borderRadius: 6, padding: "10px 12px", display: "flex", gap: 10, alignItems: "center" }}>
                  <span style={{ color }}><Icon name={s.status === "failed" ? "x-circle" : meta.icon} size={18} /></span>
                  <span>
                    <span style={{ display: "block", fontWeight: 600 }}>{s.stage}{many && s.contract && <span className="faint" style={{ fontWeight: 400 }}> · {s.contract}</span>}</span>
                    <span className="muted" style={{ display: "block", fontSize: 12 }}>{s.status === "running" ? "çalışıyor · " : ""}{duration(s.startedAt, s.finishedAt)}</span>
                    {s.detail && <span className={s.status === "failed" ? "error" : "muted"} style={{ display: "block", fontSize: 12, overflowWrap: "anywhere" }}>{s.detail}</span>}
                  </span>
                </li>
              </Fragment>
            );
          })}
        </ol>
      )}
    </Card>
  );
}

const MARK: Record<Mark, { tint: string; color: string; icon: IconName; label: string }> = {
  ok: { tint: "var(--tint-green)", color: "var(--green)", icon: "check", label: "ok" },
  changed: { tint: "var(--tint-yellow)", color: "var(--yellow)", icon: "pencil", label: "changed" },
  failed: { tint: "var(--tint-red)", color: "var(--red)", icon: "x", label: "failed" },
  unreachable: { tint: "var(--tint-red)", color: "var(--red)", icon: "x", label: "unreachable" },
  skipped: { tint: "transparent", color: "var(--gray)", icon: "minus", label: "skipped" },
};

function MatrixCard({ m, hosts, live }: { m: ReturnType<typeof matrix>; hosts: Record<string, string>; live: boolean }) {
  const legend = (["ok", "changed", "failed"] as Mark[]).map((k) => (
    <span key={k} className="row" style={{ gap: 6 }}>
      <span style={{ display: "inline-flex", width: 20, height: 18, alignItems: "center", justifyContent: "center", background: MARK[k].tint, color: MARK[k].color, borderRadius: 3 }}><Icon name={MARK[k].icon} size={12} width={2.6} /></span>{k}
    </span>
  ));
  return (
    <Card title="Sunucu × task" icon="grid" right={<div className="row muted" style={{ gap: 16, fontSize: 13 }}>{legend}</div>}
      hint={<>Her sunucuda her Ansible task'ının sonucu.
        <Leg icon="check" color="var(--green)" name="ok">zaten istenen durumdaydı</Leg>
        <Leg icon="pencil" color="var(--yellow)" name="changed">değiştirildi</Leg>
        <Leg icon="x" color="var(--red)" name="failed">hata verdi</Leg>
        <Leg icon="minus" color="var(--gray)" name="skipped">bu sunucuda çalışmadı</Leg></>}>
      {m.hosts.length === 0 ? <div className="empty">{live ? "Ansible henüz çalışmadı." : "Bu run'da Ansible çalışmadı."}</div> : (
        <div style={{ overflowX: "auto", padding: "8px 16px 16px" }}>
          <table style={{ borderCollapse: "separate", borderSpacing: 4, fontSize: 13 }}>
            <thead>
              <tr className="muted" style={{ textAlign: "left" }}>
                <th style={{ padding: "6px 8px" }}>Sunucu</th>
                {m.tasks.map((t) => <th key={t} style={{ padding: "6px 8px", fontWeight: 500, maxWidth: 120, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={t}>{t.replace(/^[^:]+ : /, "")}</th>)}
                <th style={{ padding: "6px 8px" }}>Özet</th>
              </tr>
            </thead>
            <tbody>
              {m.hosts.map((h) => (
                <tr key={h}>
                  <td style={{ padding: "6px 8px", fontWeight: 600, whiteSpace: "nowrap" }} title={h}>{hosts[h] ?? short(h, 10)}</td>
                  {m.tasks.map((t) => {
                    const k = m.cells[h]?.[t];
                    if (!k) return <td key={t} className="faint" style={{ textAlign: "center" }}>–</td>;
                    const look = MARK[k];
                    return <td key={t} title={`${t}: ${look.label}`} style={{ background: look.tint, color: look.color, borderRadius: 3, padding: "6px 0" }}><span style={{ display: "flex", justifyContent: "center" }}><Icon name={look.icon} size={13} width={2.6} /></span></td>;
                  })}
                  <td className="muted" style={{ padding: "6px 8px", whiteSpace: "nowrap" }}>ok={m.summary[h]?.ok} changed={m.summary[h]?.changed}{m.summary[h]?.failed ? ` failed=${m.summary[h]?.failed}` : ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

const STAGE_TAG: Record<string, string> = { provision: "var(--purple)", register: "var(--orange)", deregister: "var(--orange)", configure: "var(--aqua)", access: "var(--aqua)", record: "var(--yellow)", ipam: "var(--blue)", release: "var(--blue)" };
const KIND_COLOR: Partial<Record<Line["kind"], string>> = { changed: "var(--yellow)", failed: "var(--red)", task: "var(--fg)" };

function Log({ events, hosts, live, broken }: { events: RunEvent[]; hosts: Record<string, string>; live: boolean; broken: boolean }) {
  const lines = useMemo(() => events.map((e) => line(e, hosts)).filter((l): l is Line => l !== null), [events, hosts]);
  const box = useRef<HTMLPreElement>(null);
  const [follow, setFollow] = useState(true);
  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines, follow]);
  return (
    <section aria-labelledby="h-log" style={{ background: "var(--bg0h)", border: "1px solid var(--bg1)", borderRadius: 6, color: "var(--fg2)" }}>
      <div className="spread" style={{ padding: "12px 16px", borderBottom: "1px solid var(--bg1)" }}>
        <div className="row faint" style={{ gap: 8 }}><Icon name="terminal" size={16} /><h2 id="h-log" style={{ color: "var(--fg)" }}>Log</h2>
          {live && <span className="badge yellow">canlı</span>}
          {broken && <span className="badge red">bağlantı koptu</span>}</div>
        <div className="row faint" style={{ gap: 14, fontSize: 13 }}>
          <label className="row" style={{ gap: 6 }}><input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />sona kaydır</label>
          <span className="il" style={{ fontWeight: 400 }}><Icon name="lock" size={13} />secret'lar maskeli</span>
        </div>
      </div>
      <pre ref={box} className="mono" style={{ margin: 0, padding: "12px 16px 16px", lineHeight: 1.6, overflow: "auto", maxHeight: 480, whiteSpace: "pre-wrap" }}>
        {lines.length === 0 ? <span className="faint">{live ? "Olay bekleniyor…" : "Olay yok."}</span> : lines.map((l) => (
          <div key={l.seq}>
            <span className="faint">{new Date(l.at).toLocaleTimeString("tr-TR")}</span>{"  "}
            <span style={{ color: STAGE_TAG[l.stage] ?? "var(--fg4)", display: "inline-block", minWidth: 96 }}>{l.stage}</span>
            <span style={{ color: KIND_COLOR[l.kind] }}>{l.text}</span>
          </div>
        ))}
      </pre>
    </section>
  );
}
