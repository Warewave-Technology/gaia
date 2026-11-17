// The shared scale of the proof bars: logarithmic, 30 s to 7 days, so a 5-minute and a daily policy sit on one axis.
export const LO = 30
export const HI = 7 * 86400
export const SCALE_TICKS: ReadonlyArray<readonly [number, string]> = [
  [60, '1m'], [600, '10m'], [3600, '1h'], [6 * 3600, '6h'], [86400, '1d'], [7 * 86400, '7d'],
]

/** Position of `v` seconds on a bar `w` wide. */
export function xlog(v: number, w: number): number {
  const c = Math.min(Math.max(v, LO), HI)
  return (w * (Math.log(c) - Math.log(LO))) / (Math.log(HI) - Math.log(LO))
}

/** Which band a value is in against its target: within, up to twice, beyond. */
export function band(value: number, target: number): 'ok' | 'warn' | 'crit' {
  if (value <= target) return 'ok'
  return value <= 2 * target ? 'warn' : 'crit'
}
