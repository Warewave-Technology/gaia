import { useT } from '../../../i18n'
import { serverTime, type Fmt, type ServerClock } from '../../../lib/time'
import { can } from '../../access'
import { change, useRepositoryRows, type RepositoryRow } from '../../api/inventory'
import type { User } from '../../api/session'
import { Failed, Loading } from '../../layout/States'
import { Button, MoreRows, PageHead, StatusChip } from '../../ui'
import s from '../Inventory.module.css'
import { useAction, when } from './common'

type T = ReturnType<typeof useT>['inventory']

/**
 * A repository's status: quarantined contents, or damage the newest verification found, are CRITICAL; a repository
 * never verified, a verification that could not finish or one still running say nothing yet, so they are UNKNOWN.
 */
function status(r: RepositoryRow, fmt: Fmt, clock: ServerClock, t: T) {
  const w = t.repositories.word
  const v = r.last_verification
  if (r.quarantined_contents > 0) return { status: 'CRITICAL', word: w.damaged, text: t.repositories.damagedText(r.quarantined_contents) }
  if (!v) return { status: 'UNKNOWN', word: w.notVerified, text: '' }
  if (v.state === 'critical') return { status: 'CRITICAL', word: w.damaged, text: v.reason }
  if (v.state === 'failed') return { status: 'UNKNOWN', word: w.failed, text: v.error }
  if (v.state === 'running') return { status: 'UNKNOWN', word: w.verifying, text: when(fmt, clock, v.started_at, t) }
  if (v.state === 'ok') return { status: 'OK', word: undefined, text: '' }
  return { status: 'UNKNOWN', word: undefined, text: v.state }
}

export function Repositories({ me }: { me: User }) {
  const t = useT().inventory
  const r = t.repositories
  const q = useRepositoryRows()
  const act = useAction()
  const writer = can(me, 'act')
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const total = q.head?.total ?? 0
  const clock = q.head?.clock ?? { now: new Date().toISOString(), zone: 'UTC', offset_s: 0 }
  const fmt = serverTime(clock)
  const damaged = q.rows.filter((x) => status(x, fmt, clock, t).status === 'CRITICAL').length
  const verify = (x: RepositoryRow, mode: 'sampled' | 'full') =>
    act.run(`verify/${x.id}`, 'POST', `/api/v1/repositories/${x.id}/verify`, (h) => change.verifyRepository(x.id, mode, h), (v) => r.started(x.name, v.id))
  return (
    <>
      <PageHead
        question={r.question}
        answer={damaged > 0 && !q.hasNextPage ? r.damaged(damaged, total) : r.answer(total)}
        lede={<p>{r.hint}</p>}
      />
      <section className={s.sec}>
        {q.rows.length === 0 ? <p className={s.empty}>{r.empty}</p> : (
          <div className={s.scroll}>
            <table className={s.tbl}>
              <thead>
                <tr>
                  <th scope="col">{r.cols.repository}</th>
                  <th scope="col">{r.cols.status}</th>
                  <th scope="col">{r.cols.used}</th>
                  <th scope="col">{r.cols.verify}</th>
                  <th scope="col" className={s.num}>{r.cols.quarantined}</th>
                  {writer && <th scope="col"><span className={s.vh}>{t.action}</span></th>}
                </tr>
              </thead>
              <tbody>
                {q.rows.map((x) => {
                  const st = status(x, fmt, clock, t)
                  const v = x.last_verification
                  const running = v?.state === 'running'
                  return (
                    <tr key={x.id} className={st.status === 'CRITICAL' ? s.rCRITICAL : st.status === 'UNKNOWN' ? s.rUNKNOWN : ''}>
                      <th scope="row">
                        <span className={s.name}>{x.name}</span>
                        <span className={s.of}>{r.kind(x.kind, x.location)}</span>
                      </th>
                      <td>
                        <span className={s.state}>
                          <StatusChip status={st.status} word={st.word} size="sm" />
                          {st.text && <span>{st.text}</span>}
                        </span>
                      </td>
                      <td className={s.fg2}>{r.used(x.policies, x.vms)}</td>
                      <td className={s.fg2}>
                        {!v ? r.never : running ? r.running(r.mode(v.mode), when(fmt, clock, v.started_at, t)) : (
                          <>
                            {r.verification(r.mode(v.mode), when(fmt, clock, v.finished_at, t), v.points_checked)}
                            <span className={s.of}>{r.state(v.state)}</span>
                          </>
                        )}
                      </td>
                      <td className={s.num}><b className={x.quarantined_contents > 0 ? s.fig : s.fg2}>{r.contents(x.quarantined_contents)}</b></td>
                      {writer && (
                        <td>
                          <span className={s.acts}>
                            <Button variant="quiet" disabled={running || act.busy === `verify/${x.id}`} onClick={() => verify(x, 'sampled')}>{r.sampled}</Button>
                            <Button variant="quiet" disabled={running || act.busy === `verify/${x.id}`} onClick={() => verify(x, 'full')}>{r.full}</Button>
                          </span>
                        </td>
                      )}
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
        {act.outcome}
        <MoreRows shown={q.rows.length} total={total} more={!!q.hasNextPage} loading={q.isFetchingNextPage} onMore={() => q.fetchNextPage()} />
        <p className={s.foot}>{r.foot}</p>
      </section>
    </>
  )
}
