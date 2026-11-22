import { useT } from '../../../i18n'
import { serverTime } from '../../../lib/time'
import { useAuditPages, type AuditEntry } from '../../api/inventory'
import { useSystem } from '../../api/views'
import { Failed, Loading } from '../../layout/States'
import { DataTable, PageHead, TechDetails, type Column } from '../../ui'
import s from '../Inventory.module.css'
import { auditOn, auditResult, auditWhat } from './words'

/**
 * The audit log (admins and auditors): newest first, a page at a time from the server. Each entry in words (who did
 * what, on what, from where, and whether it was done); the route, target and detail as recorded are in the row's
 * technical details (docs/design/v3 rule 3).
 */
export function Audit() {
  const t = useT().inventory
  const a = t.audit
  const pages = useAuditPages()
  const sys = useSystem().data
  if (pages.isPending || !sys) return <Loading />
  if (pages.isError) return <Failed error={pages.error} onRetry={() => pages.refetch()} />
  const fmt = serverTime(sys.clock)
  const who = (e: AuditEntry) => e.username || (e.user_id === null ? a.anonymous : a.system)
  const cols: Column<AuditEntry>[] = [
    { key: 'when', header: a.cols.when, width: '140px', cell: (e) => fmt.at(e.at) },
    {
      key: 'who', header: a.cols.who, width: '170px', title: who,
      cell: (e) => <><span className={s.name}>{who(e)}</span>{e.role && <span className={s.cellOf}>{e.role}</span>}</>,
    },
    { key: 'what', header: a.cols.what, title: auditWhat, cell: (e) => auditWhat(e) },
    { key: 'on', header: a.cols.target, width: '260px', title: auditOn, cell: (e) => auditOn(e) || t.dash },
    { key: 'from', header: a.cols.from, width: '140px', cell: (e) => <span className={s.mono}>{e.remote_addr || t.dash}</span> },
    {
      key: 'result', header: a.cols.result, width: '110px',
      cell: (e) => {
        const r = auditResult(e.outcome)
        return <span className={r === 'done' ? s.fg2 : r === 'asked' ? s.cellOf : s.strong}>{r === 'refused' ? a.refusedCode(e.outcome) : a.result[r]}</span>
      },
    },
  ]
  return (
    <>
      <PageHead question={a.question} answer={a.answer(pages.total ?? 0)} lede={<p>{a.hint}</p>} />
      <section className={s.sec}>
        <DataTable<AuditEntry>
          caption={a.caption}
          columns={cols}
          pages={pages}
          rowKey={(e) => String(e.seq)}
          rowState={(e) => (['refused', 'failed'].includes(auditResult(e.outcome)) ? 'bad' : undefined)}
          minWidth={980}
          empty={a.empty}
          detail={(e) => (
            <div className={s.facts}>
              <p>{a.asSeq(e.seq, fmt.at(e.at))}</p>
              <TechDetails>{[`${a.route}: ${e.action}`, e.target && `${a.target}: ${e.target}`, e.detail && `${a.detail}: ${e.detail}`, `sha256 ${e.sha256}`]
                .filter(Boolean).join('\n')}</TechDetails>
            </div>
          )}
        />
      </section>
    </>
  )
}
