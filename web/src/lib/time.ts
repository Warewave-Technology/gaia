// Every time on Gaia's screens is the server's local time (PO decision 2026-11-17), the zone named once in the bar.
export type ServerClock = { now: string; zone: string; offset_s: number }

export type Fmt = {
  clock(iso: string): string // 19:10:00
  hhmm(iso: string): string // 19:10
  date(iso: string): string // Nov 17
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
  const at = (iso: string) => new Date(Date.parse(iso) + shift)
  const f = (o: Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat('en-GB', { timeZone: tz, hourCycle: 'h23', ...o })
  const clock = f({ hour: '2-digit', minute: '2-digit', second: '2-digit' })
  const hhmm = f({ hour: '2-digit', minute: '2-digit' })
  const date = new Intl.DateTimeFormat('en-US', { timeZone: tz, month: 'short', day: 'numeric' })
  return {
    clock: (iso) => clock.format(at(iso)),
    hhmm: (iso) => hhmm.format(at(iso)),
    date: (iso) => date.format(at(iso)),
    zone: c.zone,
  }
}

/** Seconds between two RFC 3339 times. */
export function secondsBetween(from: string, to: string): number {
  return (Date.parse(to) - Date.parse(from)) / 1000
}
