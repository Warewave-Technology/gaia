// Times as the screens say them, in Turkish.

export function ago(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "";
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return `${s} sn önce`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} dk önce`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h} sa önce`;
  return `${Math.round(h / 24)} gün önce`;
}

export function duration(from: string | undefined, to: string | undefined, now = Date.now()): string {
  if (!from) return "";
  const s = Math.max(0, Math.round(((to ? new Date(to).getTime() : now) - new Date(from).getTime()) / 1000));
  if (s < 60) return `${s} sn`;
  const m = Math.floor(s / 60);
  if (m < 60) return s % 60 ? `${m} dk ${s % 60} sn` : `${m} dk`;
  const h = Math.floor(m / 60);
  return m % 60 ? `${h} sa ${m % 60} dk` : `${h} sa`;
}

export function clock(iso: string | undefined): string {
  if (!iso) return "";
  return new Date(iso).toLocaleTimeString("tr-TR", { hour: "2-digit", minute: "2-digit" });
}

export function stamp(iso: string | undefined): string {
  if (!iso) return "";
  return new Date(iso).toLocaleString("tr-TR", { dateStyle: "medium", timeStyle: "short" });
}
