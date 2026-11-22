import { useT } from '../../i18n'
import s from './JobState.module.css'

export type JobWord = 'RUNNING' | 'QUEUED' | 'DONE' | 'DONE_WARNING' | 'FAILED' | 'CANCELLED'
const GLYPH: Record<JobWord, string> = { RUNNING: '●', QUEUED: '', DONE: '✓', DONE_WARNING: '▲', FAILED: '◆', CANCELLED: '' }

/** A job's state in C16's words from the API's state (queued, running, ok, failed, cancelled) and, for a finished
 *  test, its verdict: a test that found a fault is FAILED, one that warned is DONE, WARNING. */
export function jobWord(state: string, verdict?: string | null): JobWord {
  switch (state) {
    case 'queued': return 'QUEUED'
    case 'running': return 'RUNNING'
    case 'cancelled': return 'CANCELLED'
    case 'failed': return 'FAILED'
  }
  if (verdict === 'CRITICAL') return 'FAILED'
  if (verdict === 'WARNING') return 'DONE_WARNING'
  return 'DONE'
}

/**
 * A job's state (ADR-007 C16): never a VM status mark. RUNNING is blue and pulses (still with reduced motion), QUEUED
 * and CANCELLED are neutral, a finished job says DONE, DONE, WARNING or FAILED. The word is always there;
 * `data-job-state` lets the T27 gate check it (lab/scale/ui-gate.mjs).
 */
export function JobState({ state, verdict, detail }: { state: string; verdict?: string | null; detail?: string }) {
  const t = useT().job
  const w = jobWord(state, verdict)
  return (
    <span className={`${s.job} ${s[w.toLowerCase()]}`} data-job-state={w}>
      {GLYPH[w] && <span className={s.g} aria-hidden="true">{GLYPH[w]}</span>}
      {t[w]}
      {detail && <span className={s.detail}>{detail}</span>}
    </span>
  )
}
