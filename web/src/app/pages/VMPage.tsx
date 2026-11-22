import { useState, type FormEvent } from 'react'
import { Link, useLocation, useParams } from 'react-router'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { secondsBetween, serverTime, type Fmt } from '../../lib/time'
import { query, useFilters } from '../../lib/url'
import { ApiError } from '../api/errors'
import { can, useMe } from '../api/session'
import { useReasonCodes, useSameCause, useSetTags, useVMAction, useVMRow, useVMView, type VMView } from '../api/views'
import { Failed, Loading } from '../layout/States'
import { Activity, Button, Notice, ProofBar, StatusChip, asStatus } from '../ui'
import { detailOf, when, type VMStrings } from './vm/common'
import { History } from './vm/History'
import { LatestTest } from './vm/LatestTest'
import { Points, StatusHistory } from './vm/Points'
import s from './VMPage.module.css'

const KEYS = ['range', 'point', 'from'] as const
export type VMFilters = Record<(typeof KEYS)[number], string>
export type SetVMFilters = (patch: Partial<VMFilters>) => void

/**
 * One VM (docs/design/v2/VM.dc.html): where it returns to if it breaks now and the test that proves it, why its status
 * is what it is and whether other VMs share the cause, the four measures against their targets, the history with a
 * point to look at, the latest test and every point with its proof.
 */
export function VMPage() {
  const id = Number(useParams().id)
  const [f, setF] = useFilters(KEYS)
  const windowH = f.range === '7d' ? 168 : 24
  const q = useVMView(id, windowH)
  const t = useT()
  if (q.isPending) return <Loading />
  if (q.isError) {
    if (q.error instanceof ApiError && q.error.status === 404) return <main className={s.page}><Notice tone="warning">{t.vm.notFound}</Notice></main>
    return <Failed error={q.error} onRetry={() => q.refetch()} />
  }
  return <VMBody v={q.data} f={f} setF={setF} windowH={windowH} />
}

function VMBody({ v, f, setF, windowH }: { v: VMView; f: VMFilters; setF: SetVMFilters; windowH: 24 | 168 }) {
  const t = useT().vm
  const me = useMe().data
  const fmt = serverTime(v.clock)
  const st = v.status
  return (
    <main className={s.page}>
      <Crumbs v={v} from={f.from} />
      <section className={s.head}>
        <div className={s.idcol}>
          <div className={s.stline}>
            <StatusChip status={st.status} size="lg" />
            <span className={s.sub}>{t.since(when(fmt, v.clock, st.changed_at, t), dur(secondsBetween(st.changed_at, v.clock.now)))}</span>
          </div>
          <h1 className={s.name}>{v.vm.name}</h1>
          <Where v={v} write={can(me, 'act')} />
          <Activity jobs={st.activity} fmt={fmt} wide />
        </div>
        {can(me, 'act') && <Actions id={v.vm.id} />}
      </section>
      <Answer v={v} fmt={fmt} />
      <Bullets v={v} />
      <History v={v} fmt={fmt} f={f} setF={setF} windowH={windowH} />
      <div className={s.two}>
        <LatestTest v={v} fmt={fmt} />
        <Points v={v} fmt={fmt} selected={f.point ? Number(f.point) : null} onSelect={(id) => setF({ point: String(id) })} />
      </div>
      <StatusHistory v={v} fmt={fmt} />
    </main>
  )
}

/** Where the user came from: a link with `state.from`, or `?from=`; a path of this app only. */
function useFrom(param: string): string | null {
  const loc = useLocation()
  const st = loc.state as { from?: unknown } | null
  const from = typeof st?.from === 'string' ? st.from : param
  return from && from.startsWith('/') && !from.startsWith('//') ? from : null
}

/** The breadcrumb keeps the place one came from: a filtered overview stays filtered, its filter named. */
function Crumbs({ v, from: param }: { v: VMView; from: string }) {
  const tt = useT()
  const t = tt.vm
  const from = useFrom(param)
  const codes = useReasonCodes().data
  const crumbs: { to: string; label: string }[] = []
  const u = from ? new URL(from, 'http://gaia') : null
  if (!u || u.pathname === '/') {
    crumbs.push({ to: '/', label: tt.nav.overview })
    if (u && from && u.search) {
      const p = u.searchParams
      const code = p.get('code')
      const cause = code ? codes?.codes.find((c) => c.code === code)?.cause ?? code : null
      const label = cause ?? (p.get('host') === String(v.host.id) ? t.crumb.host(v.host.name)
        : v.policy && p.get('policy') === String(v.policy.id) ? t.crumb.policy(v.policy.name)
          : p.get('tag') ? t.crumb.tag(p.get('tag')!) : p.get('status') ? t.crumb.status(p.get('status')!) : p.get('q') ? t.crumb.search(p.get('q')!) : t.crumb.filtered)
      crumbs.push({ to: from, label })
    }
  } else {
    const page = u.pathname.split('/')[1] as keyof typeof tt.nav
    crumbs.push({ to: from!, label: typeof tt.nav[page] === 'string' ? tt.nav[page] : tt.nav.overview })
  }
  return (
    <nav className={s.crumbs} aria-label={t.crumbs}>
      {crumbs.map((c) => <span key={c.to}><Link to={c.to}>{c.label}</Link><span aria-hidden="true"> / </span></span>)}
      <span className={s.here} aria-current="page">{v.vm.name}</span>
    </nav>
  )
}

/** Host, policy with its targets, repository and tags; each a way to the VMs that share it. */
function Where({ v, write }: { v: VMView; write: boolean }) {
  const t = useT().vm
  const pol = v.policy
  return (
    <p className={s.where}>
      {t.host} <Link className={s.mono} to={`/${query({ host: v.host.id, show: 'all' })}#vms`}>{v.host.name}</Link>
      <span className={s.dot}> · </span>
      {pol ? (
        <>
          {t.policy} <Link className={s.mono} to={`/${query({ policy: pol.id, show: 'all' })}#vms`}>{pol.name}</Link>{' '}
          {t.targets(dur(pol.rpo_target_s), dur(pol.rto_target_s), dur(pol.backup_interval_s), dur(pol.test_interval_s))}
        </>
      ) : <b className={s.none}>{t.noPolicy}</b>}
      {v.repository && <><span className={s.dot}> · </span>{t.repository} <Link className={s.mono} to="/inventory/repositories">{v.repository.name}</Link></>}
      <span className={s.dot}> · </span>
      <Tags v={v} write={write} />
    </p>
  )
}

/** The VM's tags, from its row of the VM table; writers replace them ("key" or "key:value", by spaces or commas). */
function Tags({ v, write }: { v: VMView; write: boolean }) {
  const t = useT().vm
  const row = useVMRow(v.vm.id, v.host.id, v.vm.name)
  const set = useSetTags(v.vm.id)
  const [edit, setEdit] = useState<string | null>(null)
  const tags = row.data?.tags ?? []
  if (edit !== null) {
    const save = (e: FormEvent) => {
      e.preventDefault()
      set.mutate(edit.split(/[\s,]+/).filter(Boolean), { onSuccess: () => setEdit(null) })
    }
    return (
      <form className={s.tagform} onSubmit={save}>
        <label>
          <span className={s.vh}>{t.tagsLabel}</span>
          <input value={edit} onChange={(e) => setEdit(e.target.value)} placeholder={t.tagsHint} autoFocus />
        </label>
        <Button type="submit" disabled={set.isPending}>{t.save}</Button>
        <Button type="button" variant="quiet" onClick={() => setEdit(null)}>{t.cancel}</Button>
        {set.error && <span role="alert" className={s.err}>{set.error.message}</span>}
      </form>
    )
  }
  return (
    <>
      {t.tags}{' '}
      {row.isPending ? '…' : tags.length === 0 ? <span className={s.muted}>{t.noTags}</span> : tags.map((g, i) => (
        <span key={g}>{i > 0 && ', '}<Link className={s.mono} to={`/${query({ tag: g, show: 'all' })}#vms`}>{g}</Link></span>
      ))}
      {write && row.data && <button type="button" className={s.link} onClick={() => setEdit(tags.join(' '))}>{t.editTags}</button>}
    </>
  )
}

function Actions({ id }: { id: number }) {
  const t = useT().vm
  const backup = useVMAction(id, 'backup')
  const test = useVMAction(id, 'test')
  const done = backup.data ?? test.data
  const err = backup.error ?? test.error
  return (
    <div className={s.acts}>
      <div className={s.btns}>
        <Button variant="quiet" onClick={() => backup.mutate()} disabled={backup.isPending}>{t.backupNow}</Button>
        <Button onClick={() => test.mutate()} disabled={test.isPending}>{t.testNow}</Button>
      </div>
      {done && <span className={s.muted} role="status">{t.queued(done === backup.data ? t.backupNow : t.testNow, done.id)}</span>}
      {err && <Notice tone="critical">{err.message}</Notice>}
    </div>
  )
}

/** "If it breaks now, you return to", and why the status is what it is, with the VMs that share the cause. */
function Answer({ v, fmt }: { v: VMView; fmt: Fmt }) {
  const t = useT().vm
  const st = v.status
  const pr = st.proven
  const test = pr ? v.tests.find((x) => x.id === pr.test_id) : undefined
  const checks = test ? detailOf(test).checks ?? [] : []
  const boot = checks.find((c) => c.name === 'boot')
  const passed = checks.filter((c) => c.verdict === 'OK').length
  return (
    <section className={`${s.answer} ${s[`a${asStatus(st.status)}`]}`} aria-label={t.returnTo}>
      <div className={s.acol}>
        <span className={s.lbl}>{t.returnTo}</span>
        {pr ? (
          <>
            <div className={s.ret}>{when(fmt, v.clock, pr.point_taken_at, t)} <small>{t.pointN(pr.point_id)}</small></div>
            <p className={s.atext}>
              {t.provenBy(pr.test_id, fmt.hhmm(pr.test_finished_at))}
              {test && checks.length > 0 && <>: {t.provenWhat(boot ? dur(boot.seconds, true) : null, passed, checks.length)}</>}.{' '}
              {st.proven_rto_s !== null && <>{t.backIn} <b>{dur(st.proven_rto_s, true)}</b>.</>}
            </p>
          </>
        ) : (
          <>
            <div className={`${s.ret} ${s.nothing}`}>{t.nothing}</div>
            <p className={s.atext}>{t.nothingWhat}</p>
          </>
        )}
      </div>
      <Why v={v} />
    </section>
  )
}

function Why({ v }: { v: VMView }) {
  const t = useT().vm
  const st = v.status
  const top = st.reasons[0]
  return (
    <div className={s.acol}>
      <span className={s.lbl}>{t.why(asStatus(st.status))}</span>
      <ul className={s.reasons}>
        {st.reasons.map((x, i) => <li key={i}><StatusChip status={x.status} size="xs" /><span>{x.text}</span></li>)}
        {st.note_reasons.map((x, i) => <li key={`n${i}`}><span className={s.nl}>{t.note}</span><span>{x.text}</span></li>)}
      </ul>
      {top && asStatus(st.status) !== 'OK' && <SameCause id={v.vm.id} code={top.code} scope={top.scope} />}
    </div>
  )
}

/** "Same cause on N other VMs": the VM table with this VM's deciding code and scope. */
function SameCause({ id, code, scope }: { id: number; code: string; scope: VMView['status']['reasons'][number]['scope'] }) {
  const t = useT().vm
  const q = useSameCause(code, scope, true)
  if (!q.data) return null
  const self = q.data.rows.some((r) => r.vm_id === id) || q.data.total > q.data.rows.length
  const others = Math.max(0, q.data.total - (self ? 1 : 0))
  if (others === 0) return <p className={s.cause}>{t.onlyThis}</p>
  const hosts = [...new Set(q.data.rows.filter((r) => r.vm_id !== id).map((r) => r.host))]
  const where = scope ? t.inScope(scope.kind, scope.name) : t.onHosts(hosts.slice(0, 3), hosts.length > 3 || q.data.total > q.data.rows.length)
  return (
    <p className={s.cause}>
      {t.sameCause(others)} {where}{' '}
      <Link to={`/${query({ code, scope_kind: scope?.kind, scope_id: scope?.id, show: 'all' })}#vms`}>{t.seeGroup}</Link>
    </p>
  )
}

/** Proven RPO, Theoretical RPO, Proven RTO and proof age, each against its target. */
function Bullets({ v }: { v: VMView }) {
  const t = useT().vm
  const st = v.status
  const pol = v.policy
  const pr = st.proven
  const test = pr ? v.tests.find((x) => x.id === pr.test_id) : undefined
  const restore = test ? detailOf(test).restore_s ?? null : null
  const rtoNote = test && restore !== null && st.proven_rto_s !== null
    ? t.rtoNote(dur(restore, true), dur(st.proven_rto_s - restore, true), test.id) : t.rtoNoteNone
  return (
    <section className={s.bullets} aria-label={t.measures}>
      <Bullet label={t.provenRpo} value={st.proven_rpo_s} target={pol?.rpo_target_s ?? null} word={t.target} note={t.provenRpoNote} />
      <Bullet label={t.theoreticalRpo} value={st.theoretical_rpo_s} target={pol?.rpo_target_s ?? null} word={t.target} note={t.theoreticalRpoNote} />
      <Bullet label={t.provenRto} value={st.proven_rto_s} target={pol?.rto_target_s ?? null} word={t.target} note={rtoNote} />
      <Bullet label={t.proofAge} value={st.proof_age_s} target={pol?.max_proof_age_s ?? null} word={t.max} note={t.proofAgeNote} />
    </section>
  )
}

function Bullet({ label, value, target, word, note }: {
  label: string
  value: number | null
  target: number | null
  word: VMStrings['target']
  note: string
}) {
  const t = useT().vm
  const over = value !== null && target !== null && value > target
  return (
    <div className={s.bul}>
      <span className={s.lbl}>{label}</span>
      <span className={s.bv}>
        <b className={over ? s.bad : ''}>{dur(value, true)}</b>
        <span className={s.of}>{target !== null ? word(dur(target)) : t.noTarget}</span>
      </span>
      {target !== null && <ProofBar value={value} target={target} width={280} label={label} />}
      <span className={s.of}>{note}</span>
    </div>
  )
}
