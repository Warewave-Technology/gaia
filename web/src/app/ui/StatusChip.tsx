import { useT } from '../../i18n'
import s from './StatusChip.module.css'

export type Status = 'OK' | 'WARNING' | 'CRITICAL' | 'UNKNOWN'
const GLYPH: Record<Status, string> = { OK: '✓', WARNING: '▲', CRITICAL: '◆', UNKNOWN: '?' }

export function asStatus(v: string): Status {
  return v === 'OK' || v === 'WARNING' || v === 'CRITICAL' ? v : 'UNKNOWN'
}

/** A status by shape, word and colour together (never colour alone). OK is quiet; UNKNOWN is hatched, never calm. */
export function StatusChip({ status, size = 'md' }: { status: string; size?: 'xs' | 'sm' | 'md' | 'lg' }) {
  const t = useT()
  const st = asStatus(status)
  return (
    <span className={`${s.chip} ${s[st.toLowerCase()]} ${s[size]}`}>
      <span className={s.g} aria-hidden="true">{GLYPH[st]}</span>
      {t.status[st]}
    </span>
  )
}
