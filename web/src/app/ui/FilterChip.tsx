import type { ReactNode } from 'react'
import s from './FilterChip.module.css'

/** A filter above a table (docs/design/v3 rule 1): dashed when not set, solid when set. The screen keeps it in its URL. */
export function FilterChip({ set, onClick, children, title }: { set: boolean; onClick: () => void; children: ReactNode; title?: string }) {
  return (
    <button type="button" className={s.chip} data-set={set || undefined} aria-pressed={set} onClick={onClick} title={title}>{children}</button>
  )
}

/** The row of filters and the search above a table. */
export function Toolbar({ children, end }: { children: ReactNode; end?: ReactNode }) {
  return <div className={s.bar}>{children}{end && <span className={s.end}>{end}</span>}</div>
}
