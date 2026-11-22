import { useT } from '../../../i18n'
import type { Fmt } from '../../../lib/time'
import type { VMView } from '../../api/views'
import { StatusChip, TechDetails } from '../../ui'
import s from '../VMPage.module.css'

/** A key id as people compare it: its start and its end. */
const short = (k: string) => (k.length > 20 ? `${k.slice(0, 8)}…${k.slice(-7)}` : k)

/**
 * The newest test's evidence (ADR-006): its bundle verified now against the installation's key, who signed it, its
 * ledger entry and whether a daily report anchors it yet.
 */
export function Evidence({ v, fmt }: { v: VMView; fmt: Fmt }) {
  const t = useT().vm3.ev
  const test = v.tests.find((x) => x.finished_at)
  if (!test) return <p className={s.muted}>{t.none}</p>
  if (!test.bundle_id) return <p className={s.muted}>{t.noBundle}</p>
  const b = v.latest_bundle?.bundle_id === test.bundle_id ? v.latest_bundle : null
  const rep = v.daily_report
  const anchored = rep !== null && test.ledger_seq > 0 && test.ledger_seq <= rep.head_seq
  const checked = v.ledger.verdict === 'OK' && test.ledger_seq > 0 && test.ledger_seq <= v.ledger.head_seq ? fmt.at(v.ledger.checked_at) : null
  return (
    <div className={s.tabbody}>
      <p className={s.sub}>{t.of(test.id, fmt.at(test.finished_at))}</p>
      <dl className={s.ev}>
        <dt>{t.bundle}</dt>
        <dd>
          {b ? <StatusChip status={b.verdict} size="xs" word={b.verdict === 'OK' ? t.verified : t.invalid} /> : <span className={s.neutral}>{t.notChecked}</span>}
          <span className={s.mono} title={test.bundle_id}>{test.bundle_id}</span>
          {b?.verdict === 'OK' && <span className={s.sub}>{t.files(b.files)}</span>}
        </dd>
        {b && b.verdict !== 'OK' && b.problems.length > 0 && (
          <>
            <dt />
            <dd><TechDetails summary={t.problems}>{b.problems.join('\n')}</TechDetails></dd>
          </>
        )}
        {b?.key_id && (
          <>
            <dt>{t.signedBy}</dt>
            <dd><span className={s.mono} title={b.key_id}>{short(b.key_id)}</span></dd>
          </>
        )}
        <dt>{t.ledger}</dt>
        <dd>{test.ledger_seq > 0 ? t.entry(test.ledger_seq, checked) : t.notInLedger}</dd>
        {test.ledger_seq > 0 && (
          <>
            <dt>{t.anchored}</dt>
            <dd>{anchored && rep ? t.yes(fmt.at(rep.generated_at)) : t.notYet}</dd>
          </>
        )}
      </dl>
    </div>
  )
}
