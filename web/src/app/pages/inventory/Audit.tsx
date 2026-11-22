import { useT } from '../../../i18n'
import { serverTime } from '../../../lib/time'
import { useAudit } from '../../api/inventory'
import { Failed, Loading } from '../../layout/States'
import { MoreRows, PageHead } from '../../ui'
import s from '../Inventory.module.css'

/** The audit log (admins and auditors): newest first, a page at a time as the table scrolls. */
export function Audit() {
  const t = useT().inventory
  const a = t.audit
  const q = useAudit()
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const head = q.head
  const total = head?.total ?? 0
  const fmt = head ? serverTime(head.clock) : null
  return (
    <>
      <PageHead question={a.question} answer={a.answer(total)} lede={<p>{a.hint}</p>} />
      <section className={s.sec}>
        {q.rows.length === 0 || !fmt ? <p className={s.empty}>{a.empty}</p> : (
          <div className={s.scroll}>
            <table className={s.tbl}>
              <thead>
                <tr>
                  <th scope="col">{a.cols.when}</th>
                  <th scope="col">{a.cols.who}</th>
                  <th scope="col">{a.cols.action}</th>
                  <th scope="col">{a.cols.target}</th>
                  <th scope="col">{a.cols.from}</th>
                  <th scope="col">{a.cols.result}</th>
                </tr>
              </thead>
              <tbody>
                {q.rows.map((e) => (
                  <tr key={e.seq}>
                    <th scope="row">
                      <span className={s.mono}>{fmt.clock(e.at)}</span>
                      <span className={s.of}>{fmt.date(e.at)}</span>
                    </th>
                    <td>
                      <span className={s.name}>{e.username || (e.user_id === null ? a.anonymous : a.system)}</span>
                      {e.role && <span className={s.of}>{e.role}</span>}
                    </td>
                    <td>
                      {e.action}
                      {e.detail && <span className={s.of}>{e.detail}</span>}
                    </td>
                    <td className={s.mono}>{e.target || t.dash}</td>
                    <td className={s.mono}>{e.remote_addr || t.dash}</td>
                    <td className={e.outcome >= 400 ? s.fig : s.fg2}>{e.outcome >= 400 ? a.refused(e.outcome) : a.ok}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <MoreRows shown={q.rows.length} total={total} more={!!q.hasNextPage} loading={q.isFetchingNextPage} onMore={() => q.fetchNextPage()} />
      </section>
    </>
  )
}
