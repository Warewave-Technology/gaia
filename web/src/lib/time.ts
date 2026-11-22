// Every time on Gaia's screens is the server's local time (PO decision 2026-11-17), the zone named once in the bar.
export type ServerClock = { now: string; zone: string; offset_s: number }

export type Fmt = {
  clock(iso: string): string // 19:10:00
  hhmm(iso: string): string // 19:10
  date(iso: string): string // Nov 17
  /** The one way a moment is written on a screen (docs/design/v3 rule 4): `14:05` today, `yesterday 22:51`,
   * `tomorrow 03:00`, `Nov 19 10:43` otherwise. Never an ISO string. */
  at(iso: string): string
  /** Seconds from `iso` to the server's now (negative: in the future). */
  age(iso: string): number
  zone: string
}

function validZone(zone: string): boolean {
  try {
    new Intl.DateTimeFormat('en', { timeZone: zone })
    return true
  } catch {
    return false
  }
}

/** Formatters in the server's zone; when the browser does not know the zone's name, the server's offset is used. */
export function serverTime(c: ServerClock): Fmt {
  const tz = validZone(c.zone) ? c.zone : 'UTC'
  const shift = tz === c.zone ? 0 : c.offset_s * 1000
  // A time that is not there yet (a test still running has no finished_at) is a dash, never an exception.
  const safe = (fmt: Intl.DateTimeFormat) => (iso: string) => {
    const ms = Date.parse(iso)
    return Number.isFinite(ms) ? fmt.format(new Date(ms + shift)) : '—'
  }
  const f = (o: Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat('en-GB', { timeZone: tz, hourCycle: 'h23', ...o })
  const clock = f({ hour: '2-digit', minute: '2-digit', second: '2-digit' })
  const hhmm = f({ hour: '2-digit', minute: '2-digit' })
  const date = new Intl.DateTimeFormat('en-US', { timeZone: tz, month: 'short', day: 'numeric' })
  const day = f({ year: 'numeric', month: '2-digit', day: '2-digit' })
  const now = Date.parse(c.now)
  const dayOf = (ms: number) => day.format(new Date(ms + shift))
  const at = (iso: string) => {
    const ms = Date.parse(iso)
    if (!Number.isFinite(ms)) return '—'
    const d = dayOf(ms)
    const t = hhmm.format(new Date(ms + shift))
    if (!Number.isFinite(now) || d === dayOf(now)) return t
    if (d === dayOf(now - 86_400_000)) return `yesterday ${t}`
    if (d === dayOf(now + 86_400_000)) return `tomorrow ${t}`
    return `${date.format(new Date(ms + shift))} ${t}`
  }
  return {
    clock: safe(clock),
    hhmm: safe(hhmm),
    date: safe(date),
    at,
    age: (iso: string) => (now - Date.parse(iso)) / 1000,
    zone: c.zone,
  }
}

/** Seconds between two RFC 3339 times. */
export function secondsBetween(from: string, to: string): number {
  return (Date.parse(to) - Date.parse(from)) / 1000
}
