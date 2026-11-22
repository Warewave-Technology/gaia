const UNIT: Record<string, number> = { s: 1, m: 60, min: 60, h: 3600, d: 86400, w: 7 * 86400 }

/**
 * Seconds from a duration as a person types it, and as lib/duration's dur() writes it: "30m", "4h", "1d 12h",
 * "6m 50s", "2 h". Null when it is not one (a bare number has no unit: is it minutes or hours?).
 */
export function parseDur(text: string): number | null {
  const v = text.trim().toLowerCase()
  if (!v) return null
  let total = 0
  let rest = v
  const part = /^(\d+)\s*(min|s|m|h|d|w)\s*/
  while (rest) {
    const m = part.exec(rest)
    if (!m) return null
    total += Number(m[1]) * UNIT[m[2]]
    rest = rest.slice(m[0].length)
  }
  return total > 0 ? total : null
}
