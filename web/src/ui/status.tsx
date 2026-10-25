// What each state looks like, and the "?" legends that explain them.
import type { ReactNode } from "react";
import type { ChangeStatus, RunStatus } from "../api/types";
import { Shield, type IconName } from "./icons";
import { Badge, IL, Leg, Term, type Tone } from "./parts";

type Look = { tone: Tone; icon: IconName; label: string };

export const CHANGE: Record<ChangeStatus, Look> = {
  pending: { tone: "blue", icon: "clock", label: "Onay bekliyor" },
  approved: { tone: "green", icon: "check", label: "Onaylandı · kuyrukta" },
  running: { tone: "yellow", icon: "play", label: "Uygulanıyor" },
  applied: { tone: "green", icon: "check", label: "Uygulandı" },
  failed: { tone: "red", icon: "x", label: "Başarısız" },
  rejected: { tone: "red", icon: "x", label: "Reddedildi" },
  cancelled: { tone: "gray", icon: "stop", label: "İptal edildi" },
  superseded: { tone: "gray", icon: "arrow", label: "Yerini yenisi aldı" },
  expired: { tone: "gray", icon: "clock", label: "Süresi doldu" },
};

export const RUN: Record<RunStatus, Look> = {
  queued: { tone: "gray", icon: "clock", label: "Kuyrukta" },
  running: { tone: "yellow", icon: "play", label: "Çalışıyor" },
  succeeded: { tone: "green", icon: "check", label: "Uygulandı" },
  failed: { tone: "red", icon: "x", label: "Başarısız" },
  cancelled: { tone: "gray", icon: "stop", label: "İptal edildi" },
};

export function ChangeBadge({ status }: { status: ChangeStatus }) {
  const l = CHANGE[status] ?? { tone: "gray", icon: "help", label: status };
  return <Badge tone={l.tone} icon={l.icon}>{l.label}</Badge>;
}

export function RunBadge({ status }: { status: RunStatus }) {
  const l = RUN[status] ?? { tone: "gray", icon: "help", label: status };
  return <Badge tone={l.tone} icon={l.icon}>{l.label}</Badge>;
}

export const SOURCE: Record<string, { icon: IconName; label: string; text: string }> = {
  commit: { icon: "commit", label: "commit", text: "kontrat reposuna yeni bir commit geldi" },
  retry: { icon: "rerun", label: "retry", text: "başarısız bir değişiklik tekrar denendi" },
  revert: { icon: "undo", label: "revert", text: "önceki bir sürüme geri dönüldü" },
  drift: { icon: "scan", label: "drift", text: "elle yapılan bir değişiklik düzeltiliyor" },
  "drift-auto": { icon: "scan", label: "drift (otomatik)", text: "enforce seviyesinde sapma kendiliğinden düzeltiliyor" },
  operation: { icon: "zap", label: "operation", text: "bir Operation (ör. yeniden başlatma, yama) çalışıyor" },
  schedule: { icon: "calendar", label: "schedule", text: "zamanlanmış bir Operation onaylanıyor" },
  settings: { icon: "gear", label: "ayar", text: "bir ayar değişikliği (site, ağ, takım…) kontratın planını değiştirdi" },
};

export function SourceLabel({ source, children }: { source: string; children?: ReactNode }) {
  const s = SOURCE[source] ?? { icon: "help" as IconName, label: source };
  return <IL icon={s.icon} color="var(--fg4)" size={15} weight={400}>{children ?? s.label}</IL>;
}

export function SourceLegend() {
  return (
    <>
      Değişikliği ya da run'ı neyin başlattığı.
      {Object.entries(SOURCE).map(([k, s]) => (
        <Leg key={k} icon={s.icon} color={k.startsWith("drift") ? "var(--orange)" : "var(--fg2)"} name={s.label}>{s.text}</Leg>
      ))}
    </>
  );
}

export function ChangeStatusLegend() {
  return (
    <>
      Değişikliğin durumu.
      <Leg icon="clock" color="var(--blue)" name="Onay bekliyor">gereken onay verilmedi</Leg>
      <Leg icon="check" color="var(--green)" name="Onaylandı / Uygulandı">onaylar tamam; run kuyrukta ya da bitti</Leg>
      <Leg icon="play" color="var(--yellow)" name="Uygulanıyor">run çalışıyor</Leg>
      <Leg icon="x" color="var(--red)" name="Başarısız / Reddedildi">run hata verdi ya da bir onaycı reddetti</Leg>
      <Leg icon="arrow" color="var(--fg4)" name="Yerini yenisi aldı">aynı kontrata daha yeni bir commit geldi</Leg>
    </>
  );
}

export const SYNC = {
  synced: { icon: "check-circle" as IconName, color: "var(--green)", label: "Synced" },
  "out-of-sync": { icon: "arrow-up-circle" as IconName, color: "var(--yellow)", label: "OutOfSync" },
  drifted: { icon: "scan" as IconName, color: "var(--orange)", label: "Drifted" },
  unknown: { icon: "help" as IconName, color: "var(--gray)", label: "Bilinmiyor" },
};

export type SyncState = keyof typeof SYNC;

export function Sync({ state, children, size = 15 }: { state: SyncState; children?: ReactNode; size?: number }) {
  const s = SYNC[state];
  return <IL icon={s.icon} color={s.color} size={size}>{children ?? s.label}</IL>;
}

export function SyncLegend() {
  return (
    <>
      Git'teki istenen durumla uygulanmış durumun karşılaştırması.
      <Leg icon="check-circle" color="var(--green)" name="Synced">Git'teki hâli uygulanmış</Leg>
      <Leg icon="arrow-up-circle" color="var(--yellow)" name="OutOfSync">Git'te henüz uygulanmamış bir commit var</Leg>
      <Leg icon="scan" color="var(--orange)" name="Drifted">Git aynı, ama gerçek kaynak elle değiştirilmiş</Leg>
      <Leg icon="help" color="var(--gray)" name="Bilinmiyor">Gaia kontratı henüz değerlendiremedi</Leg>
    </>
  );
}

export const PLAN_ACTION: Record<string, { icon: IconName; color: string; label: string }> = {
  create: { icon: "plus", color: "var(--green)", label: "eklenecek" },
  adopt: { icon: "plus", color: "var(--green)", label: "içeri alınacak" },
  replace: { icon: "pencil", color: "var(--yellow)", label: "değiştirilecek" },
  keep: { icon: "same", color: "var(--gray)", label: "aynı kalacak" },
  delete: { icon: "x", color: "var(--red)", label: "silinecek" },
};

export function PlanLegend() {
  return (
    <>
      <Leg icon="plus" color="var(--green)" name="eklenecek" />
      <Leg icon="pencil" color="var(--yellow)" name="değişecek" />
      <Leg icon="same" color="var(--gray)" name="aynı kalacak">dokunulmayacak</Leg>
      <Leg icon="x" color="var(--red)" name="silinecek" />
    </>
  );
}

export function RecordLegend() {
  return (
    <>
      Run'ın nasıl bittiği.
      <span className="leg"><Badge tone="green" icon="check">Uygulandı</Badge><span>bütün aşamalar geçti</span></span>
      <span className="leg"><Badge tone="red" icon="x">Başarısız</Badge><span>bir aşama hata verdi; sonrakiler çalışmadı</span></span>
      <span className="leg"><Shield /><span>run kaydı: imzalı, değiştirilemez, bir öncekine zincirli. Üstüne gelince ayrıntıları açılır.</span></span>
      <span className="leg"><span /><span><Term>Not:</Term> kalkan sadece kaydı yazılmış run'larda görünür.</span></span>
    </>
  );
}

export const HEALTH = {
  green: { icon: "heart" as IconName, color: "var(--green)", label: "Sağlıklı", fill: "var(--green)" },
  yellow: { icon: "alert" as IconName, color: "var(--yellow)", label: "Uyarı", fill: "none" },
  red: { icon: "heart-crack" as IconName, color: "var(--red)", label: "Bozuk", fill: "none" },
};

export function HealthLabel({ h, children, size = 15 }: { h: keyof typeof HEALTH; children?: ReactNode; size?: number }) {
  const x = HEALTH[h];
  return <IL icon={x.icon} color={x.color} size={size} fill={x.fill}>{children ?? x.label}</IL>;
}

export function HealthLegend() {
  return (
    <>
      Gaia'nın bildiklerinden çıkarılır: son run başarılı mı, sunucular active mi, açık drift var mı.
      <Leg icon="heart" color="var(--green)" name="Sağlıklı" fill="var(--green)">son run başarılı, sunucular active, drift yok</Leg>
      <Leg icon="alert" color="var(--yellow)" name="Uyarı">açık drift var ya da bir sunucu active değil</Leg>
      <Leg icon="heart-crack" color="var(--red)" name="Bozuk">son run başarısız</Leg>
    </>
  );
}
