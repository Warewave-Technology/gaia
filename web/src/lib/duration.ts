/** Compact durations for figures: 37s, 6m 50s, 12m, 3h 46m, 17h, 2d 4h. `precise` keeps seconds up to an hour. */
export function dur(seconds: number | null | undefined, precise = false): string {
  if (seconds === null || seconds === undefined || Number.isNaN(seconds)) return '—'
  let s = Math.round(seconds)
  if (s < 60) return `${s}s`
  let m = Math.floor(s / 60)
  s %= 60
  if (m < 60) return s && (precise || m < 10) ? `${m}m ${s}s` : `${m}m`
  let h = Math.floor(m / 60)
  m %= 60
  if (h < 24) return m ? `${h}h ${m}m` : `${h}h`
  const d = Math.floor(h / 24)
  h %= 24
  return h ? `${d}d ${h}h` : `${d}d`
}
