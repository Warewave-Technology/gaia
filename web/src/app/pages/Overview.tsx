import { Link } from 'react-router'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { serverTime, type Fmt } from '../../lib/time'
import { useOverview, type OverviewView, type VMOverview } from '../api/views'
import { Failed, Loading } from '../layout/States'
import { GaiaMark, HistoryStrip, Legend, Notice, ProofBar, ProofScale, StatusChip, asStatus } from '../ui'
import s from './Overview.module.css'

const ORDER = ['CRITICAL', 'UNKNOWN', 'WARNING', 'OK'] as const

/** The overview (L1 + L2): which point every VM can return to, problems first; the installation's own state. */
export function Overview() {
  const q = useOverview()
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  return <OverviewBody v={q.data} />
}

function OverviewBody({ v }: { v: OverviewView }) {
  const t = useT().overview
  const fmt = serverTime(v.clock)
  const counts = Object.fromEntries(ORDER.map((st) => [st, v.vms.filter((r) => asStatus(r.status) === st).length]))
  const aged = v.vms.filter((r) => r.proof_age_s !== null)
  const worst = aged.length ? aged.reduce((a, b) => ((a.proof_age_s ?? 0) >= (b.proof_age_s ?? 0) ? a : b)) : null
  const never = v.vms.filter((r) => r.proven === null).map((r) => r.vm)
  const hours = Math.round(v.window_s / 3600)
  return (
    <main className={s.ov}>
      <section className={s.fleet} aria-labelledby="q">
        <div className={s.q}>
          <h1 id="q"><GaiaMark size={36} className={s.qmark} />{t.title}</h1>
        </div>
        <ul className={s.tally} aria-label={t.byStatus}>
          {ORDER.map((st) => (
            <li key={st} className={`${s[st.toLowerCase()]} ${counts[st] ? '' : s.zero}`}>
              <span className={s.n}>{counts[st]}</span>
              <StatusChip status={st} size="sm" />
            </li>
          ))}
        </ul>
        <dl className={s.facts}>
          <div><dt>{t.oldestProof}</dt><dd>{worst ? <><b>{dur(worst.proof_age_s)}</b>{worst.vm}</> : t.none}</dd></div>
          <div><dt>{t.neverProven}</dt><dd><b>{never.length}</b>{never.join(', ') || t.none}</dd></div>
          <div>
            <dt>{t.ledger}</dt>
            <dd><StatusChip status={v.ledger.verdict} size="xs" />{v.ledger.checked_at ? t.ledgerText(v.ledger.entries, fmt.hhmm(v.ledger.checked_at)) : t.ledgerUnchecked}</dd>
          </div>
          <div>
            <dt>{t.kit}</dt>
            <dd><StatusChip status={v.recovery_kit.state === 'confirmed' ? 'OK' : 'WARNING'} size="xs" />{v.recovery_kit.confirmed_at
              ? t.kitSaved(v.recovery_kit.generation, `${fmt.date(v.recovery_kit.confirmed_at)} ${fmt.hhmm(v.recovery_kit.confirmed_at)}`, v.recovery_kit.confirmed_by ?? '')
              : v.recovery_kit.text}</dd>
          </div>
        </dl>
        {v.notifications.state !== 'OK' && <div className={s.wide}><Notice tone="warning">{v.notifications.text}</Notice></div>}
        {v.ledger.verdict !== 'OK' && v.ledger.checked_at && <div className={s.wide}><Notice tone="critical">{v.ledger.text}</Notice></div>}
      </section>

      {v.vms.length === 0 ? (
        <p className={s.empty}>{t.empty}</p>
      ) : (
        <section className={s.vms} aria-label={t.cols.vm}>
          <Legend />
          <table className={s.grid}>
            <thead>
              <tr>
                <th scope="col">{t.cols.vm}</th>
                <th scope="col">{t.cols.ret}</th>
                <th scope="col" className={s.cProof}>{t.cols.proof}<ProofScale /></th>
                <th scope="col" className={s.cNum}>{t.cols.rto}</th>
                <th scope="col" className={s.cNum}>{t.cols.age}</th>
                <th scope="col" className={s.cHist}>{t.cols.hist(hours)}</th>
                <th scope="col">{t.cols.why}</th>
              </tr>
            </thead>
            <tbody>
              {v.vms.map((r) => <Row key={r.vm_id} r={r} fmt={fmt} now={v.clock.now} windowS={v.window_s} />)}
            </tbody>
          </table>
        </section>
      )}
    </main>
  )
}

function Row({ r, fmt, now, windowS }: { r: VMOverview; fmt: Fmt; now: string; windowS: number }) {
  const t = useT().overview
  const st = asStatus(r.status)
  const tg = r.targets
  return (
    <tr className={s[`r${st}`]}>
      <th scope="row" className={s.cVm}>
        <StatusChip status={st} />
        <Link className={s.vm} to={`/vms/${r.vm_id}`}>{r.vm}</Link>
        <span className={s.sub}>{r.host}{r.policy ? '' : `, ${t.noPolicy}`}</span>
      </th>
      <td className={s.cRet}>
        {r.proven ? (
          <>
            <span className={s.rt}>{fmt.clock(r.proven.point_taken_at)}</span>
            <span className={s.rd}>{t.pointOn(fmt.date(r.proven.point_taken_at), r.proven.point_id)}</span>
            <span className={s.rs}>{t.provenBy(r.proven.test_id, fmt.hhmm(r.proven.test_finished_at))}</span>
          </>
        ) : (
          <>
            <span className={`${s.rt} ${s.none}`}>{t.nothingProven}</span>
            <span className={s.rs}>{t.noPassingTest}</span>
          </>
        )}
      </td>
      <td className={s.cProof}>
        {tg ? <ProofBar value={r.proven_rpo_s} newest={r.theoretical_rpo_s} target={tg.rpo_target_s} label={t.cols.proof} /> : null}
        <span className={s.fig}>
          {r.proven_rpo_s !== null ? <b>{dur(r.proven_rpo_s)}</b> : <b className={s.noneB}>{t.none}</b>}
          {tg && <span className={s.of}>{t.target(dur(tg.rpo_target_s))}</span>}
        </span>
      </td>
      <td className={s.cNum}>
        <b>{dur(r.proven_rto_s)}</b>
        {tg && <span className={s.of}>{t.target(dur(tg.rto_target_s))}</span>}
      </td>
      <td className={s.cNum}>
        <b className={tg && r.proof_age_s !== null && r.proof_age_s > tg.max_proof_age_s ? s.over : ''}>{dur(r.proof_age_s)}</b>
        {tg && <span className={s.of}>{t.max(dur(tg.max_proof_age_s))}</span>}
      </td>
      <td className={s.cHist}>
        <HistoryStrip events={r.events} now={now} windowS={windowS} returnAt={r.proven?.point_taken_at} />
      </td>
      <td className={s.cWhy}>
        <p>{r.reasons[0]?.text}</p>
        {(r.reasons.length > 1 || r.notes.length > 0) && (
          <details>
            <summary>{t.reasons}</summary>
            <ul>
              {r.reasons.map((x, i) => <li key={i}><StatusChip status={x.status} size="xs" /><span>{x.text}</span></li>)}
              {r.notes.map((n, i) => <li key={`n${i}`} className={s.note}><span className={s.nl}>{t.note}</span><span>{n}</span></li>)}
            </ul>
          </details>
        )}
      </td>
    </tr>
  )
}
