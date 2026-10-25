// A settings change (design 17.3): what it does to each object, which
// contracts' plans it changes, and the decision.
import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { api } from "../../api/client";
import type { Proposal, ProposalStatus } from "../../api/proposals";
import { useSession } from "../../api/session";
import { lineDiff } from "../../lib/linediff";
import { ago, stamp } from "../../lib/time";
import { Icon, type IconName } from "../../ui/icons";
import { Badge, Button, Card, ErrorNote, Hint, Leg, Loading, StatusBar, type Tone } from "../../ui/parts";
import { Modal } from "../../ui/modal";
import { Page } from "../../ui/shell";
import { useStrong } from "../../ui/strong";
import { kindOf, objectPath } from "../../ui/kinds";
import { Author } from "./Versions";

export const PROPOSAL: Record<ProposalStatus, { tone: Tone; icon: IconName; label: string; text: string }> = {
  pending: { tone: "blue", icon: "clock", label: "Onay bekliyor", text: "bir onaycı karar verecek" },
  applied: { tone: "green", icon: "check", label: "Uygulandı", text: "yeni bir ayar sürümü oldu" },
  rejected: { tone: "red", icon: "x", label: "Reddedildi", text: "bir onaycı gerekçesiyle reddetti" },
  stale: { tone: "gray", icon: "history", label: "Eskidi", text: "aynı nesneler başka bir değişiklikle değişti; yeniden önerilmeli" },
  withdrawn: { tone: "gray", icon: "undo", label: "Geri çekildi", text: "öneren geri çekti" },
};

export function ProposalBadge({ status }: { status: ProposalStatus }) {
  const l = PROPOSAL[status] ?? { tone: "gray" as Tone, icon: "help" as IconName, label: status };
  return <Badge tone={l.tone} icon={l.icon}>{l.label}</Badge>;
}

export function ProposalLegend() {
  return <>{Object.entries(PROPOSAL).map(([k, l]) => <Leg key={k} icon={l.icon} color={`var(--${l.tone === "gray" ? "fg4" : l.tone})`} name={l.label}>{l.text}</Leg>)}</>;
}

const OP: Record<string, { tone: Tone; label: string }> = {
  add: { tone: "green", label: "eklenir" },
  change: { tone: "yellow", label: "değişir" },
  remove: { tone: "red", label: "kaldırılır" },
};

function YamlDiff({ before, after }: { before: string; after: string }) {
  const lines = lineDiff(before, after);
  return (
    <pre className="mono diff" style={{ margin: 0, padding: "12px 0", overflowX: "auto", lineHeight: 1.6 }}>
      {lines.map((l, i) => (
        <div key={i} className={`diff-line ${l.op}`}><span className="diff-sign">{l.op === "add" ? "+" : l.op === "del" ? "−" : " "}</span>{l.text}</div>
      ))}
    </pre>
  );
}

function Decide({ p, approve, onClose }: { p: Proposal; approve: boolean; onClose: () => void }) {
  const [comment, setComment] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const strong = useStrong();
  const qc = useQueryClient();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await strong.run(() => api(`/settings/proposals/${encodeURIComponent(p.id)}/${approve ? "approve" : "reject"}`, { method: "POST", body: { contentHash: p.contentHash, comment } }));
      void qc.invalidateQueries({ queryKey: ["proposals"] });
      void qc.invalidateQueries({ queryKey: ["settings"] });
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Modal title={approve ? "Ayar değişikliğini onayla" : "Ayar değişikliğini reddet"} icon={approve ? "check" : "x"} tone={approve ? undefined : "red"} onClose={onClose} onSubmit={submit}
        footer={<><Button onClick={onClose} disabled={busy}>Vazgeç</Button><Button type="submit" kind={approve ? "primary" : "solid-danger"} icon={approve ? "check" : "x"} disabled={busy}>{busy ? "Bekleniyor…" : approve ? "Onayla" : "Reddet"}</Button></>}>
        <p className="muted" style={{ margin: 0 }}>
          {approve
            ? <>Onay bu sayfada gördüğünüz içeriğe bağlıdır. Onaylanınca yeni bir ayar sürümü olur{p.impact.length > 0 && <>; planı değişen {p.impact.length} kontrat için olağan Change'ler açılır</>}.</>
            : "Red gerekçe ister; öneren gerekçeyi görür."}
        </p>
        <label className="field"><span>{approve ? "Not (isteğe bağlı)" : "Gerekçe"}</span><input className="input" value={comment} onChange={(e) => setComment(e.target.value)} required={!approve} /></label>
        {error && <div className="error" role="alert">{error}</div>}
      </Modal>
      {strong.prompt}
    </>
  );
}

export function ProposalPage() {
  const { id = "" } = useParams();
  const { me } = useSession();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["proposals", id], queryFn: () => api<Proposal>(`/settings/proposals/${encodeURIComponent(id)}`) });
  const [deciding, setDeciding] = useState<"approve" | "reject" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const withdraw = async () => {
    setError(null);
    try {
      await api(`/settings/proposals/${encodeURIComponent(id)}/withdraw`, { method: "POST" });
      void qc.invalidateQueries({ queryKey: ["proposals"] });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  const p = q.data;
  return (
    <Page crumbs={<><Link to="/changes">Değişiklikler</Link> / Ayar değişikliği / {id}</>}>
      {q.isPending ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : p && (
        <>
          <div className="spread">
            <div className="title-row">
              <span style={{ color: "var(--blue)" }}><Icon name="gear" size={22} /></span>
              <h1>{p.title}</h1>
              <ProposalBadge status={p.status} />
              {p.critical && <Badge tone="orange" icon="shield">platform-admin onayı</Badge>}
              <Hint width={340}>Ayar değişikliği: Gaia'nın ayarlarına (Git'te olmayan nesnelere) önerilen bir değişiklik. Öneren kendisi onaylayamaz; onaylanınca yeni bir ayar sürümü olur.<ProposalLegend /></Hint>
            </div>
            {p.status === "pending" && (
              <span className="row">
                {p.mayDecide && <Button kind="primary" icon="check" onClick={() => setDeciding("approve")}>Onayla</Button>}
                {p.mayDecide && <Button kind="danger" icon="x" onClick={() => setDeciding("reject")}>Reddet</Button>}
                {me?.username === p.requestedBy && <Button icon="undo" onClick={withdraw}>Geri çek</Button>}
              </span>
            )}
          </div>
          {error && <div className="notice err" role="alert">{error}</div>}
          {p.status === "pending" && !p.mayDecide && p.why && <div className="notice">{p.why}</div>}
          {p.status === "stale" && <div className="notice warn">Bu değişiklik eskidi: {p.staleReason}. Güncel ayarlar üzerinden yeniden önerilmeli.</div>}
          {p.critical && p.whyCritical && <div className="notice warn"><b>Sadece platform-admin onaylar:</b> {p.whyCritical.join("; ")}</div>}
          <StatusBar items={[
            { icon: "user", title: "İsteyen", value: <span style={{ fontSize: 15 }}><Author who={p.requestedBy} /></span>, sub: `${ago(p.createdAt)} · ${stamp(p.createdAt)}` },
            {
              icon: "history", title: "Sürüm", hint: "Önerinin yapıldığı ayar sürümü; uygulandıysa oluşan sürüm.",
              value: p.appliedVersion ? <Link to={`/settings/versions/${p.appliedVersion}`} style={{ fontSize: 15 }}>v{p.baseVersion} → v{p.appliedVersion}</Link> : <span style={{ fontSize: 15 }}>v{p.baseVersion} üzerinde</span>,
            },
            { icon: "file", title: "Etki", hint: "Planı değişecek kontratlar. Uygulanınca her biri için olağan bir Change açılır ve kendi onayından geçer.", value: p.impact.length, sub: p.impact.length ? "kontrat" : "hiçbir kontrat değişmiyor" },
            {
              icon: "check-circle", title: "Karar", value: <span style={{ fontSize: 15 }}>{p.decidedBy ?? "–"}</span>,
              sub: p.decidedAt ? `${ago(p.decidedAt)}${p.comment ? ` · ${p.comment}` : ""}` : p.reason ? `neden: ${p.reason}` : "",
            },
          ]} />
          <div className="row" style={{ alignItems: "flex-start", gap: 16 }}>
            <div style={{ flex: "2 1 520px", minWidth: 0, display: "flex", flexDirection: "column", gap: 16 }}>
              {(p.objects ?? []).map((o) => (
                <section key={o.path} className="card" aria-label={`${o.kind}/${o.name}`}>
                  <div className="card-head">
                    <div className="card-title">
                      <Icon name={kindOf(o.kind).icon} size={16} />
                      <h2>{o.op === "remove" ? <>{o.kind}/{o.name}</> : <Link to={objectPath(o.kind, o.name)}><span className="faint" style={{ fontWeight: 400 }}>{o.kind}/</span>{o.name}</Link>}</h2>
                    </div>
                    <Badge tone={OP[o.op]?.tone ?? "gray"}>{OP[o.op]?.label ?? o.op}</Badge>
                  </div>
                  <YamlDiff before={o.before ?? ""} after={o.after ?? ""} />
                </section>
              ))}
            </div>
            <div style={{ flex: "1 1 280px", display: "flex", flexDirection: "column", gap: 16 }}>
              <Card title="Planı değişecek kontratlar" icon="file">
                {p.impact.length === 0 ? <div className="empty">Hiçbir kontratın planı değişmiyor.</div> : (
                  <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
                    {p.impact.map((i, n) => (
                      <li key={i.contract} className="row" style={{ gap: 10, padding: "10px 16px", borderTop: n ? "1px solid var(--bg1)" : undefined }}>
                        <span className="faint"><Icon name="file" size={15} /></span>
                        <Link to={objectPath("Server", i.contract)}>{i.contract}</Link>
                      </li>
                    ))}
                  </ul>
                )}
              </Card>
            </div>
          </div>
          {deciding && <Decide p={p} approve={deciding === "approve"} onClose={() => setDeciding(null)} />}
        </>
      )}
    </Page>
  );
}
