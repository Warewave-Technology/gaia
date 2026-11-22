import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, useLocation, useParams } from 'react-router'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { serverTime } from '../../lib/time'
import { query, useFilters } from '../../lib/url'
import { ApiError } from '../api/errors'
import { can, useMe } from '../api/session'
import { useReasonCodes, useSetTags, useVMAction, useVMRow, useVMView, type VMView } from '../api/views'
import { useAssignPolicy, usePolicyNames } from '../api/vm'
import { useStepUp } from '../layout/StepUp'
import { Failed, Loading } from '../layout/States'
import { Activity, Button, Notice, StatusChip, TabPanel, Tabs } from '../ui'
import { Answer } from './vm/Answer'
import { Evidence } from './vm/Evidence'
import { LatestTest } from './vm/LatestTest'
import { Points } from './vm/Points'
import { StatusHistory } from './vm/StatusHistory'
import { Strip } from './vm/Strip'
import s from './VMPage.module.css'

const KEYS = ['range', 'point', 'from', 'tab'] as const
export type VMFilters = Record<(typeof KEYS)[number], string>
export type SetVMFilters = (patch: Partial<VMFilters>) => void
const TABS = ['test', 'points', 'history', 'evidence'] as const

/**
 * One VM (docs/design/v3/VM3.dc.html, rules 2 and 6): the header, the answer band (where it returns to if it breaks
 * now, why its status is what it is, the four measures against their targets), the history strip, then the tabs
 * Latest test · Points · Status history · Evidence. Range, tab and a chosen point are in the URL.
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
  const t = useT().vm3
  const me = useMe().data
  const fmt = serverTime(v.clock)
  const st = v.status
  const [edit, setEdit] = useState<'tags' | 'policy' | null>(null)
  const tab = (TABS as readonly string[]).includes(f.tab) ? f.tab : f.point ? 'points' : 'test'
  const point = f.point ? Number(f.point) : null
  return (
    <main className={s.page}>
      <Crumbs v={v} from={f.from} />
      <section className={s.head} aria-labelledby="vm-name">
        <StatusChip status={st.status} size="lg" />
        <h1 id="vm-name" className={s.name}>{v.vm.name}</h1>
        <span className={s.sub}>{t.since(fmt.at(st.changed_at), dur(fmt.age(st.changed_at)))}</span>
        {can(me, 'act') && <Actions id={v.vm.id} onEdit={setEdit} />}
      </section>
      <Meta v={v} />
      {st.activity.length > 0 && <Activity jobs={st.activity} fmt={fmt} wide />}
      {edit === 'tags' && <TagsForm v={v} onDone={() => setEdit(null)} />}
      {edit === 'policy' && <PolicyForm v={v} onDone={() => setEdit(null)} />}
      <Answer v={v} fmt={fmt} />
      <Strip v={v} fmt={fmt} windowH={windowH} point={point}
        onRange={(r) => setF({ range: r === 168 ? '7d' : '' })}
        onPoint={(p) => setF({ point: p === null ? '' : String(p) })}
        onShowPoint={(p) => setF({ point: String(p), tab: 'points' })} />
      <section className={s.tabs}>
        <Tabs label={t.tabs} value={tab} onChange={(k) => setF({ tab: k === 'test' ? '' : k })}
          tabs={[
            { key: 'test', label: t.tab.test },
            { key: 'points', label: t.tab.points, count: v.points.length ? v.points.length.toLocaleString('en-US') : undefined },
            { key: 'history', label: t.tab.history, count: v.status_history.length ? v.status_history.length.toLocaleString('en-US') : undefined },
            { key: 'evidence', label: t.tab.evidence },
          ]} />
        <TabPanel id={tab}>
          {tab === 'test' && <LatestTest v={v} fmt={fmt} />}
          {tab === 'points' && <Points v={v} fmt={fmt} selected={point} />}
          {tab === 'history' && <StatusHistory v={v} fmt={fmt} />}
          {tab === 'evidence' && <Evidence v={v} fmt={fmt} />}
        </TabPanel>
      </section>
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

/** Host · policy with its targets · repository · tags; each a way to the VMs that share it. */
function Meta({ v }: { v: VMView }) {
  const t = useT().vm3
  const pol = v.policy
  const row = useVMRow(v.vm.id, v.host.id, v.vm.name)
  const tags = row.data?.tags ?? []
  const dot = <span className={s.dot} aria-hidden="true"> · </span>
  return (
    <p className={s.meta} aria-label={t.meta.label}>
      <Link className={s.mono} to={`/${query({ host: v.host.id, show: 'all', tab: 'vms' })}`}>{v.host.name}</Link>
      {dot}
      {pol ? (
        <span>
          <Link className={s.mono} to={`/${query({ policy: pol.id, show: 'all', tab: 'vms' })}`}>{pol.name}</Link>{' '}
          {t.meta.targets(dur(pol.rpo_target_s), dur(pol.rto_target_s), dur(pol.test_interval_s))}
        </span>
      ) : <b className={s.none}>{t.meta.noPolicy}</b>}
      {v.repository && <>{dot}<span>{t.meta.repository} <Link className={s.mono} to="/inventory/repositories">{v.repository.name}</Link></span></>}
      {dot}
      <span>
        {row.isPending ? '…' : tags.length === 0 ? <span className={s.muted}>{t.meta.noTags}</span> : tags.map((g, i) => (
          <span key={g}>{i > 0 && ', '}<Link className={s.mono} to={`/${query({ tag: g, show: 'all', tab: 'vms' })}`}>{g}</Link></span>
        ))}
      </span>
    </p>
  )
}

/** Back up now, test the newest point, and the rarer actions under More. */
function Actions({ id, onEdit }: { id: number; onEdit: (w: 'tags' | 'policy') => void }) {
  const t = useT().vm3
  const backup = useVMAction(id, 'backup')
  const test = useVMAction(id, 'test')
  const done = backup.data ?? test.data
  const err = backup.error ?? test.error
  return (
    <div className={s.acts}>
      <div className={s.btns}>
        <Button variant="quiet" onClick={() => backup.mutate()} disabled={backup.isPending}>{t.backupNow}</Button>
        <Button onClick={() => test.mutate()} disabled={test.isPending}>{t.testNow}</Button>
        <More onEdit={onEdit} />
      </div>
      {done && <span className={s.muted} role="status">{t.queued(done === backup.data ? t.backupNow : t.testNow, done.id)}</span>}
      {err && <Notice tone="critical">{err.message}</Notice>}
    </div>
  )
}

function More({ onEdit }: { onEdit: (w: 'tags' | 'policy') => void }) {
  const t = useT().vm3
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent ? e.key === 'Escape' : !ref.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', close)
    return () => {
      document.removeEventListener('mousedown', close)
      document.removeEventListener('keydown', close)
    }
  }, [open])
  const pick = (w: 'tags' | 'policy') => {
    setOpen(false)
    onEdit(w)
  }
  return (
    <div className={s.more} ref={ref}>
      <Button variant="quiet" aria-haspopup="menu" aria-expanded={open} aria-label={t.moreLabel} onClick={() => setOpen(!open)}>{t.more} ▾</Button>
      {open && (
        <div className={s.menu} role="menu">
          <button type="button" role="menuitem" onClick={() => pick('tags')}>{t.editTags}</button>
          <button type="button" role="menuitem" onClick={() => pick('policy')}>{t.changePolicy}</button>
        </div>
      )}
    </div>
  )
}

/** The VM's tags, replaced as a whole ("key" or "key:value", by spaces or commas). */
function TagsForm({ v, onDone }: { v: VMView; onDone: () => void }) {
  const t = useT().vm3
  const row = useVMRow(v.vm.id, v.host.id, v.vm.name)
  const set = useSetTags(v.vm.id)
  const [text, setText] = useState<string | null>(null)
  const value = text ?? (row.data?.tags ?? []).join(' ')
  const save = (e: FormEvent) => {
    e.preventDefault()
    set.mutate(value.split(/[\s,]+/).filter(Boolean), { onSuccess: onDone })
  }
  return (
    <form className={s.form} onSubmit={save}>
      <label>
        <span>{t.tags.label}</span>
        <input value={value} onChange={(e) => setText(e.target.value)} placeholder={t.tags.hint} autoFocus />
      </label>
      <Button type="submit" disabled={set.isPending || row.isPending}>{t.tags.save}</Button>
      <Button type="button" variant="quiet" onClick={onDone}>{t.tags.cancel}</Button>
      {set.error && <span role="alert" className={s.err}>{set.error.message}</span>}
    </form>
  )
}

/** Assign another policy (or none); the server asks for a step-up when the new one protects less. */
function PolicyForm({ v, onDone }: { v: VMView; onDone: () => void }) {
  const t = useT().vm3
  const ask = useStepUp()
  const pols = usePolicyNames(true)
  const [pick, setPick] = useState(String(v.policy?.id ?? 0))
  const m = useAssignPolicy(v.vm.id, ask, { title: t.policy.stepUp, facts: [{ label: 'VM', value: v.vm.name }] })
  const save = (e: FormEvent) => {
    e.preventDefault()
    m.mutate(Number(pick), { onSuccess: onDone })
  }
  return (
    <form className={s.form} onSubmit={save}>
      <label>
        <span>{t.policy.label}</span>
        <select value={pick} onChange={(e) => setPick(e.target.value)} disabled={pols.isPending}>
          <option value="0">{t.policy.none}</option>
          {(pols.data ?? []).map((p) => (
            <option key={p.id} value={p.id}>{`${p.name} ${t.meta.targets(dur(p.rpo_target_s), dur(p.rto_target_s), dur(p.test_interval_s))}`}</option>
          ))}
        </select>
      </label>
      <Button type="submit" disabled={m.isPending || pick === String(v.policy?.id ?? 0)}>{t.policy.save}</Button>
      <Button type="button" variant="quiet" onClick={onDone}>{t.policy.cancel}</Button>
      <span className={s.muted}>{t.policy.weaker}</span>
      {(m.error || pols.error) && <span role="alert" className={s.err}>{(m.error ?? pols.error)!.message}</span>}
    </form>
  )
}

