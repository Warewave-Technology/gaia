import { useEffect, useMemo, useState } from 'react'
import { Link, useLocation } from 'react-router'
import { useT } from '../../i18n'
import { reason, type CauseIn } from '../../i18n/en-reasons'
import { dur } from '../../lib/duration'
import { serverTime, type Fmt } from '../../lib/time'
import { useFilters } from '../../lib/url'
import { useCapacitySummary, useFilterNames, useFleet, type FleetView } from '../api/fleet'
import {
  useAttentionPages, useCoveragePages, useVMPages, type AttentionGroup, type CoverageCounts, type CoverageGroup, type LedgerSummary, type VMFilter,
  type VMListRow,
} from '../api/overview'
import { Failed, Loading } from '../layout/States'
import {
  DataTable, FilterChip, Notice, RowToggle, StatusBar, StatusChip, StatusCounts, TabPanel, Tabs, TechDetails, Toolbar, asStatus, type Column,
} from '../ui'
import { ledgerState, where } from './overview/logic'
import { PickChip } from './overview/PickChip'
import s from './Overview.module.css'

const ORDER = ['CRITICAL', 'UNKNOWN', 'WARNING', 'OK'] as const
const KEYS = ['tab', 'show', 'status', 'host', 'policy', 'tag', 'code', 'scope_kind', 'scope_id', 'q', 'by', 'all'] as const
type Filters = Record<(typeof KEYS)[number], string>
type SetFilters = (patch: Partial<Filters>) => void
type T = ReturnType<typeof useT>['v3']['overview']
const TABS = ['attention', 'coverage', 'vms'] as const
type TabKey = (typeof TABS)[number]
/** The filters of the VM table; any of them in a link (the VM page's, the search's) opens the VMs tab. */
const VM_FILTERS = ['show', 'status', 'host', 'policy', 'tag', 'code', 'q'] as const
const CLEAR_VM: Partial<Filters> = { show: '', status: '', host: '', policy: '', tag: '', code: '', scope_kind: '', scope_id: '', q: '' }

const n = (v: number) => v.toLocaleString('en-US')

/** The overview as it is now, filters included: a VM page's breadcrumb leads back to it. */
function useFrom() {
  const l = useLocation()
  return l.pathname + l.search
}

/**
 * The overview (docs/design/v3/Main3.dc.html): the answer — how many VMs can return to a proven point — with the
 * installation's facts and the status tally above the fold, then three tabs: the causes of the VMs that cannot
 * (one row per cause), coverage by host, policy or tag, and every VM, worst first. The tab and every filter are in
 * the URL; every list is one server page at a time (ADR-007 C1, C15).
 */
export function Overview() {
  const q = useFleet()
  const [f, setF] = useFilters(KEYS)
  const t = useT().v3.overview
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const v = q.data
  const tab: TabKey = (TABS as readonly string[]).includes(f.tab) ? (f.tab as TabKey) : VM_FILTERS.some((k) => f[k]) ? 'vms' : 'attention'
  const fmt = serverTime(v.clock)
  // the sentences' "now" (an age in a reason): the server's, as the fleet view last said it
  const now = Date.parse(v.clock.now)
  return (
    <main className={s.page}>
      <Head v={v} f={f} tab={tab} setF={setF} fmt={fmt} />
      <Notices v={v} />
      {v.vms > 0 && (
        <>
          <Tabs label={t.tabs} value={tab} onChange={(k) => setF({ tab: k })} hint={t.hint[tab]}
            tabs={[
              { key: 'attention', label: t.tab.attention, count: v.cannot > 0 ? t.tabCount.attention(v.causes, v.cannot) : '' },
              { key: 'coverage', label: t.tab.coverage },
              { key: 'vms', label: t.tab.vms, count: n(v.vms) },
            ]} />
          <TabPanel id={tab}>
            {tab === 'attention' && <Attention fmt={fmt} now={now} setF={setF} />}
            {tab === 'coverage' && <Coverage f={f} setF={setF} />}
            {tab === 'vms' && <VMs f={f} setF={setF} fmt={fmt} now={now} />}
          </TabPanel>
        </>
      )}
    </main>
  )
}

/** The answer, the facts line, and the status tally as filters (rule 2: all of it above the fold at 1440 × 900). */
function Head({ v, f, tab, setF, fmt }: { v: FleetView; f: Filters; tab: TabKey; setF: SetFilters; fmt: Fmt }) {
  const t = useT().v3.overview
  const cap = useCapacitySummary().data
  const now = Date.parse(v.clock.now)
  const ledger = v.ledger as LedgerSummary
  const ls = ledgerState(ledger, v.can_return > 0, now)
  const at = ledger.checked_at ? fmt.at(ledger.checked_at) : ''
  const running = (v.running ?? []).filter((c) => c.state === 'running' && c.count > 0)
  const queued = (v.running ?? []).filter((c) => c.state === 'queued').reduce((x, c) => x + c.count, 0)
  const kit = v.recovery_kit
  const capStatus = !cap ? null : cap.sandbox === null && cap.sandbox_error ? 'UNKNOWN' : cap.coverage >= 1 ? 'OK' : 'WARNING'
  const pick = (st: string) => {
    const on = tab === 'vms' && f.status === st
    setF({ tab: 'vms', status: on ? '' : st, show: on ? '' : 'all' })
  }
  return (
    <section className={s.head}>
      <div className={s.answer}>
        <p className={s.q}>{t.question}</p>
        <h1 className={s.h1}>
          <span className={s.big}>{n(v.can_return)}</span> <span className={s.rest}>{v.vms === 0 ? t.empty : t.answer(v.vms)}</span>
        </h1>
        {v.uncomputed > 0 && <p className={s.uncomputed}>{t.uncomputed(v.uncomputed)}</p>}
        <dl className={s.facts} aria-label={t.facts}>
          <div>
            <dt><Link to="/capacity">{t.capacity}</Link></dt>
            <dd>
              {cap && capStatus ? (
                <>
                  <StatusChip status={capStatus} size="xs" word={capStatus === 'UNKNOWN' ? undefined : t.capacityPct(Math.floor(cap.coverage * 100))} />
                  {capStatus === 'UNKNOWN' && <span className={s.fs}>{t.capacityNoSandbox}</span>}
                </>
              ) : <span className={s.fs}>…</span>}
            </dd>
          </div>
          <div>
            <dt><Link to="/evidence">{t.ledger}</Link></dt>
            <dd>
              <StatusChip status={ls.status} size="xs" />
              <span className={s.fs}>{(t.ledgerSays[ls.code] ?? t.ledgerSays.ok)(at, ledger.unchecked)}</span>
            </dd>
          </div>
          <div>
            <dt>{t.kit}</dt>
            <dd title={kit.text}>
              <StatusChip status={kit.state === 'confirmed' ? 'OK' : 'WARNING'} size="xs" />
              <span className={s.fs}>{(t.kitSays[kit.state] ?? t.kitSays.missing)(kit.generation, kit.confirmed_at ? fmt.at(kit.confirmed_at) : '')}</span>
            </dd>
          </div>
          <div>
            <dt><Link to="/activity">{t.running}</Link></dt>
            <dd><b>{t.runningWhat(running.map((c) => t.kindCount(c.kind, c.count)).join(', '), queued)}</b></dd>
          </div>
        </dl>
      </div>
      {v.vms > 0 && (
        <div className={s.tally} role="group" aria-label={t.tally}>
          {ORDER.map((st) => (
            <button key={st} type="button" className={`${s.tile} ${s[`t${st}`]}`} aria-pressed={tab === 'vms' && f.status === st} onClick={() => pick(st)}>
              <b>{n(v.by_status[st] ?? 0)}</b>
              <StatusChip status={st} size="sm" />
            </button>
          ))}
        </div>
      )}
    </section>
  )
}

/** What is wrong with the installation rather than with a VM: proof capacity short (C13), the ledger (C14),
 * notifications. Each sentence is built from fields, never the server's text. */
function Notices({ v }: { v: FleetView }) {
  const t = useT().v3.overview
  const cap = useCapacitySummary().data
  const ls = ledgerState(v.ledger as LedgerSummary, v.can_return > 0, Date.parse(v.clock.now))
  const nt = v.notifications
  return (
    <>
      {cap && cap.coverage < 1 && cap.demand_slot_s_per_day > 0 && (
        <Notice tone="warning" action={<Link to="/capacity">{t.planCapacity}</Link>}>
          {cap.sandbox === null && cap.sandbox_error ? `${t.capacityShortNoSandbox} ` : ''}
          {t.capacityShort(Math.floor(cap.coverage * 100), cap.slots_needed, cap.slots)}
          {cap.expiring_total > 0 && ` ${t.capacityExpiring(cap.expiring_total)}`}
        </Notice>
      )}
      {ls.status === 'CRITICAL' && t.ledgerNotice[ls.code] && (
        <Notice tone="critical" action={<Link to="/evidence">{t.openEvidence}</Link>}>{t.ledgerNotice[ls.code]}</Notice>
      )}
      {nt.state !== 'OK' && (
        <Notice tone="warning">{nt.failing.length > 0 ? t.notifyFailing(nt.failing) : t.notifyOff}</Notice>
      )}
    </>
  )
}

function Attention({ fmt, now, setF }: { fmt: Fmt; now: number; setF: SetFilters }) {
  const t = useT().v3.overview
  const pages = useAttentionPages()
  const cause = (g: AttentionGroup): CauseIn => ({ code: g.code, scope: g.scope, count: g.count, since: g.since ? fmt.at(g.since) : '—', hosts: g.hosts, policies: g.policies })
  const cols: Column<AttentionGroup>[] = [
    { key: 'status', header: t.cols.status, width: '128px', cell: (g) => <StatusChip status={g.status} size="sm" /> },
    {
      key: 'cause', header: t.cols.cause, title: (g) => `${reason(g.code).title}: ${reason(g.code).short(cause(g), now)}`,
      cell: (g) => <><b className={s.title}>{reason(g.code).title}</b><span className={s.sub}>{reason(g.code).short(cause(g), now)}</span></>,
    },
    { key: 'vms', header: t.cols.vms, numeric: true, width: '80px', cell: (g) => <b className={s.fig}>{n(g.count)}</b> },
    { key: 'where', header: t.cols.where, width: '260px', title: (g) => where(g, t), cell: (g) => <span className={s.fg2}>{where(g, t)}</span> },
    { key: 'since', header: t.cols.since, width: '130px', cell: (g) => <span className={s.mono}>{g.since ? fmt.at(g.since) : '—'}</span> },
    { key: 'open', header: '', width: '44px', cell: (_g, ctx) => <RowToggle ctx={ctx} label={t.open} /> },
  ]
  if (pages.isError) return <Failed error={pages.error} onRetry={() => pages.refetch()} />
  return (
    <DataTable caption={t.causes} columns={cols} pages={pages} rowKey={(g) => `${g.code}/${g.scope?.kind ?? ''}/${g.scope?.id ?? 0}`}
      rowState={(g) => (asStatus(g.status) === 'CRITICAL' ? 'critical' : asStatus(g.status) === 'UNKNOWN' ? 'bad' : undefined)}
      empty={t.noneNeedAttention}
      detail={(g) => {
        const def = reason(g.code)
        return (
          <>
            <span className={s.long}>{def.long(cause(g), now)}</span>
            {def.todo && <span className={s.todo}><b>{t.whatToDo}</b> {def.todo}</span>}
            <span>
              <button type="button" className={s.link}
                onClick={() => setF({ ...CLEAR_VM, tab: 'vms', show: 'all', code: g.code, scope_kind: g.scope?.kind ?? '', scope_id: g.scope ? String(g.scope.id) : '' })}>
                {t.showVMs(g.count)}
              </button>
            </span>
            <TechDetails summary={t.example(g.example.vm)}>{g.example.reason}</TechDetails>
          </>
        )
      }} />
  )
}

function Coverage({ f, setF }: { f: Filters; setF: SetFilters }) {
  const t = useT().v3.overview
  const by = f.by === 'policy' || f.by === 'tag' ? f.by : 'host'
  const all = f.all === 'true'
  const pages = useCoveragePages(by, all)
  const counts = pages.data as unknown as CoverageCounts | undefined
  const word = t.by[by]
  const open = (g: CoverageGroup) => setF({
    ...CLEAR_VM, tab: 'vms', show: 'all',
    host: by === 'host' ? String(g.id) : '', policy: by === 'policy' ? String(g.id) : '', tag: by === 'tag' ? g.name : '',
  })
  const cols: Column<CoverageGroup>[] = [
    {
      key: 'name', header: word, width: '220px', title: (g) => g.name,
      cell: (g) => (by === 'policy' && !g.id) || !g.name
        ? <span className={s.mono}>{g.name || t.noPolicy}</span>
        : <button type="button" className={s.name} onClick={() => open(g)}>{g.name}</button>,
    },
    { key: 'vms', header: t.cols.vms, numeric: true, width: '80px', cell: (g) => <b className={s.fig}>{n(g.vms)}</b> },
    {
      key: 'every', header: t.cols.every,
      cell: (g) => <span className={s.bar}><StatusBar counts={g.by_status} /><StatusCounts counts={g.by_status} /></span>,
    },
    {
      key: 'rpo', header: t.cols.worstRpo, width: '200px',
      cell: (g) => {
        const w = g.worst_proven_rpo
        if (!w) return <span className={s.none}>{t.nothingProven}</span>
        const over = w.rpo_target_s !== null && w.proven_rpo_s > w.rpo_target_s
        return <><b className={over ? s.over : s.fig}>{dur(w.proven_rpo_s)}</b><span className={s.sub}>{w.rpo_target_s !== null ? t.ofTarget(dur(w.rpo_target_s)) : t.noTarget}</span></>
      },
    },
  ]
  return (
    <>
      <Toolbar end={all ? t.healthyShown : t.healthyHidden}>
        <span className={s.label}>{t.groupBy}</span>
        {(['host', 'policy', 'tag'] as const).map((b) => (
          <FilterChip key={b} set={by === b} onClick={() => setF({ by: b === 'host' ? '' : b })}>{t.by[b]}</FilterChip>
        ))}
      </Toolbar>
      {pages.isError ? <Failed error={pages.error} onRetry={() => pages.refetch()} /> : (
        <DataTable caption={t.coverage(word)} columns={cols} pages={pages} rowKey={(g) => `${g.id}/${g.name}`} empty={t.allOk(word)} />
      )}
      {counts && (
        <p className={s.foot}>
          {t.covFoot(all ? counts.groups : (pages.total ?? 0), counts.healthy, all)}{' '}
          {counts.healthy > 0 && <button type="button" className={s.link} onClick={() => setF({ all: all ? '' : 'true' })}>{all ? t.hideHealthy : t.showHealthy}</button>}
        </p>
      )}
    </>
  )
}

/** The search box: typing changes the URL a moment after the last key, so the table asks once. */
function SearchBox({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  const [text, setText] = useState(value)
  useEffect(() => setText(value), [value])
  useEffect(() => {
    if (text.trim() === value) return
    const h = setTimeout(() => onChange(text.trim()), 250)
    return () => clearTimeout(h)
  }, [text, value, onChange])
  return <input type="search" placeholder={label} aria-label={label} value={text} onChange={(e) => setText(e.target.value)} />
}

function VMs({ f, setF, fmt, now }: { f: Filters; setF: SetFilters; fmt: Fmt; now: number }) {
  const t = useT().v3.overview
  const names = useFilterNames()
  const from = useFrom()
  const attention = f.show !== 'all' && !f.status
  const filter: VMFilter = useMemo(() => ({
    attention: attention || undefined,
    status: f.status || undefined,
    host_id: f.host ? Number(f.host) : undefined,
    policy_id: f.policy ? Number(f.policy) : undefined,
    tag: f.tag || undefined,
    code: f.code || undefined,
    scope_kind: f.code ? f.scope_kind || undefined : undefined,
    scope_id: f.code && f.scope_id ? Number(f.scope_id) : undefined,
    q: f.q || undefined,
  }), [attention, f.status, f.host, f.policy, f.tag, f.code, f.scope_kind, f.scope_id, f.q])
  const pages = useVMPages(filter)
  const setQ = useMemo(() => (q: string) => setF({ q }), [setF])
  const cols = vmColumns(t, fmt, now, from)
  const status = attention ? 'attention' : f.status || 'all'
  const scopeName = f.code && f.scope_kind ? scopeLabel(f, names, t) : ''
  return (
    <>
      <Toolbar end={t.sorted}>
        <SearchBox value={f.q} onChange={setQ} label={t.filter} />
        {(['attention', 'all', ...ORDER] as const).map((k) => (
          <FilterChip key={k} set={status === k}
            onClick={() => setF(k === 'attention' ? { status: '', show: '' } : k === 'all' ? { status: '', show: 'all' } : { status: status === k ? '' : k, show: status === k ? '' : 'all' })}>
            {t.chips[k]}
          </FilterChip>
        ))}
        <PickChip label={t.by.host} placeholder={t.pick.host} value={f.host} display={t.picked.host} clearLabel={t.clear(t.by.host.toLowerCase())}
          options={names.hosts.map((h) => [String(h.id), h.name] as const)} onChange={(x) => setF({ host: x, show: f.show || (x ? 'all' : '') })} />
        <PickChip label={t.by.policy} placeholder={t.pick.policy} value={f.policy} display={t.picked.policy} clearLabel={t.clear(t.by.policy.toLowerCase())}
          options={names.policies.map((p) => [String(p.id), p.name] as const)} onChange={(x) => setF({ policy: x, show: f.show || (x ? 'all' : '') })} />
        <PickChip label={t.by.tag} placeholder={t.pick.tag} value={f.tag} display={t.picked.tag} clearLabel={t.clear(t.by.tag.toLowerCase())}
          options={names.tags.map((g) => [g, g] as const)} onChange={(x) => setF({ tag: x, show: f.show || (x ? 'all' : '') })} />
        {f.code && (
          <FilterChip set onClick={() => setF({ code: '', scope_kind: '', scope_id: '' })} title={t.clear(reason(f.code).title)}>
            {t.cause(reason(f.code).title, scopeName)} <span aria-hidden="true">×</span>
          </FilterChip>
        )}
      </Toolbar>
      {pages.isError ? <Failed error={pages.error} onRetry={() => pages.refetch()} /> : (
        <div id="vms">
          <DataTable caption={t.vmTable} columns={cols} pages={pages} rowKey={(r) => String(r.vm_id)} minWidth={1100}
            rowState={(r) => (asStatus(r.status) === 'CRITICAL' ? 'critical' : asStatus(r.status) === 'UNKNOWN' ? 'bad' : undefined)}
            empty={attention && !f.host && !f.policy && !f.tag && !f.code && !f.q ? t.noneNeedAttention : t.noneMatch}
            detail={(r) => <VMDetail r={r} now={now} from={from} />} />
        </div>
      )}
    </>
  )
}

/** The scope of a cause filter in words, from the names the filters know ("host kvm-14"). */
function scopeLabel(f: Filters, names: ReturnType<typeof useFilterNames>, t: T) {
  const id = Number(f.scope_id)
  const name = f.scope_kind === 'host' ? names.hosts.find((h) => h.id === id)?.name : f.scope_kind === 'policy' ? names.policies.find((p) => p.id === id)?.name : undefined
  return t.scope(f.scope_kind, name ?? `#${f.scope_id}`)
}

const why = (r: VMListRow, now: number) => (asStatus(r.status) === 'OK' && r.code === 'proven' ? '' : reason(r.code).vm({ code: r.code, params: r.params, scope: r.scope }, now))

function vmColumns(t: T, fmt: Fmt, now: number, from: string): Column<VMListRow>[] {
  return [
    { key: 'status', header: t.cols.status, width: '124px', cell: (r) => <StatusChip status={r.status} size="sm" /> },
    {
      key: 'vm', header: t.cols.vm, width: '230px', title: (r) => `${r.vm} · ${r.host}${r.policy ? ` · ${r.policy}` : ''}`,
      cell: (r) => <><Link className={s.vm} to={`/vms/${r.vm_id}`} state={{ from }}>{r.vm}</Link><span className={s.sub}>{r.host}</span></>,
    },
    {
      key: 'ret', header: t.cols.ret, width: '170px',
      cell: (r) => r.proven ? <span className={s.fg2}>{fmt.at(r.proven.point_taken_at)}</span> : <span className={s.none}>{t.nothingProven}</span>,
    },
    {
      key: 'rpo', header: t.cols.rpo, width: '200px',
      cell: (r) => {
        const tg = r.targets
        const over = !!tg && r.proven_rpo_s !== null && r.proven_rpo_s > tg.rpo_target_s
        const w = tg && r.proven_rpo_s !== null ? Math.min(1, r.proven_rpo_s / tg.rpo_target_s) : 0
        return (
          <>
            <span className={s.rb} aria-hidden="true"><i className={over ? s.rbBad : undefined} style={{ width: `${Math.round(w * 100)}%` }} /></span>
            <b className={over ? s.over : s.val}>{dur(r.proven_rpo_s)}</b>
            {tg && <span className={s.sub}>{t.ofTarget(dur(tg.rpo_target_s))}</span>}
          </>
        )
      },
    },
    {
      key: 'rto', header: t.cols.rto, numeric: true, width: '90px',
      cell: (r) => <span className={r.targets && r.proven_rto_s !== null && r.proven_rto_s > r.targets.rto_target_s ? s.over : undefined}>{dur(r.proven_rto_s, true)}</span>,
    },
    {
      key: 'age', header: t.cols.age, numeric: true, width: '110px',
      cell: (r) => <span className={r.targets && r.proof_age_s !== null && r.proof_age_s > r.targets.max_proof_age_s ? s.over : undefined}>{dur(r.proof_age_s)}</span>,
    },
    { key: 'why', header: t.cols.why, title: (r) => why(r, now) || undefined, cell: (r) => <span className={s.fg2}>{why(r, now)}</span> },
  ]
}

/** A VM row opened: its reason and notes in words, its targets, the raw text folded, the way to its page. */
function VMDetail({ r, now, from }: { r: VMListRow; now: number; from: string }) {
  const t = useT().v3.overview
  const w = why(r, now)
  const notes = r.notes.map((x) => reason(x.code).vm(x, now))
  const raw = [r.reason, ...r.notes.map((x) => x.text)].filter(Boolean).join('\n')
  return (
    <>
      {w && <span className={s.long}>{w === reason(r.code).title ? <b>{w}.</b> : <><b>{reason(r.code).title}:</b> {w}.</>}</span>}
      {notes.length > 0 && <span className={s.todo}><b>{t.notes}:</b> {notes.join(' · ')}</span>}
      <span className={s.sub0}>
        {r.host}{r.policy ? ` · ${r.policy}` : ` · ${t.noPolicy}`}{r.tags.length > 0 && ` · ${r.tags.join(', ')}`}
        {r.targets && ` · ${t.targets(dur(r.targets.rpo_target_s), dur(r.targets.rto_target_s), dur(r.targets.max_proof_age_s))}`}
      </span>
      <span><Link to={`/vms/${r.vm_id}`} state={{ from }}>{t.openVM(r.vm)}</Link></span>
      <TechDetails>{raw}</TechDetails>
    </>
  )
}
