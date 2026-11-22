import type { ReactNode } from 'react'
import { useT } from '../../i18n'
import s from './TechDetails.module.css'

/** Raw text (probe output, error strings, paths) under a disclosure, never the first thing seen (docs/design/v3 rule 3). */
export function TechDetails({ children, summary }: { children: ReactNode; summary?: string }) {
  const t = useT().table
  if (children === null || children === undefined || children === '') return null
  return (
    <details className={s.d}>
      <summary>{summary ?? t.technical}</summary>
      <pre className={s.raw}>{children}</pre>
    </details>
  )
}
