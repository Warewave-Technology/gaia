import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { secondsBetween, serverTime, type Fmt, type ServerClock } from '../../lib/time'
import {
  useCapacity, usePolicies, useTestQueue, useWhatIf, type CapacityGroup, type CapacityView, type QueueItem, type WhatIf,
} from '../api/capacity'
import { Failed, Loading } from '../layout/States'
import { Button, Fact, Facts, MoreRows, PageHead, Pick, SectionHead, Seg, StatusChip } from '../ui'
import s from './Capacity.module.css'

type T = ReturnType<typeof useT>['capacity']

/** The test intervals a what-if offers besides the policy's own. */
const INTERVALS = [3600, 6 * 3600, 12 * 3600, 86400, 2 * 86400, 7 * 86400]
/** The expiring proofs grow by this many; the view answers at most MAX_LIMIT of a list. */
const STEP = 25
const MAX_LIMIT = 1000
const ISO = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})/g

/** "Today 04:51" or "Nov 17 04:51" in the server's zone. */
function when(fmt: Fmt, clock: ServerClock, iso: string, t: T) {
  return fmt.date(iso) === fmt.date(clock.now) ? t.today(fmt.hhmm(iso)) : `${fmt.date(iso)} ${fmt.hhmm(iso)}`
}

/** The horizon in hours when it is whole hours ("24 h"), else as a duration. */
function horizon(v: CapacityView) {
  return v.horizon_s % 3600 === 0 ? `${v.horizon_s / 3600} h` : dur(v.horizon_s)
}

const hours = (slotS: number) => slotS / 3600
const pct = (x: number) => Math.floor(x * 100)

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
 * Proof capacity (docs/design/v2/Capacity.dc.html, ADR-007 C4): can the sandbox's slots run every restore test the
 * policies ask for before the proofs expire. Demand and capacity by OS class, a what-if that only models, demand by
 * policy, the proofs that will expire, and the queue in the order it starts. A sandbox that does not answer is
 * UNKNOWN, never a calm zero.
 */
export function Capacity() {
  const [limit, setLimit] = useState(STEP)
  const q = useCapacity(limit)
  const [slots, setSlots] = useState<number | null>(null)
  const [changes, setChanges] = useState<Record<number, number>>({})
  const settled = useSettled(useMemo(() => ({ slots, changes }), [slots, changes]))
  const body = useMemo<WhatIf | null>(() => {
    const policies = Object.entries(settled.changes).map(([id, iv]) => ({ policy_id: Number(id), test_interval_s: iv })).sort((a, b) => a.policy_id - b.policy_id)
    if (settled.slots === null && policies.length === 0) return null
    return { ...(settled.slots !== null && { slots: settled.slots }), ...(policies.length > 0 && { policies }) }
  }, [settled])
  const model = useWhatIf(body)
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const v = q.data
  const m = body ? model.data : undefined
  return (
    <main className={s.page}>
      <Headline v={v} />
      <Model v={v} m={m} modelling={!!body && model.isFetching} failed={body ? model.error : null}
        slots={slots} setSlots={setSlots} changes={changes} setChanges={setChanges} />
      <div className={s.two}>
        <ByPolicy v={v} m={m} changes={changes} />
        <ExpiringList v={v} loading={q.isFetching && q.isPlaceholderData} onMore={() => setLimit((l) => Math.min(MAX_LIMIT, l + STEP * 2))} />
      </div>
      <Queue h={horizon(v)} />
    </main>
  )
}

function Headline({ v }: { v: CapacityView }) {
  const t = useT().capacity
  const tests = v.by_os_class.reduce((n, g) => n + g.tests_per_day, 0)
  const h = horizon(v)
  const down = v.sandbox === null
  const answer = down ? (
    <span className={s.unknown}><StatusChip status="UNKNOWN" size="lg" />{t.unknown}</span>
  ) : v.demand_slot_s_per_day === 0 ? t.yesNoDemand
    : v.coverage >= 1 ? t.yesSpare(pct((v.capacity_slot_s_per_day - v.demand_slot_s_per_day) / v.capacity_slot_s_per_day))
    : t.no(pct(v.coverage))
  const lede = down ? <p>{t.sandboxDown(v.sandbox_error)}</p> : v.demand_slot_s_per_day === 0 ? null : (
    <p>
      {t.ask(Math.round(tests), v.vms)}{' '}
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
            <Fact term={t.slotsNow}><StatusChip status="UNKNOWN" size="xs" />{t.noAnswer}</Fact>
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

function Model({ v, m, modelling, failed, slots, setSlots, changes, setChanges }: {
  v: CapacityView
  m: CapacityView | undefined
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
  const shown = slots ?? v.slots
  const max = Math.min(64, Math.max(8, v.slots * 2, v.slots_needed + 2))
  const h = horizon(v)
  const changed = [
    ...(slots !== null ? [t.slotsChange(v.slots, slots)] : []),
    ...Object.entries(changes).map(([id, iv]) => {
      const p = policies.find((x) => x.id === Number(id))
      return t.policyChange(p?.name ?? id, dur(iv), dur(p?.test_interval_s))
    }),
  ]
  const rows: [string, (x: CapacityView) => string][] = [
    [t.row.slots, (x) => x.slots.toLocaleString('en-US')],
    [t.row.capacity, (x) => t.slotHours(hours(x.capacity_slot_s_per_day))],
    [t.row.demand, (x) => t.slotHours(hours(x.demand_slot_s_per_day))],
    [t.row.coverage, (x) => t.pct(pct(x.coverage))],
    [t.row.needed, (x) => x.slots_needed.toLocaleString('en-US')],
    [t.row.tests, (x) => Math.round(x.by_os_class.reduce((n, g) => n + g.tests_per_day, 0)).toLocaleString('en-US')],
    [t.row.expiring(h), (x) => x.expiring_total.toLocaleString('en-US')],
  ]
  return (
    <section className={s.panel} aria-labelledby="model">
      <SectionHead id="model" title={t.model} hint={t.modelHint} />
      <div className={s.controls}>
        <label className={s.ctl}>
          <span className={s.lbl}>{t.slots}</span>
          <span className={s.slider}>
            <input type="range" min={1} max={max} step={1} value={Math.max(1, shown)} aria-label={t.slots}
              onChange={(e) => { const x = Number(e.target.value); setSlots(x === v.slots ? null : x) }} />
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
      </div>
      {changed.length > 0 && (
        <p className={s.changed}>
          {t.modelled(changed.join(' · '))}{' '}
          <Button variant="quiet" onClick={() => { setSlots(null); setChanges({}) }}>{t.reset}</Button>
          {modelling && <span className={s.hint}> {t.modelling}</span>}
        </p>
      )}
      {failed ? <Failed error={failed} /> : null}
      <div className={s.bars}>
        <CapBar label={t.barNow} v={v} scale={Math.max(v.demand_slot_s_per_day, v.capacity_slot_s_per_day, m?.demand_slot_s_per_day ?? 0, m?.capacity_slot_s_per_day ?? 0)} />
        {m && <CapBar label={t.barModel} v={m} scale={Math.max(v.demand_slot_s_per_day, v.capacity_slot_s_per_day, m.demand_slot_s_per_day, m.capacity_slot_s_per_day)} />}
        <Legend v={m ?? v} />
      </div>
      <table className={`${s.tbl} ${s.compare}`}>
        <thead>
          <tr>
            <th scope="col">{t.col.what}</th>
            <th scope="col" className={s.num}>{t.col.now}</th>
            <th scope="col" className={s.num}>{t.col.model}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map(([label, val]) => (
            <tr key={label}>
              <th scope="row">{label}</th>
              <td className={s.num}><b className={s.fig}>{val(v)}</b></td>
              <td className={s.num}>{m ? <b className={val(m) === val(v) ? s.fig : s.changedFig}>{val(m)}</b> : <span className={s.of}>—</span>}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className={s.hint}>{t.note}</p>
    </section>
  )
}

/** Demand by OS class against the capacity, in slot-hours a day; the part the capacity does not cover is hatched. */
function CapBar({ label, v, scale }: { label: string; v: CapacityView; scale: number }) {
  const t = useT().capacity
  const sc = scale * 1.08 || 1
  const w = (x: number) => `${(x / sc) * 100}%`
  let left = 0
  return (
    <div className={s.barRow}>
      <span className={s.barLbl}>{label}</span>
      <div className={s.cap} role="img" aria-label={t.barLabel(hours(v.demand_slot_s_per_day), hours(v.capacity_slot_s_per_day))}>
        {v.by_os_class.map((g, i) => {
          const el = <span key={g.name} className={`${s.seg} ${s[`c${i % 3}`]}`} style={{ left: w(left), width: w(g.demand_slot_s_per_day) }} />
          left += g.demand_slot_s_per_day
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
      {v.by_os_class.map((g, i) => (
        <span key={g.name}><i className={`${s.sw} ${s[`c${i % 3}`]}`} />{t.classLegend(t.osClass(g.name), hours(g.demand_slot_s_per_day), g.tests_per_day, dur(g.median_test_s))}</span>
      ))}
      <span><i className={s.swLine} />{t.capLegend(hours(v.capacity_slot_s_per_day))}</span>
      {v.capacity_slot_s_per_day < v.demand_slot_s_per_day && <span><i className={`${s.sw} ${s.swGap}`} />{t.gap}</span>}
    </div>
  )
}

function ByPolicy({ v, m, changes }: { v: CapacityView; m: CapacityView | undefined; changes: Record<number, number> }) {
  const t = useT().capacity
  const policies = usePolicies().data ?? []
  const byName = new Map(policies.map((p) => [p.name, p]))
  const modelled = new Map((m?.by_policy ?? []).map((g) => [g.name, g]))
  const share = (g: CapacityGroup) => (v.demand_slot_s_per_day > 0 ? Math.round((g.demand_slot_s_per_day / v.demand_slot_s_per_day) * 100) : 0)
  return (
    <section className={s.sec} aria-labelledby="by-policy">
      <SectionHead id="by-policy" title={t.policies} />
      {v.by_policy.length === 0 ? <p className={s.empty}>{t.noPolicies}</p> : (
        <div className={s.scroll}>
          <table className={s.tbl}>
            <thead>
              <tr>
                <th scope="col">{t.pcol.policy}</th>
                <th scope="col" className={s.num}>{t.pcol.vms}</th>
                <th scope="col">{t.pcol.every}</th>
                <th scope="col" className={s.num}>{t.pcol.tests}</th>
                <th scope="col" className={s.num}>{t.pcol.each}</th>
                <th scope="col" className={s.num}>{t.pcol.hours}</th>
                {m && <th scope="col" className={s.num}>{t.pcol.model}</th>}
              </tr>
            </thead>
            <tbody>
              {v.by_policy.map((g) => {
                const p = byName.get(g.name)
                const iv = p ? changes[p.id] : undefined
                const mg = modelled.get(g.name)
                return (
                  <tr key={g.name}>
                    <td><span className={s.name}>{g.name}</span></td>
                    <td className={s.num}>{g.vms.toLocaleString('en-US')}</td>
                    <td>
                      {dur(p?.test_interval_s)}
                      {iv !== undefined && <span className={s.of}>{t.modelled(dur(iv))}</span>}
                    </td>
                    <td className={s.num}>{Math.round(g.tests_per_day).toLocaleString('en-US')}</td>
                    <td className={s.num}><span className={s.mono}>{dur(g.median_test_s)}</span></td>
                    <td className={s.num}>
                      <b className={s.fig}>{hours(g.demand_slot_s_per_day).toFixed(1)}</b>
                      <span className={s.of}>{t.share(share(g))}</span>
                    </td>
                    {m && <td className={s.num}><b className={mg && mg.demand_slot_s_per_day !== g.demand_slot_s_per_day ? s.changedFig : s.fig}>{mg ? hours(mg.demand_slot_s_per_day).toFixed(1) : '—'}</b></td>}
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

function ExpiringList({ v, loading, onMore }: { v: CapacityView; loading: boolean; onMore: () => void }) {
  const t = useT().capacity
  const fmt = serverTime(v.clock)
  const h = horizon(v)
  return (
    <section className={s.sec} aria-labelledby="expiring">
      <SectionHead id="expiring" title={t.expiring(h)} hint={t.expiringHint} />
      {v.expiring_total === 0 ? <p className={s.empty}>{v.sandbox === null ? t.expiringUnknown : t.noneExpiring}</p> : (
        <>
          <div className={s.scroll}>
            <table className={s.tbl}>
              <thead>
                <tr>
                  <th scope="col">{t.ecol.vm}</th>
                  <th scope="col">{t.ecol.expires}</th>
                  <th scope="col">{t.ecol.next}</th>
                </tr>
              </thead>
              <tbody>
                {v.expiring.map((e) => (
                  <tr key={e.vm_id}>
                    <td><Link className={s.vm} to={`/vms/${e.vm_id}`}>{e.vm}</Link><span className={s.of}>{e.policy}</span></td>
                    <td><b className={s.fig}>{when(fmt, v.clock, e.proof_expires_at, t)}</b></td>
                    <td className={s.fg2}>
                      {e.next_test_start && e.next_test_finish
                        ? t.nextTest(when(fmt, v.clock, e.next_test_start, t), fmt.hhmm(e.next_test_finish))
                        : t.noTest(h)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <MoreRows auto={false} shown={v.expiring.length} total={v.expiring_total}
            more={v.expiring.length < v.expiring_total && v.expiring.length < MAX_LIMIT} loading={loading} onMore={onMore} />
        </>
      )}
    </section>
  )
}

function Queue({ h }: { h: string }) {
  const t = useT().capacity
  const q = useTestQueue()
  const head = q.head
  return (
    <section className={s.sec} aria-labelledby="queue">
      <SectionHead id="queue" title={t.queue} hint={head && t.queueHint(head.total)} />
      {q.isPending ? <Loading /> : q.isError ? <Failed error={q.error} onRetry={() => q.refetch()} /> : (() => {
        const hd = q.data.pages[0]
        const fmt = serverTime(hd.clock)
        const empty = hd.running.length === 0 && hd.waiting_for_admission.length === 0 && q.rows.length === 0
        if (empty) return <p className={s.empty}>{t.queueEmpty}</p>
        return (
          <>
            <div className={s.scroll}>
              <table className={s.tbl}>
                <thead>
                  <tr>
                    <th scope="col" className={`${s.num} ${s.w60}`}>{t.qcol.pos}</th>
                    <th scope="col">{t.qcol.vm}</th>
                    <th scope="col">{t.qcol.why}</th>
                    <th scope="col" className={s.num}>{t.qcol.expected}</th>
                    <th scope="col">{t.qcol.starts}</th>
                  </tr>
                </thead>
                <tbody>
                  {hd.running.map((r) => (
                    <tr key={`run/${r.slot}`}>
                      <td className={`${s.num} ${s.of}`}>{t.run}</td>
                      <td><span className={s.name}>{r.vm}</span><span className={s.of}>{t.slot(r.slot, hd.slots)} · {t.memMiB(r.memory_mib)}</span></td>
                      <td className={s.fg2}>—</td>
                      <td className={s.num}>—</td>
                      <td className={s.running}>{t.runningFor(dur(secondsBetween(r.since, hd.clock.now)))}</td>
                    </tr>
                  ))}
                  {hd.waiting_for_admission.map((w) => (
                    <tr key={`wait/${w.vm}`}>
                      <td className={`${s.num} ${s.of}`}>{t.wait}</td>
                      <td><span className={s.name}>{w.vm}</span><span className={s.of}>{t.memMiB(w.memory_mib)}</span></td>
                      <td className={s.fg2}>{t.waitingFor(w.why)}</td>
                      <td className={s.num}>—</td>
                      <td className={s.mono}>{when(fmt, hd.clock, w.since, t)}</td>
                    </tr>
                  ))}
                  {q.rows.map((r) => <QueueRow key={r.job_id} r={r} fmt={fmt} clock={hd.clock} h={h} />)}
                </tbody>
              </table>
            </div>
            <MoreRows shown={q.rows.length} total={hd.total} more={!!q.hasNextPage} loading={q.isFetchingNextPage} onMore={() => q.fetchNextPage()} />
          </>
        )
      })()}
    </section>
  )
}

function QueueRow({ r, fmt, clock, h }: { r: QueueItem; fmt: Fmt; clock: ServerClock; h: string }) {
  const t = useT().capacity
  // A queued test the simulation does not start within its horizon has no estimated start (the zero time).
  const starts = Date.parse(r.estimated_start) > 0
  return (
    <tr>
      <td className={`${s.num} ${s.of}`}>{r.queue_position}</td>
      <td><Link className={s.vm} to={`/vms/${r.vm_id}`}>{r.vm}</Link><span className={s.of}>{t.job(r.job_id)}</span></td>
      <td className={s.fg2}>{r.order_reason.replace(ISO, (iso) => when(fmt, clock, iso, t))}</td>
      <td className={s.num}><span className={s.mono}>{dur(r.expected_s)}</span><span className={s.of}>{r.duration_basis}</span></td>
      <td className={s.mono}>{starts ? when(fmt, clock, r.estimated_start, t) : t.notInHorizon(h)}</td>
    </tr>
  )
}
