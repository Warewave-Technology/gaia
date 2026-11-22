import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useT } from '../../i18n'
import { durationBasis, orderReason, osClassBasis } from '../../i18n/en-capacity'
import { dur } from '../../lib/duration'
import { serverTime, type Fmt } from '../../lib/time'
import { useFilters } from '../../lib/url'
import { useCapacity, usePolicies, useWhatIf, type CapacityGroup, type CapacityView, type Expiring, type QueueItem, type WhatIf } from '../api/capacity'
import { useCapacityInputPages, useTestQueuePages, type CapacityInput, type TestQueueView } from '../api/capacityPages'
import { Failed, Loading } from '../layout/States'
import {
  Button, DataTable, Fact, Facts, JobState, PageHead, Pick, SectionHead, Seg, StatusChip, TabPanel, Tabs, TechDetails, by, type Column,
} from '../ui'
import s from './Capacity.module.css'

/** The test intervals a what-if offers besides the policy's own. */
const INTERVALS = [3600, 6 * 3600, 12 * 3600, 86400, 2 * 86400, 7 * 86400]
/** The expiring proofs the screen holds (the view answers at most this many of a list, without a cursor). */
const EXPIRING_MAX = 1000
const TABS = ['demand', 'expiring', 'queue', 'inputs'] as const
type TabKey = (typeof TABS)[number]
const KEYS = ['tab'] as const

/** The horizon in hours when it is whole hours ("24 h"), else as a duration. */
function horizon(v: CapacityView) {
  return v.horizon_s % 3600 === 0 ? `${v.horizon_s / 3600} h` : dur(v.horizon_s)
}

const hours = (slotS: number) => slotS / 3600
const pct = (x: number) => Math.floor(x * 100)
const tests = (v: CapacityView) => v.by_os_class.reduce((n, g) => n + g.tests_per_day, 0)
/** A queued test the simulation does not start within its horizon has the zero time. */
const starts = (iso: string) => Date.parse(iso) > 0

/** The slots the sandbox unit is set to, when the server knows them while the helper does not answer (a newer server
 * sends `configured_slots`); null when not known. */
function configuredSlots(v: CapacityView): number | null {
  const c = (v as CapacityView & { configured_slots?: number | null }).configured_slots
  return typeof c === 'number' && c > 0 ? c : null
}

/** A value that follows `v` once it has stood still for `ms` (the slider asks the what-if once, not at every step). */
function useSettled<V>(v: V, ms = 300): V {
  const [out, setOut] = useState(v)
  useEffect(() => {
    const id = setTimeout(() => setOut(v), ms)
    return () => clearTimeout(id)
  }, [v, ms])
  return out
}

/**
 * Proof capacity (docs/design/v2/Capacity.dc.html with the v3 rules, ADR-007 C4, C13): can the sandbox's slots run
 * every restore test the policies ask for before the proofs expire. The answer and a what-if that only models are
 * above the fold; demand, the proofs that will expire, the queue and the expected durations are tabs of data-tables.
 * A sandbox that does not answer is UNKNOWN, never a calm zero, and the model then starts from its configured slots.
 */
export function Capacity() {
  const t = useT().capacity
  const [f, setF] = useFilters(KEYS)
  const tab: TabKey = (TABS as readonly string[]).includes(f.tab) ? (f.tab as TabKey) : 'demand'
  // The expiring proofs are long: only their tab asks for them.
  const q = useCapacity(tab === 'expiring' ? EXPIRING_MAX : 1)
  const [slots, setSlots] = useState<number | null>(null)
  const [changes, setChanges] = useState<Record<number, number>>({})
  const v = q.data
  const down = v ? v.sandbox === null : false
  const start = v && down ? (configuredSlots(v) ?? 1) : null
  const modelSlots = slots ?? start
  const settled = useSettled(useMemo(() => ({ slots: modelSlots, changes }), [modelSlots, changes]))
  const body = useMemo<WhatIf | null>(() => {
    const policies = Object.entries(settled.changes).map(([id, iv]) => ({ policy_id: Number(id), test_interval_s: iv })).sort((a, b) => a.policy_id - b.policy_id)
    if (settled.slots === null && policies.length === 0) return null
    return { ...(settled.slots !== null && { slots: settled.slots }), ...(policies.length > 0 && { policies }) }
  }, [settled])
  const model = useWhatIf(body)
  if (q.isPending) return <Loading />
  if (q.isError || !v) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const m = body ? model.data : undefined
  const fmt = serverTime(v.clock)
  const h = horizon(v)
  return (
    <main className={s.page}>
      <Headline v={v} m={start !== null && slots === null ? m : undefined} start={start} />
      <Model v={v} m={m} start={start} modelling={!!body && model.isFetching} failed={body ? model.error : null}
        slots={slots} setSlots={setSlots} changes={changes} setChanges={setChanges} />
      <section className={s.detail}>
        <Tabs label={t.tabs} value={tab} onChange={(k) => setF({ tab: k === 'demand' ? '' : k })}
          hint={tab === 'expiring' ? t.hint.expiring(h) : t.hint[tab]}
          tabs={[
            { key: 'demand', label: t.tab.demand, count: v.by_policy.length || '' },
            { key: 'expiring', label: t.tab.expiring, count: v.expiring_total.toLocaleString('en-US') },
            { key: 'queue', label: t.tab.queue, count: v.queue_total.toLocaleString('en-US') },
            { key: 'inputs', label: t.tab.inputs, count: v.vms.toLocaleString('en-US') },
          ]} />
        <TabPanel id={tab}>
          {tab === 'demand' && <Demand v={v} m={m} changes={changes} />}
          {tab === 'expiring' && <ExpiringList v={v} fmt={fmt} loading={q.isFetching && v.expiring.length < Math.min(v.expiring_total, EXPIRING_MAX)} />}
          {tab === 'queue' && <Queue h={h} down={down} />}
          {tab === 'inputs' && <Inputs />}
        </TabPanel>
      </section>
    </main>
  )
}

function Headline({ v, m, start }: { v: CapacityView; m: CapacityView | undefined; start: number | null }) {
  const t = useT().capacity
  const h = horizon(v)
  const down = v.sandbox === null
  const configured = configuredSlots(v)
  const answer = down ? (
    <span className={s.unknown}><StatusChip status="UNKNOWN" size="lg" />{t.unknown}</span>
  ) : v.demand_slot_s_per_day === 0 ? t.yesNoDemand
    : v.coverage >= 1 ? t.yesSpare(pct((v.capacity_slot_s_per_day - v.demand_slot_s_per_day) / v.capacity_slot_s_per_day))
    : t.no(pct(v.coverage))
  const lede = down ? (
    <>
      <p>
        {t.sandboxDown}{' '}
        {m && start !== null && (configured !== null ? t.modelFromConfigured(start, pct(m.coverage)) : t.modelFromOne(pct(m.coverage)))}
      </p>
      <TechDetails>{v.sandbox_error}</TechDetails>
    </>
  ) : v.demand_slot_s_per_day === 0 ? null : (
    <p>
      {t.ask(Math.round(tests(v)), v.vms)}{' '}
      {v.coverage >= 1 ? t.ok(v.slots) : <><b>{t.behind(v.slots, v.expiring_total, h)}</b> {t.need(v.slots_needed)}</>}
    </p>
  )
  return (
    <PageHead
      question={t.question}
      answer={answer}
      lede={lede}
      aside={
        <Facts label={t.facts}>
          {v.sandbox === null ? (
            <Fact term={t.slotsNow}><StatusChip status="UNKNOWN" size="xs" />{t.noAnswer}{configured !== null && <> · {t.configured(configured)}</>}</Fact>
          ) : (
            <>
              <Fact term={t.slotsNow}>{t.slotsText(v.sandbox.slots, v.sandbox.basis)}</Fact>
              <Fact term={t.memory}>{t.memoryText(v.sandbox.used_mib, v.sandbox.memory_limit_mib)}</Fact>
              <Fact term={t.runningNow}>{t.running(v.sandbox.running.length)}</Fact>
              <Fact term={t.waitingMem}>{t.waiting(v.sandbox.waiting.length)}</Fact>
            </>
          )}
        </Facts>
      }
    />
  )
}

function Model({ v, m, start, modelling, failed, slots, setSlots, changes, setChanges }: {
  v: CapacityView
  m: CapacityView | undefined
  /** While the sandbox does not answer: the slots the model starts from. */
  start: number | null
  modelling: boolean
  failed: unknown
  slots: number | null
  setSlots: (n: number | null) => void
  changes: Record<number, number>
  setChanges: (c: Record<number, number>) => void
}) {
  const t = useT().capacity
  const policies = usePolicies().data ?? []
  // The policy the interval choice starts on: the one that asks for the most test time.
  const busiest = useMemo(() => {
    const top = [...v.by_policy].sort((a, b) => b.demand_slot_s_per_day - a.demand_slot_s_per_day)[0]
    return policies.find((p) => p.name === top?.name)?.id ?? policies[0]?.id
  }, [v.by_policy, policies])
  const [picked, setPicked] = useState<number | null>(null)
  const pid = picked ?? busiest
  const policy = policies.find((p) => p.id === pid)
  const base = start ?? v.slots // what the slider shows when nothing is moved
  const shown = slots ?? base
  const max = Math.min(64, Math.max(8, v.slots * 2, v.slots_needed + 2))
  const h = horizon(v)
  const changed = [
    ...(slots !== null && slots !== v.slots ? [t.slotsChange(v.slots, slots)] : []),
    ...Object.entries(changes).map(([id, iv]) => {
      const p = policies.find((x) => x.id === Number(id))
      return t.policyChange(p?.name ?? id, dur(iv), dur(p?.test_interval_s))
    }),
  ]
  const figs: [string, (x: CapacityView) => string][] = [
    [t.fig.slots, (x) => x.slots.toLocaleString('en-US')],
    [t.fig.covers, (x) => t.pct(pct(x.coverage))],
    [t.fig.needed, (x) => x.slots_needed.toLocaleString('en-US')],
    [t.fig.tests, (x) => Math.round(tests(x)).toLocaleString('en-US')],
    [t.fig.expiring(h), (x) => x.expiring_total.toLocaleString('en-US')],
  ]
  const scale = Math.max(v.demand_slot_s_per_day, v.capacity_slot_s_per_day, m?.demand_slot_s_per_day ?? 0, m?.capacity_slot_s_per_day ?? 0)
  return (
    <section className={s.panel} aria-labelledby="model">
      <SectionHead id="model" title={t.model} hint={t.modelHint} />
      <div className={s.controls}>
        <label className={s.ctl}>
          <span className={s.lbl}>{t.slots}</span>
          <span className={s.slider}>
            <input type="range" min={1} max={max} step={1} value={Math.max(1, shown)} aria-label={t.slots}
              onChange={(e) => { const x = Number(e.target.value); setSlots(x === base ? null : x) }} />
            <b className={s.big}>{shown}</b>
          </span>
        </label>
        {policy && (
          <div className={s.ctl}>
            <span className={s.lbl}>{t.interval}</span>
            <span className={s.row}>
              <Pick label={t.policy} value={String(policy.id)} onChange={(x) => setPicked(Number(x))}
                options={policies.map((p) => [String(p.id), p.name] as const)} />
              <Seg label={t.interval} value={changes[policy.id] ? String(changes[policy.id]) : ''}
                options={[['', t.asSet(dur(policy.test_interval_s))], ...INTERVALS.filter((x) => x !== policy.test_interval_s).map((x) => [String(x), dur(x)] as const)]}
                onChange={(x) => {
                  const next = { ...changes }
                  if (x) next[policy.id] = Number(x)
                  else delete next[policy.id]
                  setChanges(next)
                }} />
            </span>
          </div>
        )}
        <dl className={s.figs} aria-label={t.model}>
          {figs.map(([label, val]) => {
            const now = val(v)
            const mod = m ? val(m) : null
            return (
              <div key={label}>
                <dt>{label}</dt>
                <dd>{mod !== null && mod !== now ? <><span className={s.was}>{now}</span> → <b className={s.changedFig}>{mod}</b></> : <b className={s.fig}>{now}</b>}</dd>
              </div>
            )
          })}
        </dl>
      </div>
      <div className={s.bars}>
        <CapBar label={t.barNow} v={v} scale={scale} />
        {m && <CapBar label={t.barModel} v={m} scale={scale} />}
        <Legend v={m ?? v} />
      </div>
      {(changed.length > 0 || !!failed) && (
        <div className={s.changed}>
          {changed.length > 0 && <span>{t.modelled(changed.join(' · '))}</span>}
          {(slots !== null || Object.keys(changes).length > 0) && (
            <Button variant="quiet" onClick={() => { setSlots(null); setChanges({}) }}>{t.reset}</Button>
          )}
          {modelling && <span className={s.hint}>{t.modelling}</span>}
          {failed ? <Failed error={failed} /> : null}
        </div>
      )}
    </section>
  )
}

/** The OS classes as drawn: the first one alone, the rest together once there are more than two (at most four legend
 * items, docs/design/v3 rule 5). */
function classes(v: CapacityView): { name: string; demand: number }[] {
  const g = v.by_os_class.map((x) => ({ name: x.name, demand: x.demand_slot_s_per_day }))
  if (g.length <= 2) return g
  return [g[0], { name: '', demand: g.slice(1).reduce((n, x) => n + x.demand, 0) }]
}

/** Demand by OS class against the capacity, in slot-hours a day; the part the capacity does not cover is hatched.
 * Segments are labelled where they are drawn when they are wide enough. */
function CapBar({ label, v, scale }: { label: string; v: CapacityView; scale: number }) {
  const t = useT().capacity
  const sc = scale * 1.08 || 1
  const w = (x: number) => `${(x / sc) * 100}%`
  let left = 0
  return (
    <div className={s.barRow}>
      <span className={s.barLbl}>{label}</span>
      <div className={s.cap} role="img" aria-label={t.barLabel(hours(v.demand_slot_s_per_day), hours(v.capacity_slot_s_per_day))}>
        {classes(v).map((g, i) => {
          const el = (
            <span key={g.name || 'other'} className={`${s.seg} ${s[`c${i}`]}`} style={{ left: w(left), width: w(g.demand) }}>
              {g.demand / sc > 0.14 && <span className={s.segLbl}>{t.segment(g.name ? t.osClass(g.name) : t.otherClasses, hours(g.demand))}</span>}
            </span>
          )
          left += g.demand
          return el
        })}
        {v.capacity_slot_s_per_day < v.demand_slot_s_per_day && (
          <span className={s.gap} style={{ left: w(v.capacity_slot_s_per_day), width: w(v.demand_slot_s_per_day - v.capacity_slot_s_per_day) }} />
        )}
        <span className={s.line} style={{ left: w(v.capacity_slot_s_per_day) }} />
      </div>
    </div>
  )
}

function Legend({ v }: { v: CapacityView }) {
  const t = useT().capacity
  return (
    <div className={s.legend}>
      {classes(v).map((g, i) => (
        <span key={g.name || 'other'}><i className={`${s.sw} ${s[`c${i}`]}`} />{t.classLegend(g.name ? t.osClass(g.name) : t.otherClasses, hours(g.demand))}</span>
      ))}
      <span><i className={s.swLine} />{t.capLegend(hours(v.capacity_slot_s_per_day))}</span>
      {v.capacity_slot_s_per_day < v.demand_slot_s_per_day && <span><i className={`${s.sw} ${s.swGap}`} />{t.gap}</span>}
    </div>
  )
}

/** Demand by OS class and by policy: short lists, each a data-table the screen holds. */
function Demand({ v, m, changes }: { v: CapacityView; m: CapacityView | undefined; changes: Record<number, number> }) {
  const t = useT().capacity
  const policies = usePolicies().data ?? []
  const byName = new Map(policies.map((p) => [p.name, p]))
  const share = (g: CapacityGroup) => (v.demand_slot_s_per_day > 0 ? Math.round((g.demand_slot_s_per_day / v.demand_slot_s_per_day) * 100) : 0)
  const common = (list: (x: CapacityView) => CapacityGroup[]): Column<CapacityGroup>[] => {
    const cols: Column<CapacityGroup>[] = [
      { key: 'vms', header: t.col.vms, numeric: true, width: '80px', compare: by((g) => g.vms), cell: (g) => g.vms.toLocaleString('en-US') },
      { key: 'tests', header: t.col.tests, numeric: true, width: '110px', compare: by((g) => g.tests_per_day), cell: (g) => Math.round(g.tests_per_day).toLocaleString('en-US') },
      { key: 'each', header: t.col.each, numeric: true, width: '110px', compare: by((g) => g.median_test_s), cell: (g) => dur(g.median_test_s, true) },
      { key: 'hours', header: t.col.hours, numeric: true, width: '140px', compare: by((g) => g.demand_slot_s_per_day),
        cell: (g) => <b className={s.fig}>{hours(g.demand_slot_s_per_day).toFixed(1)}</b> },
      { key: 'share', header: t.col.share, numeric: true, width: '80px', compare: by(share), cell: (g) => t.share(share(g)) },
    ]
    if (m) {
      cols.push({ key: 'model', header: t.col.model, numeric: true, width: '110px', cell: (g) => {
        const mg = list(m).find((x) => x.name === g.name)
        return <b className={mg && mg.demand_slot_s_per_day !== g.demand_slot_s_per_day ? s.changedFig : s.fig}>{mg ? hours(mg.demand_slot_s_per_day).toFixed(1) : '—'}</b>
      } })
    }
    return cols
  }
  const classCols: Column<CapacityGroup>[] = [
    { key: 'cls', header: t.col.cls, compare: by((g) => g.name), cell: (g) => t.osClass(g.name) },
    ...common((x) => x.by_os_class),
  ]
  const policyCols: Column<CapacityGroup>[] = [
    { key: 'policy', header: t.col.policy, compare: by((g) => g.name), cell: (g) => <span className={s.name}>{g.name}</span>, title: (g) => g.name },
    { key: 'every', header: t.col.every, width: '150px', cell: (g) => {
      const p = byName.get(g.name)
      const iv = p ? changes[p.id] : undefined
      return <>{dur(p?.test_interval_s)}{iv !== undefined && <span className={s.of}>{t.modelled(dur(iv))}</span>}</>
    } },
    ...common((x) => x.by_policy),
  ]
  return (
    <div className={s.demand}>
      <div className={s.sec}>
        <h2 className={s.h2}>{t.classes}</h2>
        <DataTable caption={t.classes} columns={classCols} rows={v.by_os_class} rowKey={(g) => g.name} sizes={false} height="240px" empty={t.noPolicies} />
      </div>
      <div className={s.sec}>
        <h2 className={s.h2}>{t.policies}</h2>
        <DataTable caption={t.policies} columns={policyCols} rows={v.by_policy} rowKey={(g) => g.name} height="360px" empty={t.noPolicies} />
      </div>
    </div>
  )
}

function ExpiringList({ v, fmt, loading }: { v: CapacityView; fmt: Fmt; loading: boolean }) {
  const t = useT().capacity
  const h = horizon(v)
  const cols: Column<Expiring>[] = [
    { key: 'vm', header: t.ecol.vm, width: '30%', compare: by((e) => e.vm), title: (e) => e.vm,
      cell: (e) => <Link className={s.vm} to={`/vms/${e.vm_id}`}>{e.vm}</Link> },
    { key: 'policy', header: t.ecol.policy, width: '18%', compare: by((e) => e.policy), cell: (e) => <span className={s.name}>{e.policy}</span> },
    { key: 'expires', header: t.ecol.expires, width: '20%', compare: by((e) => e.proof_expires_at), cell: (e) => {
      const past = fmt.age(e.proof_expires_at) > 0
      return <b className={past ? s.past : s.fig}>{past ? t.expired(fmt.at(e.proof_expires_at)) : fmt.at(e.proof_expires_at)}</b>
    } },
    { key: 'next', header: t.ecol.next, compare: by((e) => e.next_test_start ?? '~'), cell: (e) =>
      e.next_test_start && e.next_test_finish ? t.nextTest(fmt.at(e.next_test_start), fmt.at(e.next_test_finish)) : t.noTest(h) },
  ]
  if (v.sandbox === null && v.expiring_total === 0) return <p className={s.empty}>{t.expiringUnknown}</p>
  return (
    <>
      <DataTable caption={t.tab.expiring} columns={cols} rows={v.expiring} rowKey={(e) => String(e.vm_id)} empty={loading ? undefined : t.noneExpiring} />
      {v.expiring_total > v.expiring.length && v.expiring.length > 0 && <p className={s.hint}>{t.expiringCut(v.expiring.length, v.expiring_total)}</p>}
    </>
  )
}

type NowRow = { key: string; vm: string; running: boolean; slot?: number; memory: number; since: string; why?: string }

/** The queue in the order it starts, paged by the server; what runs now and what waits for memory above it. */
function Queue({ h, down }: { h: string; down: boolean }) {
  const t = useT().capacity
  const { pages, head } = useTestQueuePages()
  if (pages.isError) return <Failed error={pages.error} onRetry={() => pages.refetch()} />
  const fmt = head ? serverTime(head.clock) : null
  const cols: Column<QueueItem>[] = [
    { key: 'pos', header: t.qcol.pos, numeric: true, width: '64px', cell: (r) => t.position(r.queue_position) },
    { key: 'vm', header: t.qcol.vm, width: '24%', title: (r) => r.vm, cell: (r) => <Link className={s.vm} to={`/vms/${r.vm_id}`}>{r.vm}</Link> },
    { key: 'state', header: t.qcol.state, width: '210px',
      cell: (r) => <JobState state="queued" detail={starts(r.estimated_start) && fmt ? t.startsAt(fmt.at(r.estimated_start)) : t.notInHorizon(h)} /> },
    { key: 'why', header: t.qcol.why, title: (r) => (fmt ? orderReason(r.order_reason, fmt.at) : r.order_reason),
      cell: (r) => (fmt ? orderReason(r.order_reason, fmt.at) : r.order_reason) },
    { key: 'expected', header: t.qcol.expected, width: '260px', title: (r) => t.basisOf(durationBasis(r.duration_basis)),
      cell: (r) => <><span className={s.figSm}>{dur(r.expected_s, true)}</span><span className={s.sub}>{durationBasis(r.duration_basis)}</span></> },
  ]
  return (
    <>
      {head && <NowTable head={head} />}
      <DataTable caption={t.queueCaption} columns={cols} pages={pages} rowKey={(r) => String(r.job_id)} empty={down ? t.queueUnknown : t.queueEmpty} />
    </>
  )
}

function NowTable({ head }: { head: TestQueueView }) {
  const t = useT().capacity
  const fmt = serverTime(head.clock)
  const rows: NowRow[] = [
    ...head.running.map((r) => ({ key: `run/${r.slot}`, vm: r.vm, running: true, slot: r.slot, memory: r.memory_mib, since: r.since })),
    ...head.waiting_for_admission.map((w) => ({ key: `wait/${w.vm}`, vm: w.vm, running: false, memory: w.memory_mib, since: w.since, why: w.why })),
  ]
  if (rows.length === 0) return null
  const cols: Column<NowRow>[] = [
    { key: 'state', header: t.qcol.state, width: '220px',
      cell: (r) => <JobState state={r.running ? 'running' : 'queued'} detail={r.running ? t.runningFor(dur(fmt.age(r.since))) : t.waitingSince(fmt.at(r.since))} /> },
    { key: 'vm', header: t.qcol.vm, width: '26%', title: (r) => r.vm, cell: (r) => <span className={s.name}>{r.vm}</span> },
    { key: 'slot', header: t.qcol.where, width: '150px', cell: (r) => (r.slot !== undefined ? t.slot(r.slot, head.slots) : t.waitingMemory) },
    { key: 'mem', header: t.memory, numeric: true, width: '150px', cell: (r) => t.memMiB(r.memory) },
    { key: 'why', header: t.qcol.note, title: (r) => r.why || undefined, cell: (r) => r.why ?? '' },
  ]
  return <DataTable caption={t.nowCaption} columns={cols} rows={rows} rowKey={(r) => r.key} sizes={false} height="200px" />
}

/** Every VM's expected test duration and where it comes from, paged by the server. */
function Inputs() {
  const t = useT().capacity
  const { pages, medians } = useCapacityInputPages()
  if (pages.isError) return <Failed error={pages.error} onRetry={() => pages.refetch()} />
  const cols: Column<CapacityInput>[] = [
    { key: 'vm', header: t.icol.vm, width: '24%', title: (r) => r.vm, cell: (r) => <Link className={s.vm} to={`/vms/${r.vm_id}`}>{r.vm}</Link> },
    { key: 'policy', header: t.icol.policy, width: '14%', cell: (r) => <span className={s.name}>{r.policy}</span> },
    { key: 'cls', header: t.icol.cls, width: '18%', title: (r) => osClassBasis(r.os_class_basis) || undefined,
      cell: (r) => <>{t.osClass(r.os_class)}{r.os_class_basis && <span className={s.sub}>{osClassBasis(r.os_class_basis)}</span>}</> },
    { key: 'every', header: t.icol.every, width: '110px', cell: (r) => dur(r.test_interval_s) },
    { key: 'age', header: t.icol.age, width: '120px', cell: (r) => dur(r.max_proof_age_s) },
    { key: 'expected', header: t.icol.expected, numeric: true, width: '110px', cell: (r) => <span className={s.figSm}>{dur(r.expected_s, true)}</span> },
    { key: 'basis', header: t.icol.basis, title: (r) => durationBasis(r.basis), cell: (r) => durationBasis(r.basis) },
  ]
  const ms = Object.entries(medians).map(([c, v]) => `${t.osClass(c)} ${dur(v, true)}`).join(' · ')
  return (
    <>
      <DataTable caption={t.tab.inputs} columns={cols} pages={pages} rowKey={(r) => String(r.vm_id)} empty={t.noInputs}
        detail={(r) => <p>{r.durations_s.length ? t.durations(r.durations_s.map((d) => dur(d, true)).join(', ')) : t.noDurations}</p>} />
      <p className={s.hint}>{ms && <>{t.classMedians(ms)}. </>}{t.note}</p>
    </>
  )
}
