import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { useT } from '../../../i18n'
import { dur } from '../../../lib/duration'
import { can } from '../../access'
import {
  change, usePolicies, usePolicyCoverage, useRepositoryRows, useWhatIf,
  type CapacityView, type Policy, type PolicyChange, type PolicyInput, type PolicyPatch,
} from '../../api/inventory'
import type { User } from '../../api/session'
import { Failed, Loading } from '../../layout/States'
import { Button, Notice, PageHead, StatusBar, StatusChip, StatusCounts } from '../../ui'
import s from '../Inventory.module.css'
import { Confirm, Field, useAction } from './common'
import { parseDur } from './duration'

type T = ReturnType<typeof useT>['inventory']['policies']
const DURS = ['rpo_target_s', 'rto_target_s', 'max_proof_age_s', 'backup_interval_s', 'test_interval_s'] as const
const COUNTS = ['keep_last', 'keep_daily', 'keep_weekly'] as const
type Dk = (typeof DURS)[number]
type Ck = (typeof COUNTS)[number]
type Draft = Record<Dk | Ck, string> & { name: string; repository_id: string; check_template: string }

function label(k: Dk | Ck | 'name' | 'repository_id' | 'check_template', t: T) {
  const f = t.f
  return ({
    rpo_target_s: f.rpo, rto_target_s: f.rto, max_proof_age_s: f.age, backup_interval_s: f.backup, test_interval_s: f.test,
    keep_last: f.last, keep_daily: f.daily, keep_weekly: f.weekly, name: f.name, repository_id: f.repository, check_template: f.template,
  } as const)[k]
}

function draftOf(p: Policy | null): Draft {
  const d = (v: number | undefined) => (v ? dur(v) : '')
  return {
    name: p?.name ?? '', repository_id: p ? String(p.repository_id) : '', check_template: p?.check_template ?? '',
    rpo_target_s: d(p?.rpo_target_s), rto_target_s: d(p?.rto_target_s), max_proof_age_s: d(p?.max_proof_age_s),
    backup_interval_s: d(p?.backup_interval_s), test_interval_s: d(p?.test_interval_s),
    keep_last: p ? String(p.keep_last) : '', keep_daily: p ? String(p.keep_daily) : '', keep_weekly: p ? String(p.keep_weekly) : '',
  }
}

/** The draft as numbers, with the fields that are not valid named. */
function parse(d: Draft, t: T) {
  const errors: string[] = []
  const out: Partial<Record<Dk | Ck, number>> = {}
  for (const k of DURS) {
    const v = parseDur(d[k])
    if (v === null) errors.push(d[k].trim() ? t.badDur(label(k, t)) : t.missing(label(k, t)))
    else out[k] = v
  }
  for (const k of COUNTS) {
    if (!/^\d+$/.test(d[k].trim())) errors.push(d[k].trim() ? t.badCount(label(k, t)) : t.missing(label(k, t)))
    else out[k] = Number(d[k])
  }
  if (!d.check_template.trim()) errors.push(t.missing(label('check_template', t)))
  return { errors, v: out as Record<Dk | Ck, number> }
}

export function Policies({ me }: { me: User }) {
  const t = useT().inventory
  const q = usePolicies()
  const cov = usePolicyCoverage()
  const repos = useRepositoryRows(200)
  const act = useAction()
  const writer = can(me, 'act')
  const [open, setOpen] = useState<number | 'new' | null>(null)
  // every policy's VMs: the coverage view pages its groups; an installation has tens of policies, so all pages are read
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = cov
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) fetchNextPage()
  }, [hasNextPage, isFetchingNextPage, fetchNextPage])
  const groups = useMemo(() => new Map(cov.rows.map((g) => [g.id, g])), [cov.rows])
  const repoName = useMemo(() => new Map(repos.rows.map((r) => [r.id, r.name])), [repos.rows])
  const templates = useMemo(() => [...new Set((q.data ?? []).map((p) => p.check_template))].sort(), [q.data])

  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const ps = q.data
  const p = t.policies
  const cols = writer ? 7 : 6
  return (
    <>
      <PageHead question={p.question} answer={p.answer(ps.length)} lede={<p>{p.hint}</p>} />
      <section className={s.sec}>
        {writer && (
          <div className={s.bar}>
            <Button className={s.end} variant={open === 'new' ? 'quiet' : 'primary'} onClick={() => setOpen(open === 'new' ? null : 'new')}>
              {open === 'new' ? t.cancel : p.add}
            </Button>
          </div>
        )}
        {open === 'new' && (
          <PolicyForm policy={null} repos={repos.rows.map((r) => [r.id, r.name] as const)} templates={templates} busy={act.busy === 'create'}
            onSave={async (body) => {
              const r = await act.run('create', 'POST', '/api/v1/policies', (h) => change.addPolicy(body as PolicyInput, h), (x) => p.created(x.name))
              if (r) setOpen(null)
            }} />
        )}
        <div className={s.scroll}>
          <table className={s.tbl}>
            <thead>
              <tr>
                <th scope="col">{p.cols.policy}</th>
                <th scope="col">{p.cols.targets}</th>
                <th scope="col">{p.cols.cadence}</th>
                <th scope="col">{p.cols.keep}</th>
                <th scope="col">{p.cols.vms}</th>
                <th scope="col">{p.cols.checks}</th>
                {writer && <th scope="col"><span className={s.vh}>{t.action}</span></th>}
              </tr>
            </thead>
            <tbody>
              {ps.map((x) => {
                const g = groups.get(x.id)
                const vms = g?.vms ?? 0
                const isOpen = open === x.id
                return (
                  <PolicyRows key={x.id} x={x} vms={vms} counts={g?.by_status} repo={repoName.get(x.repository_id)} isOpen={isOpen} cols={cols} writer={writer}
                    actions={writer && (
                      <span className={s.acts}>
                        <Button variant="quiet" aria-expanded={isOpen} onClick={() => setOpen(isOpen ? null : x.id)}>{isOpen ? p.close : p.edit}</Button>
                        {vms === 0 && (
                          <Confirm label={p.remove} question={p.removeConfirm(x.name)} busy={act.busy === `remove/${x.id}`}
                            onYes={() => act.run(`remove/${x.id}`, 'DELETE', `/api/v1/policies/${x.id}`, (h) => change.removePolicy(x.id, h))} />
                        )}
                      </span>
                    )}
                    form={isOpen && (
                      <PolicyForm policy={x} repos={repos.rows.map((r) => [r.id, r.name] as const)} templates={templates} busy={act.busy === `save/${x.id}`}
                        onSave={async (patch) => {
                          const r = await act.run(`save/${x.id}`, 'PATCH', `/api/v1/policies/${x.id}`, (h) => change.patchPolicy(x.id, patch, h), () => p.saved(x.name))
                          if (r) setOpen(null)
                        }} />
                    )} />
                )
              })}
            </tbody>
          </table>
        </div>
        {act.outcome}
        {writer && <p className={s.foot}>{p.foot}</p>}
      </section>
    </>
  )
}

function PolicyRows({ x, vms, counts, repo, isOpen, cols, writer, actions, form }: {
  x: Policy
  vms: number
  counts: Record<string, number> | undefined
  repo: string | undefined
  isOpen: boolean
  cols: number
  writer: boolean
  actions: ReactNode
  form: ReactNode
}) {
  const p = useT().inventory.policies
  return (
    <>
      <tr className={isOpen ? s.open : ''}>
        <th scope="row">
          <span className={s.name}>{x.name}</span>
          <span className={s.of}>{p.repository(repo ?? String(x.repository_id))}</span>
        </th>
        <td className={s.fg2}>{p.targets(dur(x.rpo_target_s), dur(x.rto_target_s), dur(x.max_proof_age_s))}</td>
        <td className={s.fg2}>{p.cadence(dur(x.backup_interval_s), dur(x.test_interval_s))}</td>
        <td className={s.fg2}>{p.keep(x.keep_last, x.keep_daily, x.keep_weekly)}</td>
        <td className={s.mid}>
          {vms === 0 || !counts ? <span className={s.of}>{p.noVMs}</span> : (
            <>
              <b className={s.fig}>{vms.toLocaleString('en-US')}</b>
              <StatusBar counts={counts} />
              <StatusCounts counts={counts} />
            </>
          )}
        </td>
        <td className={`${s.mono} ${s.nowrap}`}>{x.check_template}</td>
        {writer && <td>{actions}</td>}
      </tr>
      {form && (
        <tr className={s.detail}>
          <td colSpan={cols}>{form}</td>
        </tr>
      )}
    </>
  )
}

/** The fields of a policy that loosen protection when they grow (targets, intervals) or shrink (points kept). */
function looser(p: Policy, v: Record<Dk | Ck, number>, template: string) {
  return DURS.some((k) => v[k] > p[k]) || COUNTS.some((k) => v[k] < p[k]) || template !== p.check_template
}

/**
 * A new policy, or a policy's change. A change shows its effect on proof capacity first (POST /capacity/what-if with
 * the new test interval and max proof age; nothing is saved), then saves only what changed; the server asks for a
 * step-up when the change loosens a target (ADR-008 §4).
 */
function PolicyForm({ policy, repos, templates, busy, onSave }: {
  policy: Policy | null
  repos: readonly (readonly [number, string])[]
  templates: string[]
  busy: boolean
  onSave: (body: PolicyPatch | PolicyInput) => void
}) {
  const t = useT().inventory
  const p = t.policies
  const [d, setD] = useState<Draft>(() => draftOf(policy))
  const [tried, setTried] = useState(false)
  const set = (k: keyof Draft) => (e: { target: { value: string } }) => setD((o) => ({ ...o, [k]: e.target.value }))
  const { errors, v } = parse(d, p)
  if (!policy) {
    if (!d.name.trim()) errors.push(p.missing(p.f.name))
    if (!d.repository_id) errors.push(p.missing(p.f.repository))
  }
  const valid = errors.length === 0
  // what changed, for the PATCH and the what-if
  const patch: PolicyPatch = {}
  if (policy && valid) {
    for (const k of [...DURS, ...COUNTS]) if (v[k] !== policy[k]) patch[k] = v[k]
    if (d.check_template.trim() !== policy.check_template) patch.check_template = d.check_template.trim()
    if (Number(d.repository_id) !== policy.repository_id) patch.repository_id = Number(d.repository_id)
  }
  const changed = Object.keys(patch).length > 0
  const ti = parseDur(d.test_interval_s)
  const age = parseDur(d.max_proof_age_s)
  const wi: PolicyChange | null = policy && ti && age && (ti !== policy.test_interval_s || age !== policy.max_proof_age_s)
    ? { policy_id: policy.id, test_interval_s: ti, max_proof_age_s: age }
    : null
  function submit(e: FormEvent) {
    e.preventDefault()
    setTried(true)
    if (!valid) return
    if (policy) {
      if (changed) onSave(patch)
      return
    }
    onSave({ name: d.name.trim(), repository_id: Number(d.repository_id), check_template: d.check_template.trim(), ...v })
  }
  return (
    <form className={s.form} onSubmit={submit} noValidate>
      {!policy && <h3>{p.add}</h3>}
      <div className={s.fields}>
        {!policy && <Field label={p.f.name}><input value={d.name} onChange={set('name')} autoFocus /></Field>}
        <Field label={p.f.repository}>
          <select value={d.repository_id} onChange={set('repository_id')}>
            {!policy && <option value="">—</option>}
            {repos.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
          </select>
        </Field>
        {DURS.map((k) => <Field key={k} label={label(k, p)} hint={p.durHint}><input value={d[k]} onChange={set(k)} inputMode="text" /></Field>)}
        {COUNTS.map((k) => <Field key={k} label={label(k, p)}><input value={d[k]} onChange={set(k)} inputMode="numeric" /></Field>)}
        <Field label={p.f.template} hint={p.templateHint}>
          <input value={d.check_template} onChange={set('check_template')} list="gaia-templates" />
        </Field>
      </div>
      <datalist id="gaia-templates">{templates.map((x) => <option key={x} value={x} />)}</datalist>
      {policy && valid && <Effect change={wi} />}
      {errors.length > 0 && (tried || policy) && <p className={s.hint} role="status">{errors.join(' ')}</p>}
      {policy && valid && changed && looser(policy, v, d.check_template.trim()) && <p className={s.hint}>{p.looser}</p>}
      <div className={s.buttons}>
        <Button type="submit" disabled={busy || !valid || (!!policy && !changed)}>{busy ? t.working : policy ? p.save : p.create}</Button>
        {policy && !changed && <span className={s.hint}>{p.unchanged}</span>}
      </div>
    </form>
  )
}

function testsPerDay(c: CapacityView) {
  return Math.round(c.by_os_class.reduce((n, g) => n + g.tests_per_day, 0))
}

/** Proof capacity now and after the change, side by side. */
function Effect({ change: wi }: { change: PolicyChange | null }) {
  const p = useT().inventory.policies
  const { base, after } = useWhatIf(wi)
  if (!wi) return <div className={s.effect}><h4>{p.effect}</h4><p className={s.hint}>{p.effectNone}</p></div>
  if (base.isError || after.isError) return <div className={s.effect}><h4>{p.effect}</h4><Notice tone="warning">{p.effectFailed}</Notice></div>
  const b = base.data
  const a = after.data
  const cell = (c: CapacityView | undefined, f: (c: CapacityView) => ReactNode) => <td>{c ? f(c) : '…'}</td>
  const cover = (c: CapacityView) => (
    <span className={s.inline}>
      <StatusChip status={c.coverage >= 1 ? 'OK' : 'WARNING'} word={c.coverage >= 1 ? p.word.covered : p.word.behind} size="xs" />
      {p.coverageVal(c.coverage)}
    </span>
  )
  return (
    <div className={s.effect}>
      <h4>{p.effect}</h4>
      <p className={s.hint}>{p.effectHint}</p>
      <table>
        <thead>
          <tr><th scope="col"><span className={s.vh}>{p.effect}</span></th><th scope="col">{p.now}</th><th scope="col">{p.after}</th></tr>
        </thead>
        <tbody>
          <tr><th scope="row">{p.coverage}</th>{cell(b, cover)}{cell(a, cover)}</tr>
          <tr><th scope="row">{p.slots}</th>{cell(b, (c) => p.slotsVal(c.slots_needed, c.slots))}{cell(a, (c) => p.slotsVal(c.slots_needed, c.slots))}</tr>
          <tr><th scope="row">{p.tests}</th>{cell(b, (c) => testsPerDay(c).toLocaleString('en-US'))}{cell(a, (c) => testsPerDay(c).toLocaleString('en-US'))}</tr>
          <tr><th scope="row">{p.expiring}</th>{cell(b, (c) => c.expiring_total.toLocaleString('en-US'))}{cell(a, (c) => c.expiring_total.toLocaleString('en-US'))}</tr>
        </tbody>
      </table>
      {a && a.coverage < 1 && <Notice tone="warning">{p.behind}</Notice>}
    </div>
  )
}
