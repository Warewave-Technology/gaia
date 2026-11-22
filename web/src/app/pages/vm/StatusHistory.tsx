import { useT } from '../../../i18n'
import { dur } from '../../../lib/duration'
import { secondsBetween, type Fmt } from '../../../lib/time'
import { historyFromView, useVMStatusHistory, type VMStatusChange } from '../../api/vm'
import type { VMView } from '../../api/views'
import { DataTable, StatusChip, TechDetails, type Column } from '../../ui'
import { whyOf } from './logic'
import s from '../VMPage.module.css'

/** Every change of the VM's status, newest first, in server pages: when, from → to, how long it lasted, why. */
export function StatusHistory({ v, fmt }: { v: VMView; fmt: Fmt }) {
  const t = useT().vm3
  const { pages, missing } = useVMStatusHistory(v.vm.id)
  const columns: Column<VMStatusChange>[] = [
    { key: 'when', header: t.col.when, width: '170px', cell: (c) => t.cap(t.day(fmt.at(c.at))) },
    {
      key: 'change', header: t.col.change, width: '260px',
      cell: (c) => (
        <span className={s.change}>
          {c.from ? <StatusChip status={c.from} size="xs" /> : <span className={s.neutral}>{t.added}</span>}
          <span className={s.arrow} aria-label={t.to}>→</span>
          <StatusChip status={c.to} size="xs" />
        </span>
      ),
    },
    {
      key: 'lasted', header: t.col.lasted, width: '190px', numeric: true,
      cell: (c) => <span className={s.mono}>{c.until ? dur(secondsBetween(c.at, c.until)) : t.soFar(dur(secondsBetween(c.at, v.clock.now)))}</span>,
    },
    { key: 'why', header: t.col.why, title: (c) => whyOf(c, t), cell: (c) => <span className={s.fg2}>{whyOf(c, t)}</span> },
  ]
  return (
    <div className={s.tabbody}>
      <DataTable<VMStatusChange> caption={t.historyTable} columns={columns} rowKey={(c) => String(c.seq)} empty={t.noHistory} height="420px"
        {...(missing ? { rows: historyFromView(v) } : { pages })}
        detail={(c) => (c.reason ? <TechDetails summary={t.changeDetails}>{c.reason}</TechDetails> : null)} />
    </div>
  )
}
