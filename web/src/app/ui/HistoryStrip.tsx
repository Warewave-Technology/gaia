import type { TimelineEvent } from '../api/views'
import { useT } from '../../i18n'
import s from './HistoryStrip.module.css'

const ORDER = ['backup', 'backup_failed', 'damaged', 'return', 'test_ok', 'test_warning', 'test_unknown', 'test_failed']

/** The last hours of backups and restore tests on a shared time axis, now at the right edge. `returnAt` circles the
 * point the VM can return to. */
export function HistoryStrip({ events, now, windowS, returnAt, width = 250, height = 34, big = false }: {
  events: TimelineEvent[]
  now: string
  windowS: number
  returnAt?: string | null
  width?: number
  height?: number
  big?: boolean
}) {
  const t = useT()
  const end = Date.parse(now)
  const start = end - windowS * 1000
  const X = (iso: string) => (width * (Date.parse(iso) - start)) / (windowS * 1000)
  const base = height - 9
  const r = big ? 1.25 : 1
  const shown = [...events, ...(returnAt ? [{ at: returnAt, kind: 'return', id: 0 }] : [])]
    .filter((e) => {
      const at = Date.parse(e.at)
      return at >= start && at <= end
    })
    .sort((a, b) => ORDER.indexOf(a.kind) - ORDER.indexOf(b.kind))
  const hours = Math.round(windowS / 3600)
  return (
    <svg className={`${s.hs} ${big ? s.big : ''}`} viewBox={`0 0 ${width} ${height}`} width={width} height={height} role="img"
      aria-label={t.strip.label(hours)}>
      {Array.from({ length: hours - 1 }, (_, k) => {
        const x = (width * (k + 1)) / hours
        return <line key={k} className={s.hr} x1={x} y1={2} x2={x} y2={base + 4} />
      })}
      <line className={s.base} x1={0} y1={base} x2={width} y2={base} />
      {shown.length === 0 && (
        <text className={s.empty} x={width / 2} y={base - 8} textAnchor="middle">{t.strip.empty}</text>
      )}
      {shown.map((e, i) => {
        const x = X(e.at)
        switch (e.kind) {
          case 'backup':
            return <line key={i} className={s.bk} x1={x} y1={base - 6 * r} x2={x} y2={base + 3} />
          case 'damaged':
            return <rect key={i} className={s.dmg} x={x - 3.5} y={base - 3.5} width={7} height={7} />
          case 'backup_failed':
            return <path key={i} className={s.xf} d={`M${x - 3} ${base - 9} l6 6 m0 -6 l-6 6`} />
          case 'test_ok':
            return <circle key={i} className={s.tok} cx={x} cy={base - 14 * r} r={2.8 * r} />
          case 'test_warning':
          case 'test_failed': {
            const z = 3.2 * r
            return <path key={i} className={e.kind === 'test_failed' ? s.tf : s.tw} d={`M${x - z} ${base - 14 * r - z} l${2 * z} ${2 * z} m0 ${-2 * z} l${-2 * z} ${2 * z}`} />
          }
          case 'return':
            return <circle key={i} className={s.ret} cx={x} cy={base} r={5.5 * r} />
          default:
            return <circle key={i} className={s.tun} cx={x} cy={base - 14 * r} r={3 * r} />
        }
      })}
      <line className={s.now} x1={width - 0.75} y1={1} x2={width - 0.75} y2={base + 4} />
    </svg>
  )
}
