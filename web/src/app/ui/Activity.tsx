import { useT } from '../../i18n'
import type { Fmt } from '../../lib/time'
import type { components } from '../../api/schema'
import s from './Activity.module.css'

type Job = components['schemas']['Job']

/** What the server does for a VM now: a spinner with the running job in words, then what waits in the queue. Nothing
 *  when it is idle. The words are always there; the spinner only says "now" (and stands still with reduced motion). */
export function Activity({ jobs, fmt, wide = false }: { jobs: Job[]; fmt: Fmt; wide?: boolean }) {
  const t = useT().activity
  if (jobs.length === 0) return null
  const running = jobs.filter((j) => j.state === 'running')
  const queued = jobs.filter((j) => j.state !== 'running')
  const line = (j: Job) => (j.state === 'running' ? t.running(t.kind(j.kind), j.id, fmt.hhmm(j.started_at)) : t.queued(t.kind(j.kind), j.id, fmt.hhmm(j.created_at)))
  const all = jobs.map(line).join('; ')
  return (
    <span className={`${s.act} ${wide ? s.wide : ''}`} title={all} role="status">
      {running.length > 0 ? <span className={s.spin} aria-hidden="true" /> : <span className={s.wait} aria-hidden="true" />}
      <span className={s.txt}>
        {wide ? all : running.length > 0 ? line(running[0]) + (jobs.length > 1 ? t.more(jobs.length - 1) : '') : line(queued[0]) + (queued.length > 1 ? t.more(queued.length - 1) : '')}
      </span>
    </span>
  )
}
