import { useT } from '../../../i18n'
import { dur } from '../../../lib/duration'
import type { Fmt } from '../../../lib/time'
import type { TestView, VMView } from '../../api/views'
import { Notice, SectionHead, StatusChip } from '../../ui'
import { detailOf, when, type VMStrings } from './common'
import s from '../VMPage.module.css'

/** A test's verdict in the words of a test. */
export function testWord(v: string, t: VMStrings) {
  return v === 'OK' ? t.word.passed : v === 'CRITICAL' ? t.word.failed : v === 'WARNING' ? t.word.warned : t.word.notProven
}

/** The newest finished restore test: the console as it was sealed, each step with its time, and its evidence. */
export function LatestTest({ v, fmt }: { v: VMView; fmt: Fmt }) {
  const t = useT().vm
  const test = v.tests.find((x) => x.finished_at)
  return (
    <section className={s.sec} id="test" aria-labelledby="latest-test">
      <SectionHead id="latest-test" title={t.latestTest}>
        {test && <StatusChip status={test.verdict} size="sm" word={testWord(test.verdict, t)} />}
        {test && <span className={`${s.of} ${s.mono}`}>{t.testOf(test.id, test.point_id, when(fmt, v.clock, test.finished_at, t))}</span>}
      </SectionHead>
      {test ? <TestBody test={test} v={v} fmt={fmt} /> : <p className={s.muted}>{t.noTests}</p>}
    </section>
  )
}

function TestBody({ test, v, fmt }: { test: TestView; v: VMView; fmt: Fmt }) {
  const t = useT().vm
  const d = detailOf(test)
  const checks = d.checks ?? []
  return (
    <>
      <Shots test={test} />
      {test.verdict !== 'OK' && test.reason && <Notice tone={test.verdict === 'CRITICAL' ? 'critical' : 'warning'}>{test.reason}</Notice>}
      <ul className={s.steps}>
        {d.restore_s !== undefined && (
          <li><StatusChip status="OK" size="xs" /><span>{t.restoreStep(test.point_id)}</span><span className={s.secs}>{dur(d.restore_s, true)}</span></li>
        )}
        {checks.map((c) => (
          <li key={c.name}>
            <StatusChip status={c.verdict} size="xs" />
            <span>{c.name.replace(/_/g, ' ')}{c.detail && <span className={s.of}>{c.detail}</span>}</span>
            <span className={s.secs}>{c.seconds ? dur(c.seconds, true) : '—'}</span>
          </li>
        ))}
        {checks.length === 0 && <li className={s.muted}>{t.noChecks}</li>}
      </ul>
      {test.proven_rto_s !== null && <p className={s.of}>{t.rtoOf(dur(test.proven_rto_s, true))}</p>}
      <Evidence test={test} v={v} fmt={fmt} />
    </>
  )
}

/** The test's sealed bundle: verified now (the newest), or where it is in the ledger and whether a report anchors it. */
function Evidence({ test, v, fmt }: { test: TestView; v: VMView; fmt: Fmt }) {
  const t = useT().vm
  if (!test.bundle_id) return <p className={s.muted}>{t.ev.state.none}</p>
  const b = v.latest_bundle?.bundle_id === test.bundle_id ? v.latest_bundle : null
  const rep = v.daily_report
  const anchored = rep !== null && test.ledger_seq > 0 && test.ledger_seq <= rep.head_seq
  return (
    <dl className={s.ev}>
      <div>
        <dt>{t.evidence}</dt>
        <dd>
          {b ? <StatusChip status={b.verdict} size="xs" word={b.verdict === 'OK' ? t.word.verified : t.word.invalid} /> : null}
          <code className={s.mono} title={test.bundle_id}>{test.bundle_id}</code>
          <span className={s.of}>{b ? (b.verdict === 'OK' ? t.ev.signed(b.files, b.key_id.slice(0, 12)) : b.problems.join('; ')) : t.ev.state[test.evidence] ?? test.evidence}</span>
        </dd>
      </div>
      {test.ledger_seq > 0 && (
        <div>
          <dt>{t.ledger}</dt>
          <dd>
            {t.ev.entry(test.ledger_seq)}{' '}
            {anchored && rep ? t.ev.anchored(rep.id, when(fmt, v.clock, rep.generated_at, t)) : t.ev.notAnchored}
          </dd>
        </div>
      )}
    </dl>
  )
}

/** The console screenshots sealed in a test's bundle. */
function Shots({ test }: { test: TestView }) {
  const t = useT().vm
  if (test.screenshots.length === 0 || test.evidence_id <= 0) return null
  return (
    <div className={s.shots}>
      {test.screenshots.map((n) => (
        <figure key={n}>
          <img src={`/api/v1/evidence/${test.evidence_id}/files/${encodeURIComponent(n)}`} width={720} height={450} alt={t.shot(n)} loading="lazy" />
          <figcaption>{n.replace(/^screenshot-|\.png$/g, '')}</figcaption>
        </figure>
      ))}
    </div>
  )
}
