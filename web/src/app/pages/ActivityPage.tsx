import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { secondsBetween, serverTime, type Fmt, type ServerClock } from '../../lib/time'
import { useFilters } from '../../lib/url'
import { useActivity, useHosts, type ActivityRow, type ActivityView, type JobCount } from '../api/capacity'
import { Failed, Loading } from '../layout/States'
import { MoreRows, PageHead, Pick, Seg, StatusChip } from '../ui'
import s from './ActivityPage.module.css'

type T = ReturnType<typeof useT>['jobs']

const KEYS = ['kind', 'state', 'host'] as const
/** While anything runs or waits the table follows it closely; otherwise it refreshes like the other screens. */
const BUSY = 5_000
const IDLE = 15_000

/** "14:31:40" today, "Nov 17 14:31" before, in the server's zone. */
function when(fmt: Fmt, clock: ServerClock, iso: string) {
  return fmt.date(iso) === fmt.date(clock.now) ? fmt.clock(iso) : `${fmt.date(iso)} ${fmt.hhmm(iso)}`
}

const sum = (cs: readonly JobCount[], state?: string) => cs.filter((c) => !state || c.state === state).reduce((n, c) => n + c.count, 0)

/** "38 backups · 6 restore tests": the counts of one state by kind. */
function byKind(cs: readonly JobCount[], state: string | undefined, t: T) {
  const kinds = new Map<string, number>()
  for (const c of cs) if (!state || c.state === state) kinds.set(c.kind, (kinds.get(c.kind) ?? 0) + c.count)
  return [...kinds].filter(([, n]) => n > 0).map(([k, n]) => t.kindCount(k, n)).join(' · ') || t.nothing
}

/**
 * Activity (docs/design/v2/Activity.dc.html): what runs, what waits and what finished in the last hour, then every
 * job newest first, filtered in the URL by kind, state and host. The table is paged and grows as it scrolls.
 */
export function ActivityPage() {
  const t = useT().jobs
  const [f, setF] = useFilters(KEYS)
  const [refresh, setRefresh] = useState(IDLE)
  const q = useActivity({ kind: f.kind || undefined, state: f.state || undefined, host_id: f.host ? Number(f.host) : undefined }, refresh)
  const hosts = useHosts().data ?? []
  // The totals do not depend on the filter: keep the last ones while a new filter's first page is on its way.
  const last = useRef<ActivityView | undefined>(undefined)
  if (q.head) last.current = q.head
  const head = q.head ?? last.current
  const busy = !!head && sum(head.open) > 0
  useEffect(() => setRefresh(busy ? BUSY : IDLE), [busy])
  const filtered = !!(f.kind || f.state || f.host)
  return (
    <main className={s.page}>
      {head ? <Totals v={head} /> : <PageHead question={t.question} answer={t.title} />}
      <div className={s.filters}>
        <Seg label={t.f.kind} value={f.kind} options={t.f.kinds} onChange={(x) => setF({ kind: x })} />
        <Seg label={t.f.state} value={f.state} options={t.f.states} onChange={(x) => setF({ state: x })} />
        <Pick label={t.f.host} value={f.host} onChange={(x) => setF({ host: x })}
          options={[['', t.f.any], ...hosts.map((h) => [String(h.id), h.name] as const)]} />
        {filtered && <button type="button" className={s.link} onClick={() => setF({ kind: '', state: '', host: '' })}>{t.f.clearAll}</button>}
        <span className={s.updates}>{t.updates(refresh / 1000)}</span>
      </div>
      {q.isPending ? <Loading /> : q.isError ? <Failed error={q.error} onRetry={() => q.refetch()} /> : q.rows.length === 0 ? (
        <p className={s.empty}>{filtered ? t.noneMatch : t.empty}</p>
      ) : (
        <section>
          <div className={s.scroll}>
            <table className={s.tbl}>
              <thead>
                <tr>
                  <th scope="col" className={s.w150}>{t.col.state}</th>
                  <th scope="col">{t.col.job}</th>
                  <th scope="col">{t.col.target}</th>
                  <th scope="col">{t.col.result}</th>
                  <th scope="col">{t.col.started}</th>
                  <th scope="col" className={s.num}>{t.col.took}</th>
                  <th scope="col">{t.col.by}</th>
                </tr>
              </thead>
              <tbody>
                {q.rows.map((r) => <JobRow key={r.id} r={r} clock={q.data.pages[0].clock} />)}
              </tbody>
            </table>
          </div>
          <MoreRows shown={q.rows.length} total={q.head?.total ?? 0} more={!!q.hasNextPage} loading={q.isFetchingNextPage} onMore={() => q.fetchNextPage()} />
        </section>
      )}
    </main>
  )
}

function Totals({ v }: { v: ActivityView }) {
  const t = useT().jobs
  const running = sum(v.open, 'running')
  const queued = sum(v.open, 'queued')
  const finished = sum(v.last_hour)
  return (
    <PageHead
      question={t.question}
      answer={running + queued === 0 ? t.idle : t.answer(running, queued)}
      lede={
        <dl className={s.stats} aria-label={t.totals}>
          <div><dt>{t.running}</dt><dd><b>{running.toLocaleString('en-US')}</b><span>{byKind(v.open, 'running', t)}</span></dd></div>
          <div><dt>{t.queued}</dt><dd><b>{queued.toLocaleString('en-US')}</b><span>{byKind(v.open, 'queued', t)}</span></dd></div>
          <div>
            <dt>{t.lastHour}</dt>
            <dd><b>{finished.toLocaleString('en-US')}</b><span>{t.finished(sum(v.last_hour, 'failed'))}</span></dd>
          </div>
        </dl>
      }
    />
  )
}

/** A job's state as shape, word and colour; a finished test or check that did not pass says its verdict too. */
function JobState({ r }: { r: ActivityRow }) {
  const t = useT().jobs
  switch (r.state) {
    case 'running': return <StatusChip status="OK" size="sm" word={t.word.running} />
    case 'queued': return <StatusChip status="UNKNOWN" size="sm" word={t.word.queued} />
    case 'failed': return <StatusChip status="CRITICAL" size="sm" word={t.word.failed} />
    case 'cancelled': return <StatusChip status="WARNING" size="sm" word={t.word.cancelled} />
    case 'ok':
      return r.verdict && r.verdict !== 'OK'
        ? <StatusChip status={r.verdict} size="sm" word={t.doneWith(r.verdict)} />
        : <StatusChip status="OK" size="sm" word={t.word.ok} />
    default: return <StatusChip status="UNKNOWN" size="sm" word={r.state.toUpperCase()} />
  }
}

function JobRow({ r, clock }: { r: ActivityRow; clock: ServerClock }) {
  const t = useT().jobs
  const fmt = serverTime(clock)
  const ids = [t.jobId(r.id), r.test_id !== null && t.testId(r.test_id), r.migration_check_id !== null && t.checkId(r.migration_check_id)].filter(Boolean).join(' · ')
  const result = [
    r.state === 'running' && t.runningNow,
    r.state === 'queued' && t.waitingSince(when(fmt, clock, r.created_at)),
    r.proven_rto_s !== null && t.ready(dur(r.proven_rto_s)),
    r.point_id !== null && t.point(r.point_id, r.point_bytes),
  ].filter(Boolean).join(' · ')
  const took = r.started_at && r.finished_at ? secondsBetween(r.started_at, r.finished_at) : r.started_at && r.state === 'running' ? secondsBetween(r.started_at, clock.now) : null
  return (
    <tr className={r.state === 'failed' ? s.failed : ''}>
      <td><JobState r={r} /></td>
      <td><span className={s.kind}>{t.kind(r.kind)}</span><span className={`${s.of} ${s.mono}`}>{ids}</span></td>
      <td>
        {r.vm_id !== null ? <Link className={s.vm} to={`/vms/${r.vm_id}`}>{r.vm}</Link> : <span className={s.name}>{r.vm || t.noVM}</span>}
        {r.host && <span className={s.of}>{r.host}</span>}
      </td>
      <td className={s.result}>
        {result && <p>{result}</p>}
        {r.error && <p className={s.error} title={r.error}>{r.error}</p>}
        {r.test_reason && <p className={s.reason} title={r.test_reason}>{r.test_reason}</p>}
      </td>
      <td className={s.mono}>{r.started_at ? when(fmt, clock, r.started_at) : '—'}</td>
      <td className={`${s.num} ${s.mono}`}>{dur(took)}</td>
      <td className={s.by}>{r.by || '—'}</td>
    </tr>
  )
}
