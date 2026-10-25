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
  const crumbs = <><Link to="/changes">Değişiklikler</Link> / {q.data?.contract ?? id}</>;
  if (q.isPending) return <Page crumbs={crumbs}><Loading /></Page>;
  if (q.error) {
    const restricted = q.error instanceof ApiError && q.error.status === 403;
    return (
      <Page crumbs={crumbs}>
        {restricted ? <div className="notice warn">Bu kontratın sadece adını görebilirsiniz; ayrıntıları kapalı.</div> : <ErrorNote error={q.error} />}
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
          <LinkButton to={`/contracts/${encodeURIComponent(c.contract)}`} icon="file">Kontrat</LinkButton>
          {c.runId && <LinkButton to={`/runs/${encodeURIComponent(c.runId)}`} icon="play">Run</LinkButton>}
        </div>
        {canDecide && step === "idle" && (
          <div className="row">
            <Button kind="danger" icon="x" onClick={() => setStep("reject")}>Reddet</Button>
            <Button kind="primary" icon="check" onClick={() => setStep("approve")}>Onayla</Button>
          </div>
        )}
      </div>

      <StatusBar items={statusItems(c, plan)} />

      {step !== "idle" && <Decision c={c} approve={step === "approve"} onDone={() => setStep("idle")} />}

      <div className="row" style={{ alignItems: "flex-start", gap: 16 }}>
        <Gates c={c} />
        <section className="card" style={{ flex: "2 1 520px", minWidth: 0 }} aria-label="Değişikliğin içeriği">
          <div role="tablist" aria-label="Görünüm" className="row" style={{ gap: 4, padding: "0 8px", borderBottom: "1px solid var(--bg1)" }}>
            <Tab on={tab === "plan"} icon="plan" onClick={() => setTab("plan")}>Plan</Tab>
            <Tab on={tab === "diff"} icon="diff" onClick={() => setTab("diff")}>YAML diff</Tab>
            <Tab on={tab === "commits"} icon="commit" onClick={() => setTab("commits")}>Commit'ler</Tab>
            <Tab on={tab === "raw"} icon="code" onClick={() => setTab("raw")}>Plan JSON</Tab>
          </div>
          {tab === "plan" && (plan ? <PlanView plan={plan} /> : <div className="card-body muted">Bu değişikliğin planı sunucu planı değil; ayrıntısı Plan JSON'da.</div>)}
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
      icon: "sync", title: "Sync durumu", hint: <SyncLegend />,
      value: open ? <Sync state="out-of-sync" size={20} /> : c.status === "applied" ? <Sync state="synced" size={20} /> : <ChangeBadge status={c.status} />,
      sub: <>{shortLabel(c.label)}{c.commits ? ` · ${(c.commits as Commit[]).length} commit` : ""}</>,
    },
    {
      icon: "user", title: "Onay",
      hint: <>Gereken onaylar ve kimin onayladığı. Onay plan hash'ine bağlıdır; plan değişirse düşer.
        <Leg icon="clock" color="var(--blue)" name="Bekliyor">onay verilmedi</Leg>
        <Leg icon="check-circle" color="var(--green)" name="Onaylandı">gereken onaylar tamam</Leg>
        <Leg icon="x-circle" color="var(--red)" name="Reddedildi">bir onaycı reddetti</Leg></>,
      value: !ag ? <span className="muted" style={{ fontSize: 15 }}>gerekmiyor</span>
        : rejected ? <IL icon="x-circle" color="var(--red)" size={20}>Reddedildi</IL>
        : ag.status === "ok" ? <IL icon="check-circle" color="var(--green)" size={20}>{need.via ? "Onaylı" : `${approved}/${need.groups.length}`}</IL>
        : <IL icon="clock" color="var(--blue)" size={20}>{approved}/{need.groups.length}</IL>,
      sub: need.via ? `onay: ${need.via}` : need.groups.map((g) => g.approvedBy ?? `bekleniyor: ${g.candidates.join(", ") || "kimse yok"}`).join(" · "),
    },
  ];
  if (plan) {
    items.push({
      icon: "plan", title: "Plan", hint: <>Gaia'nın bu commit için hesapladığı değişiklik. Hash planın parmak izidir.<PlanLegend /></>,
      value: <><CountsView c={total(plan)} size={18} /><span className="muted" style={{ fontSize: 15, fontWeight: 500 }}>sunucu</span></>,
      sub: <Mono>{short(c.planHash, 19)}…</Mono>,
    });
  }
  items.push({
    icon: "key", title: "İmza", hint: "Commit'lerin imzası ve imzalayan anahtarın Gaia'da kayıtlı sahibi.",
    value: !sg ? <span className="muted" style={{ fontSize: 15 }}>–</span>
      : sg.status === "ok" ? <IL icon="check-circle" color="var(--green)" size={20}>Doğrulandı</IL>
      : sg.status === "failed" ? <IL icon="x-circle" color="var(--red)" size={20}>İmzasız</IL>
      : <IL icon="clock" color="var(--blue)" size={20}>Bekliyor</IL>,
    sub: signers.join(", "),
  });
  return items;
}

const GATES: Record<string, { title: string; icon: IconName; hint: string }> = {
  validation: { title: "Validasyon", icon: "check", hint: "Kontratın katalogla tutarlı olduğu, parametrelerin şemaya ve CEL kurallarına uyduğu kontrol edilir." },
  signature: { title: "İmza", icon: "key", hint: "Commit, kişinin Gaia profiline kaydettiği SSH veya OpenPGP anahtarıyla imzalanmış olmalı." },
  approval: { title: "Onay", icon: "user", hint: "Policy'nin istediği onaylar. Onay plan hash'ine bağlıdır; plan değişirse onay düşer. Commit sahibi kendi değişikliğini onaylayamaz." },
  window: { title: "Pencere", icon: "clock", hint: "Operation'ın çalışabileceği zaman aralığı." },
};

function GateMark({ status }: { status: Gate["status"] }) {
  if (status === "ok") return <span style={{ color: "var(--green)" }} aria-label="Tamam"><Icon name="check-circle" size={18} /></span>;
  if (status === "failed") return <span style={{ color: "var(--red)" }} aria-label="Başarısız"><Icon name="x-circle" size={18} /></span>;
  return <span style={{ color: "var(--blue)" }} aria-label="Bekliyor"><Icon name="clock" size={18} /></span>;
}

function gateDetail(c: Change, g: Gate): ReactNode {
  const d = (g.detail ?? {}) as Record<string, unknown>;
  if (typeof d.error === "string") return <span className="error">{d.error}</span>;
  switch (g.gate) {
    case "validation":
      return g.status === "ok" ? "Graf, params ve CEL kuralları geçti" : "Validasyon geçmedi";
    case "signature": {
      if (g.status === "ok") return `İmzalayan: ${((d.signers as string[]) ?? []).join(", ")}`;
      const bad = (d.commits as Commit[] | undefined) ?? [];
      return <>İmzasız ya da tanınmayan: {bad.map((x) => <Mono key={x.commit}>{short(x.commit)} </Mono>)}{bad[0]?.problem && <span className="muted"> · {bad[0].problem}</span>}</>;
    }
    case "approval": {
      const need = approvalNeed(c);
      if (need.via) return `Onay: ${need.via}`;
      return (
        <>
          {need.groups.map((gr) => (
            <div key={gr.name}>
              {gr.teams.join(", ")} {gr.role === "approver" ? "onaycılarından" : "üyelerinden"} biri:{" "}
              {gr.approvedBy ? <b style={{ color: "var(--green)" }}>{gr.approvedBy} onayladı</b> : <span>bekleniyor ({gr.candidates.join(", ") || "onaylayabilecek kimse yok"})</span>}
            </div>
          ))}
          {need.excluded.length > 0 && <div className="faint" style={{ fontSize: 13 }}>{need.excluded.join(", ")} bu değişikliği onaylayamaz</div>}
        </>
      );
    }
    case "window":
      return d.start || d.end ? `${String(d.start ?? "")} – ${String(d.end ?? "")}` : "Her zaman";
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
        <div className="card-title"><Icon name="check-circle" size={16} /><h2 id="h-gates">Uygulanması için gerekenler</h2>
          <Hint>Gaia değişikliği bunların hepsi sağlanınca kendiliğinden uygular. Biri eksikse Change bekler.</Hint></div>
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
        {c.status === "pending" && waiting ? "Gereksinimler sağlanınca Gaia uygulamayı kendisi başlatır."
          : c.status === "rejected" ? "Reddedildi. Kontratı değiştiren yeni bir commit gelince yeni bir Change açılır."
          : c.status === "approved" || c.status === "running" ? "Gereksinimlerin hepsi sağlandı; Gaia uyguluyor."
          : <ChangeBadge status={c.status} />}
      </div>
    </section>
  );
}

function PlanView({ plan }: { plan: Plan }) {
  const rows = servers(plan);
  const AREA_ICON: Record<string, IconName> = { ipam: "network", provisioning: "vm", monitoring: "activity", configuration: "wrench" };
  const AREA_HINT: Record<string, string> = {
    ipam: "IP adresleri.",
    provisioning: "VM'ler: Pulumi'nin oluşturacağı, değiştireceği veya sileceği makineler.",
    monitoring: "İzleme sistemindeki (Zabbix) host kayıtları.",
    configuration: "Ansible'ın çalışacağı sunucular. Konfigürasyon her sunucuda baştan doğrulanır.",
  };
  const contract = plan.contracts?.[0];
  return (
    <div className="card-body" style={{ display: "flex", flexDirection: "column", gap: 16 }}>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(4, minmax(0, 1fr))", gap: 12 }}>
        {areas(plan).map((a) => (
          <div key={a.key} style={{ background: "var(--bg0)", border: "1px solid var(--bg2)", borderRadius: 6, padding: "10px 12px" }}>
            <div className="label-caps row" style={{ gap: 6 }}><Icon name={AREA_ICON[a.key] ?? "info"} size={14} />{a.label}<Hint width={240}>{AREA_HINT[a.key]}<PlanLegend /></Hint></div>
            <div className="row" style={{ marginTop: 4, gap: 10, fontWeight: 600 }}>
              {a.key === "configuration" ? <span>{a.counts.same} sunucu</span> : <CountsView c={a.counts} />}
              {a.providers.length > 0 && <span className="muted" style={{ fontWeight: 400 }}>{a.providers.join(", ")}</span>}
            </div>
          </div>
        ))}
      </div>
      <table className="grid">
        <thead><tr><th style={{ width: 32 }} /><th>Sunucu</th><th>Yer</th><th>Boyut</th><th>IP</th><th>UID</th></tr></thead>
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
                <td className="muted">{s.ipam?.pool ? `IPAM'dan (${s.ipam.pool})` : ""}</td>
                <td className="mono">{s.uid ? short(s.uid, 8) + "…" : <span className="muted">uygulamada</span>}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {contract?.access && contract.access.length > 0 && <AccessView access={contract.access} />}
      {contract && (
        <div className="row muted" style={{ gap: "6px 16px" }}>
          {contract.monitoring && <span className="il" style={{ fontWeight: 400 }}><Icon name="activity" size={14} />İzleme: {contract.monitoring.provider}{contract.monitoring.templates?.length ? ` · ${contract.monitoring.templates.join(", ")}` : ""}</span>}
          {contract.baseline && <span className="il" style={{ fontWeight: 400 }}><Icon name="layers" size={14} />Konfigürasyon: baseline {baselineName(contract)}{contract.roles?.length ? ` + ${contract.roles.map((r) => r.name).join(", ")}` : ""}</span>}
        </div>
      )}
    </div>
  );
}

function AccessView({ access }: { access: PlanAccess[] }) {
  return (
    <div>
      <div className="label-caps row" style={{ gap: 6, marginBottom: 8 }}><Icon name="user" size={14} />Erişim
        <Hint width={300}>Sunuculara kimin girebildiği. Kişiler takımlardan gelir; kullanıcı adları Git'e girmez.
          <Leg icon="key" color="var(--red)" name="tam sudo">her komutu root olarak çalıştırabilir</Leg>
          <Leg icon="key" color="var(--yellow)" name="sınırlı sudo">sadece listelenen komutları</Leg>
          <Leg icon="user" color="var(--fg4)" name="sudo yok">sadece giriş</Leg></Hint></div>
      <table className="grid">
        <thead><tr><th>Kim</th><th>Takımlar</th><th>Sudo</th></tr></thead>
        <tbody>
          {access.map((a) => (
            <tr key={a.via}>
              <td className="nowrap"><span className="mono">{a.via}</span>{a.privileged && <> <Badge tone="orange">ayrıcalıklı</Badge></>}</td>
              <td>{a.teams.join(", ")}</td>
              <td>
                {a.sudo ? <IL icon="key" color="var(--red)" size={14}>tam sudo</IL>
                  : a.sudoCommands?.length ? (
                    <span>
                      <IL icon="key" color="var(--yellow)" size={14}>sınırlı sudo</IL>
                      <ul style={{ margin: "4px 0 0", paddingLeft: 18 }}>{a.sudoCommands.map((c) => <li key={c} className="mono">{c}</li>)}</ul>
                    </span>
                  ) : <span className="muted">yok</span>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Commits({ commits }: { commits: Commit[] }) {
  if (commits.length === 0) return <div className="empty">Commit yok.</div>;
  return (
    <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
      {commits.map((x, i) => (
        <li key={x.repo + x.commit} className="row" style={{ gap: 12, padding: "14px 16px", borderTop: i ? "1px solid var(--bg1)" : undefined }}>
          <span className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={15} /><Mono>{short(x.commit)}</Mono></span>
          <span className="muted">{x.repo}</span>
          {x.signer ? <IL icon="key" color="var(--green)" size={14}>{x.signer}</IL> : <IL icon="x-circle" color="var(--red)" size={14}>imzasız</IL>}
          {x.reverts && <span className="muted">geri alıyor: <Mono>{short(x.reverts)}</Mono></span>}
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
  const items: Item[] = [{ at: c.createdAt, icon: "plan", color: "var(--yellow)", text: <>Change açıldı; plan <Mono>{short(c.planHash, 15)}…</Mono></> }];
  for (const a of c.approvals ?? []) {
    items.push(a.decision === "approve"
      ? { at: a.createdAt, icon: "check-circle", color: "var(--green)", text: <b>{a.username} onayladı</b> }
      : { at: a.createdAt, icon: "x-circle", color: "var(--red)", text: <><b>{a.username} reddetti</b>{a.comment && <span className="muted">: {a.comment}</span>}</> });
  }
  for (const n of notes.data ?? []) {
    items.push({ at: n.createdAt, icon: "bell", color: "var(--aqua)", text: <>Bildirim ({n.trigger}): {n.channel} → {n.recipient} <span className="faint">· {n.status}</span></> });
  }
  if (c.runId) items.push({ at: c.updatedAt, icon: "play", color: "var(--yellow)", text: <>Run: <Link to={`/runs/${encodeURIComponent(c.runId)}`}><Mono>{c.runId}</Mono></Link></> });
  if (c.finishedAt) items.push({ at: c.finishedAt, icon: "history", color: "var(--fg4)", text: <>Kapandı: <ChangeBadge status={c.status} /></> });
  items.sort((a, b) => a.at.localeCompare(b.at));
  return (
    <Card title="Akış" icon="history" hint="Bu Change'le ilgili olanlar, sırayla: planın hesaplanması, bildirimler, kararlar ve run.">
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
    await decide(send, async () => { throw new Error("Doğrulama süresi doldu; tekrar deneyin."); });
    await qc.invalidateQueries({ queryKey: ["change", c.id] });
    await qc.invalidateQueries({ queryKey: ["changes"] });
    onDone();
  };

  const run = async (e?: FormEvent) => {
    e?.preventDefault();
    if (!approve && !comment.trim()) {
      setError("Red için bir gerekçe yazın.");
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
    <Modal title={approve ? "Planı onayla" : "Planı reddet"} icon={approve ? "check-circle" : "x-circle"} tone={approve ? "yellow" : "red"} onClose={onDone} onSubmit={run} width={560}
      footer={<>
        <Button onClick={onDone} disabled={busy}>Vazgeç</Button>
        <Button type="submit" kind={approve ? "primary" : "solid-danger"} icon="key" disabled={busy}>
          {busy ? "Bekleniyor…" : totp !== null ? (approve ? "Kodla onayla" : "Kodla reddet") : approve ? "Passkey ile onayla" : "Passkey ile reddet"}
        </Button>
      </>}>
      <p style={{ margin: 0 }}>
        <b>{c.contract}</b> için bu plana karar veriyorsunuz: <Mono>{short(c.planHash, 19)}…</Mono>
      </p>
      <p className="muted" style={{ margin: 0 }}>Karar bu plana bağlıdır; plan değişirse onay düşer. Devam etmek için passkey'inizle doğrulayın.</p>
      <label className="field">
        <span>{approve ? "Not (isteğe bağlı)" : "Red gerekçesi"}</span>
        <textarea className="input" rows={3} value={comment} onChange={(e) => setComment(e.target.value)} placeholder={approve ? "" : "Commit sahibine ve kanala gider"} />
      </label>
      {totp !== null && (
        <label className="field">
          <span>Passkey'iniz yok; doğrulama uygulamanızdaki kodu girin</span>
          <input className="input mono" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} value={totp} onChange={(e) => setTotp(e.target.value)} autoFocus required />
        </label>
      )}
      {error && <div className="error" role="alert">{error}</div>}
    </Modal>
  );
}

function message(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 409) return `${err.message} — sayfayı yenileyip tekrar bakın.`;
    if (err.status === 403) return `Bu değişikliği onaylayamazsınız: ${err.message}`;
    if (err.status === 401) return "Kod ya da passkey kabul edilmedi.";
    return err.message;
  }
  if (err instanceof Error && err.name === "NotAllowedError") return "Passkey doğrulaması iptal edildi ya da zaman aşımına uğradı.";
  return err instanceof Error ? err.message : String(err);
}

