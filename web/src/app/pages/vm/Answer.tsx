import { Link } from 'react-router'
import { useT } from '../../../i18n'
import { reason, type ReasonIn } from '../../../i18n/en-reasons'
import { ago, dur } from '../../../lib/duration'
import type { Fmt } from '../../../lib/time'
import { query } from '../../../lib/url'
import { useSameCause, type VMView } from '../../api/views'
import { asStatus, TechDetails } from '../../ui'
import s from '../VMPage.module.css'

type Reason = VMView['status']['reasons'][number]

/** A reason as a sentence an operator would say, from its code (docs/design/v3 rule 3). */
export const sentence = (r: Reason, now: number) => reason(r.code).vm(r as ReasonIn, now)
const stop = (x: string) => (/[.!?]$/.test(x) ? x : `${x}.`)

/**
 * The answer band (VM3.dc.html): where the VM returns to if it breaks now, how that was proven and how fast it would
 * run again; when it is not OK, why (from the reason code) and how many other VMs share the cause; then the four
 * measures against their targets.
 */
export function Answer({ v, fmt }: { v: VMView; fmt: Fmt }) {
  const t = useT().vm3
  const st = v.status
  const pr = st.proven
  const now = Date.parse(v.clock.now)
  const ok = asStatus(st.status) === 'OK'
  return (
    <section className={`${s.answer} ${s[`a${asStatus(st.status)}`]}`} aria-label={t.answer}>
      <div className={s.acol}>
        <span className={s.lbl}>{t.returnTo}</span>
        {pr ? (
          <>
            <div className={s.ret}>{t.cap(t.day(fmt.at(pr.point_taken_at)))} <small>{t.pointN(pr.point_id)}</small></div>
            <p className={s.atext}>{t.provenBy(fmt.at(pr.test_finished_at), st.proven_rto_s !== null ? dur(st.proven_rto_s, true) : null)}</p>
          </>
        ) : (
          <>
            <div className={`${s.ret} ${s.nothing}`}>{t.nothing}</div>
            <p className={s.atext}>{t.nothingWhat}</p>
          </>
        )}
        {!ok && st.reasons.length > 0 && <Why v={v} now={now} />}
      </div>
      <Metrics v={v} />
    </section>
  )
}

function Why({ v, now }: { v: VMView; now: number }) {
  const t = useT().vm3
  const st = v.status
  const pr = st.proven
  const [top, ...more] = st.reasons
  // "Nothing proven yet" already says what never_proven would.
  const rest = more.filter((r) => pr || r.code !== 'never_proven')
  // How many restore tests failed after the one that proved the point (the prototype's "4 restore tests since then").
  const failedSince = pr && (top.code.startsWith('test_failed') || top.code === 'guest_rebooted')
    ? v.tests.filter((x) => x.finished_at && x.verdict === 'CRITICAL' && Date.parse(x.finished_at) > Date.parse(pr.test_finished_at)).length : 0
  const raw = st.reasons.map((r) => [r.text, r.params?.detail].filter(Boolean).join('\n')).concat(st.note_reasons.map((r) => r.text)).join('\n')
  return (
    <div className={s.why}>
      <span className={s.whyMain}>
        {pr && failedSince > 0 && <>{t.testsFailedSince(failedSince)} </>}
        {stop(sentence(top, now))}
      </span>
      {rest.length > 0 && <span className={s.sub}>{t.also} {rest.map((r) => stop(sentence(r, now))).join(' ')}</span>}
      {st.note_reasons.length > 0 && <span className={s.sub}>{t.note} {st.note_reasons.map((r) => stop(sentence(r, now))).join(' ')}</span>}
      <SameCause id={v.vm.id} code={top.code} scope={top.scope} />
      <TechDetails summary={t.reasonDetails}>{raw}</TechDetails>
    </div>
  )
}

/** "Same cause on N other VMs", with the overview filtered to that cause (the VM table of the deciding code and scope). */
function SameCause({ id, code, scope }: { id: number; code: string; scope: Reason['scope'] }) {
  const t = useT().vm3
  const q = useSameCause(code, scope, true)
  if (!q.data) return null
  const self = q.data.rows.some((r) => r.vm_id === id) || q.data.total > q.data.rows.length
  const others = Math.max(0, q.data.total - (self ? 1 : 0))
  if (others === 0) return <span className={s.sub}>{t.onlyThis}</span>
  const hosts = [...new Set(q.data.rows.filter((r) => r.vm_id !== id).map((r) => r.host))]
  const where = scope ? t.inScope(scope.kind, scope.name) : t.onHosts(hosts.slice(0, 3), hosts.length > 3 || q.data.total > q.data.rows.length)
  return (
    <span className={s.sub}>
      {t.sameCause(others, where)}{' '}
      <Link to={`/${query({ code, scope_kind: scope?.kind, scope_id: scope?.id, show: 'all', tab: 'vms' })}`}>{t.seeAll(q.data.total)}</Link>
    </span>
  )
}

/** Proven RPO, newest backup, proven RTO and proof age, each against its target with a mini bar. */
function Metrics({ v }: { v: VMView }) {
  const t = useT().vm3
  const st = v.status
  const pol = v.policy
  return (
    <div className={s.metrics} role="group" aria-label={t.measures}>
      <Metric label={t.provenRpo} value={st.proven_rpo_s} text={dur(st.proven_rpo_s)} none={t.noValue.rpo} target={pol?.rpo_target_s ?? null} word={t.target} />
      <Metric label={t.newestBackup} value={st.theoretical_rpo_s} none={t.noValue.backup} target={pol?.rpo_target_s ?? null} word={t.target}
        text={st.theoretical_rpo_s !== null && st.theoretical_rpo_s < 60 ? ago(st.theoretical_rpo_s) : dur(st.theoretical_rpo_s)}
        suffix={st.theoretical_rpo_s !== null && st.theoretical_rpo_s >= 60 ? t.ago : undefined} />
      <Metric label={t.provenRto} value={st.proven_rto_s} text={dur(st.proven_rto_s, true)} none={t.noValue.rto} target={pol?.rto_target_s ?? null} word={t.target} />
      <Metric label={t.proofAge} value={st.proof_age_s} text={dur(st.proof_age_s)} none={t.noValue.age} target={pol?.max_proof_age_s ?? null} word={t.max} />
    </div>
  )
}

function Metric({ label, value, text, suffix, none, target, word }: {
  label: string
  value: number | null
  text: string
  suffix?: string
  none: string
  target: number | null
  word: (d: string) => string
}) {
  const t = useT().vm3
  const over = value !== null && target !== null && value > target
  // One scale for the value and its target: the longer of the two is the whole bar.
  const scale = Math.max(value ?? 0, target ?? 0) || 1
  return (
    <div className={s.metric}>
      <span className={s.sub}>{label}</span>
      <b className={value === null ? s.mnone : over ? s.bad : undefined}>{value === null ? none : text}{value !== null && suffix && <small>{suffix}</small>}</b>
      <span className={s.sub}>{target !== null ? word(dur(target)) : t.noTarget}</span>
      {target !== null && (
        <div className={s.mini} aria-hidden="true">
          {value !== null && <i className={over ? s.ibad : undefined} style={{ width: `${(100 * value) / scale}%` }} />}
          <s style={{ left: `calc(${(100 * target) / scale}% - 1px)` }} />
        </div>
      )}
    </div>
  )
}
