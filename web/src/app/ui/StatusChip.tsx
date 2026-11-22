import { useT } from '../../i18n'
import s from './StatusChip.module.css'

export type Status = 'OK' | 'WARNING' | 'CRITICAL' | 'UNKNOWN'
const GLYPH: Record<Status, string> = { OK: '✓', WARNING: '▲', CRITICAL: '◆', UNKNOWN: '?' }

export function asStatus(v: string): Status {
  return v === 'OK' || v === 'WARNING' || v === 'CRITICAL' ? v : 'UNKNOWN'
}

/** A status by shape, word and colour together (never colour alone). OK is quiet; UNKNOWN is hatched, never calm.
 * `word` names the status in the screen's terms (FAILED, READY, BEHIND) where the status word would not. */
export function StatusChip({ status, size = 'md', word }: { status: string; size?: 'xs' | 'sm' | 'md' | 'lg'; word?: string }) {
  const t = useT()
  const st = asStatus(status)
  return (
    <span className={`${s.chip} ${s[st.toLowerCase()]} ${s[size]}`} data-status={st}>
      <span className={s.g} aria-hidden="true">{GLYPH[st]}</span>
      {word ?? t.status[st]}
    </span>
  )
}
