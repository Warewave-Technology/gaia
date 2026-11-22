import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { Link } from 'react-router'
import { useT } from '../../../i18n'
import { dur } from '../../../lib/duration'
import { serverTime, type Fmt } from '../../../lib/time'
import { query } from '../../../lib/url'
import { can } from '../../access'
import {
  change, usePolicies, usePolicyCoverage, useRepositoryRows, useWhatIf,
  type CapacityView, type CoverageGroup, type PolicyChange, type PolicyInput, type PolicyPatch, type PolicyView, type WhatIfView,
} from '../../api/inventory'
import type { User } from '../../api/session'
import { useSystem } from '../../api/views'
import { Failed, Loading } from '../../layout/States'
import { Button, by, DataTable, Notice, PageHead, StatusBar, StatusChip, StatusCounts, Toolbar, type Column } from '../../ui'
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

function draftOf(p: PolicyView | null): Draft {
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

/** ADR-007 C12's share when the server sends it; undefined when it does not (the screen then says nothing). */
const shareOf = (p: { max_ok_fraction?: number | null }) => (typeof p.max_ok_fraction === 'number' ? p.max_ok_fraction : undefined)

/**
 * The policies: a short list, so the data-table holds them all and sorts them itself. A row opens to the policy's
 * facts and, for writers, its editor; the editor shows the effect on proof capacity and, per ADR-007 C12, how much
 * of the day a VM on it can be OK at best, before anything is saved (saving stays allowed).
 */
export function Policies({ me }: { me: User }) {
  const t = useT().inventory
  const p = t.policies
  const q = usePolicies()
  const cov = usePolicyCoverage()
  const repos = useRepositoryRows(200)
  const sys = useSystem().data
  const act = useAction()
  const writer = can(me, 'act')
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<number | null>(null)
  // every policy's VMs: the coverage view pages its groups; an installation has tens of policies, so all pages are read
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = cov
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) fetchNextPage()
  }, [hasNextPage, isFetchingNextPage, fetchNextPage])
  const groups = useMemo(() => new Map(cov.rows.map((g) => [g.id, g])), [cov.rows])
  const repoName = useMemo(() => new Map(repos.rows.map((r) => [r.id, r.name])), [repos.rows])
  const templates = useMemo(() => [...new Set((q.data ?? []).map((x) => x.check_template))].sort(), [q.data])

  if (q.isPending || !sys) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const fmt = serverTime(sys.clock)
  const ps = q.data
  const known = ps.filter((x) => shareOf(x) !== undefined)
  const partial = known.filter((x) => (shareOf(x) ?? 1) < 1).length
  const headline = known.length > 0 && ps.length > 0 ? `${p.answer(ps.length)} · ${p.partial(partial)}` : p.answer(ps.length)
  const repoList = repos.rows.map((r) => [r.id, r.name] as const)

  const cols: Column<PolicyView>[] = [
    { key: 'name', header: p.cols.policy, width: '140px', compare: by((x) => x.name), cell: (x) => <span className={s.name}>{x.name}</span> },
    {
      key: 'targets', header: p.cols.targets, compare: by((x) => x.rpo_target_s),
      title: (x) => p.targetsLong(dur(x.rpo_target_s), dur(x.rto_target_s), dur(x.max_proof_age_s)),
      cell: (x) => p.targets(dur(x.rpo_target_s), dur(x.rto_target_s), dur(x.max_proof_age_s)),
    },
    { key: 'cadence', header: p.cols.cadence, width: '165px', compare: by((x) => x.test_interval_s), cell: (x) => p.cadence(dur(x.backup_interval_s), dur(x.test_interval_s)) },
  ]
  if (known.length > 0) {
    cols.push({
      key: 'ok', header: p.cols.ok, width: '140px', compare: by((x) => shareOf(x) ?? null),
      title: (x) => { const f = shareOf(x); return f === undefined ? undefined : p.okSentence(f, x.suggested_test_interval_s) },
      cell: (x) => {
        const f = shareOf(x)
        if (f === undefined) return ''
        return <span className={f < 1 ? s.warnText : s.fg2}>{f < 1 ? `▲ ${p.okCell(f)}` : p.okCell(f)}</span>
      },
    })
  }
  cols.push(
    {
      key: 'vms', header: p.cols.vms, width: known.length > 0 ? '290px' : '330px', compare: by((x) => groups.get(x.id)?.vms ?? 0),
      cell: (x) => <VMs g={groups.get(x.id)} none={p.noVMs} />,
    },
    { key: 'checks', header: p.cols.checks, width: '115px', title: (x) => x.check_template, cell: (x) => <span className={s.mono}>{x.check_template}</span> },
  )
  if (writer) {
    cols.push({
      key: 'action', header: p.cols.action, width: '76px',
      cell: (x, ctx) => (
        <Button variant="quiet" className={s.cellBtn} aria-expanded={ctx.open && editing === x.id}
          onClick={() => { setEditing(x.id); if (!ctx.open) ctx.toggle() }}>{p.edit}</Button>
      ),
    })
  }
  return (
    <>
      <PageHead question={p.question} answer={headline} lede={<p>{p.hint}</p>} />
      <section className={s.sec}>
        {writer && (
          <Toolbar end={<Button variant={adding ? 'quiet' : 'primary'} onClick={() => setAdding(!adding)}>{adding ? t.cancel : p.add}</Button>}>{null}</Toolbar>
        )}
        {adding && (
          <PolicyForm policy={null} repos={repoList} templates={templates} busy={act.busy === 'create'} onClose={() => setAdding(false)}
            onSave={async (body) => {
              const r = await act.run('create', 'POST', '/api/v1/policies', (h) => change.addPolicy(body as PolicyInput, h), (x) => p.created(x.name))
              if (r) setAdding(false)
            }} />
        )}
        <DataTable<PolicyView>
          caption={p.caption}
          columns={cols}
          rows={ps}
          rowKey={(x) => String(x.id)}
          minWidth={1060}
          height="640px"
          detail={(x) => (editing === x.id && writer
            ? <PolicyForm policy={x} repos={repoList} templates={templates} busy={act.busy === `save/${x.id}`} onClose={() => setEditing(null)}
              onSave={async (patch) => {
                const r = await act.run(`save/${x.id}`, 'PATCH', `/api/v1/policies/${x.id}`, (h) => change.patchPolicy(x.id, patch, h), () => p.saved(x.name))
                if (r) setEditing(null)
              }} />
            : <PolicyFacts x={x} g={groups.get(x.id)} repo={repoName.get(x.repository_id)} fmt={fmt} writer={writer} busy={act.busy}
              onEdit={() => setEditing(x.id)}
              onRemove={() => act.run(`remove/${x.id}`, 'DELETE', `/api/v1/policies/${x.id}`, (h) => change.removePolicy(x.id, h))} />)}
        />
        {act.outcome}
        {writer && <p className={s.foot}>{p.foot}</p>}
      </section>
    </>
  )
}

/** A group's VMs as a count, a bar and the words beside it (the bar never says a status alone). */
function VMs({ g, none }: { g: CoverageGroup | undefined; none: string }) {
  if (!g || g.vms === 0) return <span className={s.cellOf}>{none}</span>
  return (
    <span className={s.vmsCell}>
      <b className={s.fig}>{g.vms.toLocaleString('en-US')}</b>
      <StatusBar counts={g.by_status} />
      <StatusCounts counts={g.by_status} />
    </span>
  )
}

function PolicyFacts({ x, g, repo, fmt, writer, busy, onEdit, onRemove }: {
  x: PolicyView
  g: CoverageGroup | undefined
  repo: string | undefined
  fmt: Fmt
  writer: boolean
  busy: string | null
  onEdit: () => void
  onRemove: () => void
}) {
  const p = useT().inventory.policies
  const f = shareOf(x)
  const vms = g?.vms ?? 0
  return (
    <div className={s.facts}>
      <p>{p.targetsLong(dur(x.rpo_target_s), dur(x.rto_target_s), dur(x.max_proof_age_s))} {p.cadenceLong(dur(x.backup_interval_s), dur(x.test_interval_s))}</p>
      {f !== undefined && <p className={f < 1 ? s.warnText : undefined}>{p.okSentence(f, x.suggested_test_interval_s)}</p>}
      <p>{p.keep(x.keep_last, x.keep_daily, x.keep_weekly)} {p.repository(repo ?? String(x.repository_id))}</p>
      <p className={s.cellOf}>{p.times(fmt.at(x.created_at), fmt.at(x.updated_at))}</p>
      <span className={s.inline}>
        {vms > 0 && <Link to={`/${query({ show: 'all', policy: x.id })}`}>{p.showVMs}</Link>}
        {writer && <Button variant="quiet" onClick={onEdit}>{p.edit}</Button>}
        {writer && vms === 0 && <Confirm label={p.remove} question={p.removeConfirm(x.name)} busy={busy === `remove/${x.id}`} onYes={onRemove} />}
      </span>
    </div>
  )
}

/** The fields of a policy that loosen protection when they grow (targets, intervals) or shrink (points kept). */
function looser(p: PolicyView, v: Record<Dk | Ck, number>, template: string) {
  return DURS.some((k) => v[k] > p[k]) || COUNTS.some((k) => v[k] < p[k]) || template !== p.check_template
}

/**
 * A new policy, or a policy's change. A change shows its effect first (POST /capacity/what-if with the new test
 * interval and max proof age; nothing is saved), then saves only what changed; the server asks for a step-up when the
 * change loosens a target (ADR-008 §4).
 */
function PolicyForm({ policy, repos, templates, busy, onSave, onClose }: {
  policy: PolicyView | null
  repos: readonly (readonly [number, string])[]
  templates: string[]
  busy: boolean
  onSave: (body: PolicyPatch | PolicyInput) => void
  onClose: () => void
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
        {DURS.map((k) => <Field key={k} label={label(k, p)}><input value={d[k]} onChange={set(k)} inputMode="text" /></Field>)}
        {COUNTS.map((k) => <Field key={k} label={label(k, p)}><input value={d[k]} onChange={set(k)} inputMode="numeric" /></Field>)}
        <Field label={p.f.template}>
          <input value={d.check_template} onChange={set('check_template')} list="gaia-templates" />
        </Field>
      </div>
      <p className={s.hint}>{p.durHint} {p.templateHint}</p>
      <datalist id="gaia-templates">{templates.map((x) => <option key={x} value={x} />)}</datalist>
      {policy && valid && <Effect policy={policy} change={wi} rpoChanged={v.rpo_target_s !== policy.rpo_target_s} />}
      {errors.length > 0 && (tried || policy) && <p className={s.hint} role="status">{errors.join(' ')}</p>}
      {policy && valid && changed && looser(policy, v, d.check_template.trim()) && <p className={s.hint}>{p.looser}</p>}
      <div className={s.buttons}>
        <Button type="submit" disabled={busy || !valid || (!!policy && !changed)}>{busy ? t.working : policy ? p.save : p.create}</Button>
        <Button type="button" variant="quiet" onClick={onClose}>{policy ? p.close : t.cancel}</Button>
        {policy && !changed && <span className={s.hint}>{p.unchanged}</span>}
      </div>
    </form>
  )
}

function testsPerDay(c: CapacityView) {
  return Math.round(c.by_os_class.reduce((n, g) => n + g.tests_per_day, 0))
}

/** C12's numbers of one policy in a what-if answer, when the server sends them. */
function okIn(c: WhatIfView | undefined, id: number) {
  return c?.policies?.find((x) => x.policy_id === id)
}

/**
 * Proof capacity now and after the change, side by side, and (ADR-007 C12) how much of the day a VM on the policy can
 * be OK: now from the policy, after from the what-if answer. The what-if models the test interval and the proof age
 * only, so after a changed RPO target the "after" share is not shown: the screen does not guess it.
 */
function Effect({ policy, change: wi, rpoChanged }: { policy: PolicyView; change: PolicyChange | null; rpoChanged: boolean }) {
  const p = useT().inventory.policies
  const { base, after } = useWhatIf(wi)
  const nowShare = shareOf(policy)
  const afterOK = wi && !rpoChanged ? okIn(after.data, policy.id) : undefined
  const afterShare = afterOK ? shareOf(afterOK) : undefined
  const shareRows = nowShare !== undefined || afterShare !== undefined
  const shareCell = (f: number | undefined) => <td>{f === undefined ? '' : <span className={f < 1 ? s.warnText : undefined}>{p.okShare(f)}</span>}</td>
  const suggestedCell = (o: { max_ok_fraction?: number | null; suggested_test_interval_s?: number | null } | undefined) =>
    <td>{o && shareOf(o) !== undefined ? p.suggestedVal(o.suggested_test_interval_s) : ''}</td>
  const head = <h4>{p.effect}</h4>
  if (!wi) {
    return (
      <div className={s.effect}>
        {head}
        <p className={s.hint}>{p.effectNone}</p>
        {nowShare !== undefined && !rpoChanged && <p className={nowShare < 1 ? s.warnText : s.hint}>{p.okSentence(nowShare, policy.suggested_test_interval_s)}</p>}
      </div>
    )
  }
  if (base.isError || after.isError) return <div className={s.effect}>{head}<Notice tone="warning">{p.effectFailed}</Notice></div>
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
      {head}
      <p className={s.hint}>{p.effectHint}</p>
      <table>
        <thead>
          <tr><th scope="col"><span className={s.vh}>{p.effect}</span></th><th scope="col">{p.now}</th><th scope="col">{p.after}</th></tr>
        </thead>
        <tbody>
          {shareRows && <tr><th scope="row">{p.canBeOK}</th>{shareCell(nowShare)}{shareCell(afterShare)}</tr>}
          {shareRows && <tr><th scope="row">{p.suggested}</th>{suggestedCell(policy)}{suggestedCell(afterOK)}</tr>}
          <tr><th scope="row">{p.coverage}</th>{cell(b, cover)}{cell(a, cover)}</tr>
          <tr><th scope="row">{p.slots}</th>{cell(b, (c) => p.slotsVal(c.slots_needed, c.slots))}{cell(a, (c) => p.slotsVal(c.slots_needed, c.slots))}</tr>
          <tr><th scope="row">{p.tests}</th>{cell(b, (c) => testsPerDay(c).toLocaleString('en-US'))}{cell(a, (c) => testsPerDay(c).toLocaleString('en-US'))}</tr>
          <tr><th scope="row">{p.expiring}</th>{cell(b, (c) => c.expiring_total.toLocaleString('en-US'))}{cell(a, (c) => c.expiring_total.toLocaleString('en-US'))}</tr>
        </tbody>
      </table>
      {afterShare !== undefined && afterShare < 1 && <Notice tone="warning">{p.partialAfter(p.okShare(afterShare), afterOK?.suggested_test_interval_s)}</Notice>}
      {a && a.coverage < 1 && <Notice tone="warning">{p.behind}</Notice>}
    </div>
  )
}
