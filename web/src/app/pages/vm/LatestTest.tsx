import { useT } from '../../../i18n'
import { dur } from '../../../lib/duration'
import type { Fmt } from '../../../lib/time'
import type { TestView, VMView } from '../../api/views'
import { detailOf, stepOf } from './logic'
import { StatusChip, TechDetails } from '../../ui'
import s from '../VMPage.module.css'

/** The newest finished restore test: its steps as sentences with their times, its raw output folded, its screenshots. */
export function LatestTest({ v, fmt }: { v: VMView; fmt: Fmt }) {
  const t = useT().vm3
  const tt = useT().vm
  const test = v.tests.find((x) => x.finished_at)
  if (!test) return <p className={s.muted}>{t.noTests}</p>
  const d = detailOf(test)
  const checks = d.checks ?? []
  const word = test.verdict === 'OK' ? t.word.passed : test.verdict === 'CRITICAL' ? t.word.failed : test.verdict === 'WARNING' ? t.word.warned : t.word.notRun
  const raw = [
    test.reason,
    ...checks.map((c) => `${c.name}: ${c.verdict}${c.detail ? `: ${c.detail}` : ''}`),
    ...(d.errors ?? []),
  ].filter(Boolean).join('\n')
  return (
    <div className={s.testgrid}>
      <div>
        <div className={s.testhead}>
          <StatusChip status={test.verdict} size="sm" word={word} />
          <span className={`${s.sub} ${s.mono}`}>{t.testOf(test.id, test.point_id, fmt.at(test.finished_at))}</span>
        </div>
        <ol className={s.steps}>
          {d.restore_s !== undefined && (
            <li>
              <StatusChip status="OK" size="xs" word={t.word.ok} />
              <span><span className={s.stepName}>{t.step.restore}</span><span className={s.stepText}>{t.step.restoreText(test.point_id)}</span></span>
              <span className={s.secs}>{dur(d.restore_s, true)}</span>
            </li>
          )}
          {checks.map((c) => {
            const st = stepOf(c, t)
            return (
              <li key={c.name}>
                {st.notRun ? <span className={s.neutral}>{t.step.notRunWord}</span>
                  : <StatusChip status={c.verdict} size="xs" word={c.verdict === 'CRITICAL' ? t.word.failed : undefined} />}
                <span><span className={s.stepName}>{st.name}</span><span className={s.stepText}>{st.text}</span></span>
                <span className={s.secs}>{c.seconds ? dur(c.seconds, true) : '—'}</span>
              </li>
            )
          })}
          {checks.length === 0 && <li className={s.nosteps}>{tt.noChecks}</li>}
        </ol>
        {test.proven_rto_s !== null && <p className={s.sub}>{t.rtoOf(dur(test.proven_rto_s, true), d.restore_s !== undefined ? dur(d.restore_s, true) : null)}</p>}
        <TechDetails summary={t.testDetails}>{raw}</TechDetails>
      </div>
      <Shots test={test} />
    </div>
  )
}

/** The console screenshots sealed in the test's bundle: at boot and at the end of the test. */
function Shots({ test }: { test: TestView }) {
  const t = useT().vm3
  if (test.screenshots.length === 0 || test.evidence_id <= 0) return <p className={s.muted}>{t.noShots}</p>
  const caption = (n: string) => {
    const k = n.replace(/^screenshot-|\.png$/g, '')
    return k === 'boot' ? t.shotBoot : k === 'end' ? t.shotEnd : k
  }
  return (
    <div className={s.shots}>
      {test.screenshots.map((n) => (
        <figure key={n}>
          <img src={`/api/v1/evidence/${test.evidence_id}/files/${encodeURIComponent(n)}`} width={720} height={450} alt={t.shotAlt(caption(n))} loading="lazy" />
          <figcaption>{caption(n)}</figcaption>
        </figure>
      ))}
    </div>
  )
}
