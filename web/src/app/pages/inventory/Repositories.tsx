import { useT } from '../../../i18n'
import { serverTime, type Fmt } from '../../../lib/time'
import { can } from '../../access'
import { change, useRepositoryPages, type RepositoryRow } from '../../api/inventory'
import type { User } from '../../api/session'
import { useSystem } from '../../api/views'
import { Failed, Loading } from '../../layout/States'
import { Button, DataTable, PageHead, StatusChip, TechDetails, type Column } from '../../ui'
import s from '../Inventory.module.css'
import { useAction } from './common'

type T = ReturnType<typeof useT>['inventory']

/** How many points the newest verification found damaged, when the server lists them. */
function affected(v: RepositoryRow['last_verification']): number | null {
  return v && Array.isArray(v.affected) ? v.affected.length : null
}

/**
 * A repository's status: quarantined contents, or damage the newest verification found, are CRITICAL; a repository
 * never verified, a verification that could not finish or one still running say nothing yet, so they are UNKNOWN.
 */
function status(r: RepositoryRow, fmt: Fmt, t: T) {
  const w = t.repositories.word
  const x = t.repositories.text
  const v = r.last_verification
  if (r.quarantined_contents > 0) return { status: 'CRITICAL', word: w.damaged, text: x.damaged(r.quarantined_contents) }
  if (!v) return { status: 'UNKNOWN', word: w.notVerified, text: x.never }
  if (v.state === 'critical') return { status: 'CRITICAL', word: w.damaged, text: x.found(affected(v)) }
  if (v.state === 'failed') return { status: 'UNKNOWN', word: w.failed, text: x.failed }
  if (v.state === 'running') return { status: 'UNKNOWN', word: w.verifying, text: x.running(fmt.at(v.started_at)) }
  if (v.state === 'ok') return { status: 'OK', word: undefined, text: x.ok }
  return { status: 'UNKNOWN', word: undefined, text: t.repositories.state(v.state) }
}

function bytes(b: number) {
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  let x = b
  while (x >= 1024 && i < u.length - 1) { x /= 1024; i++ }
  return `${i === 0 ? x : x.toFixed(1)} ${u[i]}`
}

/** Where points are stored (docs/design/v3 rule 1: a data-table paged on the server). */
export function Repositories({ me }: { me: User }) {
  const t = useT().inventory
  const r = t.repositories
  const pages = useRepositoryPages()
  const sys = useSystem().data
  const act = useAction()
  const writer = can(me, 'act')
  if (pages.isPending || !sys) return <Loading />
  if (pages.isError) return <Failed error={pages.error} onRetry={() => pages.refetch()} />
  const fmt = serverTime(sys.clock)
  const total = pages.total ?? 0
  // Rule 7: the headline counts each state only when this page holds every repository; otherwise it says how many.
  const whole = pages.rows.length === total
  const counts = { damaged: 0, unknown: 0, ok: 0 }
  for (const x of pages.rows) {
    const st = status(x, fmt, t).status
    if (st === 'CRITICAL') counts.damaged++
    else if (st === 'OK') counts.ok++
    else counts.unknown++
  }
  const headline = total === 0 ? r.none : whole
    ? (['damaged', 'unknown', 'ok'] as const).map((k) => `${t.count(counts[k])} ${r.states[k]}`).join(' · ')
    : r.answer(total)
  const verify = (x: RepositoryRow, mode: 'sampled' | 'full') =>
    act.run(`verify/${x.id}`, 'POST', `/api/v1/repositories/${x.id}/verify`, (h) => change.verifyRepository(x.id, mode, h), (v) => r.started(x.name, v.id))

  const cols: Column<RepositoryRow>[] = [
    {
      key: 'repository', header: r.cols.repository, width: '230px', title: (x) => r.kind(x.kind, x.location),
      cell: (x) => <><span className={s.name}>{x.name}</span><span className={s.cellOf}>{r.kind(x.kind, x.location)}</span></>,
    },
    {
      key: 'status', header: r.cols.status, title: (x) => status(x, fmt, t).text,
      cell: (x) => { const st = status(x, fmt, t); return <><StatusChip status={st.status} word={st.word} size="sm" /><span>{st.text}</span></> },
    },
    { key: 'used', header: r.cols.used, width: '170px', cell: (x) => r.used(x.policies, x.vms) },
    {
      key: 'verify', header: r.cols.verify, width: '180px',
      cell: (x) => {
        const v = x.last_verification
        if (!v) return r.never
        if (v.state === 'running') return r.running(r.mode(v.mode), fmt.at(v.started_at))
        return `${r.mode(v.mode)}, ${fmt.at(v.finished_at || v.started_at)}, ${r.state(v.state)}`
      },
    },
    { key: 'q', header: r.cols.quarantined, width: '105px', numeric: true, cell: (x) => <b className={x.quarantined_contents > 0 ? s.fig : s.fg2}>{r.contents(x.quarantined_contents)}</b> },
  ]
  if (writer) {
    cols.push({
      key: 'action', header: r.cols.action, width: '215px',
      cell: (x) => {
        const busy = x.last_verification?.state === 'running' || act.busy === `verify/${x.id}`
        return (
          <>
            <Button variant="quiet" className={s.cellBtn} disabled={busy} onClick={() => verify(x, 'sampled')}>{r.sampled}</Button>
            <Button variant="quiet" className={s.cellBtn} disabled={busy} onClick={() => verify(x, 'full')}>{r.full}</Button>
          </>
        )
      },
    })
  }
  return (
    <>
      <PageHead question={r.question} answer={headline} lede={<p>{r.hint}</p>} />
      <section className={s.sec}>
        <DataTable<RepositoryRow>
          caption={r.caption}
          columns={cols}
          pages={pages}
          rowKey={(x) => String(x.id)}
          rowState={(x) => (status(x, fmt, t).status === 'CRITICAL' ? 'critical' : undefined)}
          minWidth={1000}
          empty={r.empty}
          detail={(x) => <RepositoryDetail x={x} fmt={fmt} />}
        />
        {act.outcome}
        <p className={s.foot}>{r.foot}</p>
      </section>
    </>
  )
}

function RepositoryDetail({ x, fmt }: { x: RepositoryRow; fmt: Fmt }) {
  const t = useT().inventory
  const r = t.repositories
  const v = x.last_verification
  const st = status(x, fmt, t)
  return (
    <div className={s.facts}>
      <p>{st.text}.</p>
      {v && <p>{r.detail(r.mode(v.mode), r.why(v.reason), fmt.at(v.started_at), v.finished_at ? fmt.at(v.finished_at) : '')}</p>}
      {v && v.state !== 'running' && <p>{r.read(v.points_checked, v.points_skipped, bytes(v.bytes_read))}</p>}
      <TechDetails>{[x.location, v?.error].filter(Boolean).join('\n')}</TechDetails>
    </div>
  )
}
