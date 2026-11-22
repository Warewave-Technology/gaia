import { useRef, type KeyboardEvent, type ReactNode } from 'react'
import s from './Tabs.module.css'

export type Tab = { key: string; label: string; count?: ReactNode }

/**
 * One screen, one question (docs/design/v3 rule 2): what does not answer it goes behind a tab. The screen keeps the
 * chosen tab in its URL (useFilters), so a link opens the same tab. Arrow keys move between tabs.
 */
export function Tabs({ tabs, value, onChange, label, hint }: { tabs: readonly Tab[]; value: string; onChange: (key: string) => void; label: string; hint?: ReactNode }) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const key = (e: KeyboardEvent, i: number) => {
    const d = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0
    if (!d) return
    const j = (i + d + tabs.length) % tabs.length
    onChange(tabs[j].key)
    refs.current[j]?.focus()
  }
  return (
    <div className={s.tabs} role="tablist" aria-label={label}>
      {tabs.map((t, i) => (
        <button key={t.key} ref={(el) => { refs.current[i] = el }} type="button" role="tab" id={`tab-${t.key}`} aria-selected={t.key === value}
          aria-controls={`panel-${t.key}`} tabIndex={t.key === value ? 0 : -1} className={s.tab} onClick={() => onChange(t.key)} onKeyDown={(e) => key(e, i)}>
          {t.label}
          {t.count !== undefined && t.count !== '' && <span className={s.count}>{t.count}</span>}
        </button>
      ))}
      {hint && <span className={s.hint}>{hint}</span>}
    </div>
  )
}

/** The content of the chosen tab. */
export function TabPanel({ id, children }: { id: string; children: ReactNode }) {
  return <div role="tabpanel" id={`panel-${id}`} aria-labelledby={`tab-${id}`} className={s.panel}>{children}</div>
}
