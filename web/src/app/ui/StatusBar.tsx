import { useT } from '../../i18n'
import s from './StatusBar.module.css'

const ORDER = ['CRITICAL', 'UNKNOWN', 'WARNING', 'OK'] as const

/**
 * Every VM of a group by status, as one bar, worst at the left (the v2 coverage table). The words are in its label
 * and beside it: the bar is never the only place a status is said.
 */
export function StatusBar({ counts, min = 4 }: { counts: Record<string, number>; min?: number }) {
  const t = useT()
  const parts = ORDER.map((st) => [st, counts[st] ?? 0] as const)
  const label = parts.map(([st, n]) => `${n} ${t.status[st]}`).join(', ')
  return (
    <span className={s.bar} role="img" aria-label={label}>
      {parts.map(([st, n]) => (n > 0 ? <span key={st} className={s[st.toLowerCase()]} style={{ flex: `${n} 1 0`, minWidth: min }} /> : null))}
    </span>
  )
}

/** The bar's counts in words, worst first, the zeros left out: "3 CRITICAL · 1 WARNING". */
export function StatusCounts({ counts }: { counts: Record<string, number> }) {
  const t = useT()
  return (
    <span className={s.words}>
      {ORDER.filter((st) => (counts[st] ?? 0) > 0).map((st) => (
        <span key={st} className={s[`w${st}`]}>{counts[st]} {t.status[st]}</span>
      ))}
    </span>
  )
}
