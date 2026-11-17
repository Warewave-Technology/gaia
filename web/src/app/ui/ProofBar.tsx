import { band, SCALE_TICKS, xlog } from '../../lib/scale'
import { dur } from '../../lib/duration'
import s from './ProofBar.module.css'

/** Few's bullet graph on the shared log scale: bands within the target, up to twice, beyond (darker is worse); the
 * bar is the measure; the notch the newest backup (Theoretical RPO); the tick the target. */
export function ProofBar({ value, newest, target, width = 220, label }: {
  value: number | null
  newest?: number | null
  target: number
  width?: number
  label: string
}) {
  const w = width
  const tx = xlog(target, w)
  const t2 = xlog(2 * target, w)
  const y0 = 6
  const bh = 14
  const aria = `${label}: ${value === null ? 'nothing proven' : dur(value)}, target ${dur(target)}`
  return (
    <svg className={s.pb} viewBox={`0 0 ${w} 26`} width={w} height={26} role="img" aria-label={aria}>
      <rect className={s.b0} x={0} y={y0} width={tx} height={bh} />
      <rect className={s.b1} x={tx} y={y0} width={t2 - tx} height={bh} />
      <rect className={s.b2} x={t2} y={y0} width={Math.max(w - t2, 0)} height={bh} />
      {value === null ? (
        <rect className={s.none} x={0.5} y={y0 + 0.5} width={w - 1} height={bh - 1} />
      ) : (
        <rect className={`${s.bar} ${s[band(value, target)]}`} x={0} y={y0 + 4.5} width={Math.max(xlog(value, w), 2)} height={5} />
      )}
      {newest !== null && newest !== undefined && (
        <path className={s.notch} d={`M${xlog(newest, w) - 4} 0.5 L${xlog(newest, w) + 4} 0.5 L${xlog(newest, w)} 6 Z`} />
      )}
      <rect className={s.tgt} x={tx - 1} y={y0 - 3} width={2} height={bh + 6} />
    </svg>
  )
}

/** The scale's labels, for a column header above the bars. */
export function ProofScale({ width = 220 }: { width?: number }) {
  return (
    <span className={s.axis} style={{ width }} aria-hidden="true">
      {SCALE_TICKS.map(([v, l]) => (
        <span key={l} style={{ left: xlog(v, width) }}>{l}</span>
      ))}
    </span>
  )
}
