/**
 * The one way Gaia writes a duration (docs/design/v3 rule 4, ADR-007 C15): `52 s` under 2 min, `21 min`, `4 h 22 min`,
 * `4 d 10 h`. Never Go's `4h0m0s`, never a count of seconds above 2 min. `precise` is for a measured restore or a
 * test step under 10 min, where the seconds matter against a target: `4 min 54 s` (as the v3 VM page shows it).
 */
export function dur(seconds: number | null | undefined, precise = false): string {
  if (seconds === null || seconds === undefined || Number.isNaN(seconds)) return '—'
  let s = Math.max(0, Math.round(seconds))
  if (s < 120) return `${s} s`
  let m = Math.floor(s / 60)
  s %= 60
  if (m < 60) return precise && m < 10 && s ? `${m} min ${s} s` : `${m} min`
  let h = Math.floor(m / 60)
  m %= 60
  if (h < 24) return m ? `${h} h ${m} min` : `${h} h`
  const d = Math.floor(h / 24)
  h %= 24
  return h ? `${d} d ${h} h` : `${d} d`
}

/** A duration that has passed: "32 min ago"; under a minute, "just now". */
export function ago(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || Number.isNaN(seconds)) return '—'
  return seconds < 60 ? 'just now' : `${dur(seconds)} ago`
}
