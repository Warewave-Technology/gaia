import type { ReactNode } from 'react'
import s from './Notice.module.css'

/** A line that needs attention: warning or critical tint, a glyph, the words, maybe an action. */
export function Notice({ tone, children, action }: { tone: 'warning' | 'critical'; children: ReactNode; action?: ReactNode }) {
  return (
    <p className={`${s.notice} ${s[tone]}`} role={tone === 'critical' ? 'alert' : 'status'}>
      <span className={s.g} aria-hidden="true">{tone === 'critical' ? '◆' : '▲'}</span>
      <span className={s.text}>{children}</span>
      {action && <span className={s.action}>{action}</span>}
    </p>
  )
}
