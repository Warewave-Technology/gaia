import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { secondsBetween, serverTime, type Fmt } from '../../lib/time'
import { useFilters } from '../../lib/url'
import { useHosts } from '../api/capacity'
import {
  useJobPages, useJobTotals, useTestQueuePages, type ActivityRow, type ActivityView, type QueueItem, type TestQueueView,
} from '../api/overview'
import { Failed, Loading } from '../layout/States'
import { DataTable, FilterChip, JobState, PageHead, RowToggle, TabPanel, Tabs, TechDetails, Toolbar, type Column } from '../ui'
import { known, queueOrder, scheduled } from './overview/logic'
import { PickChip } from './overview/PickChip'
import s from './ActivityPage.module.css'

type T = ReturnType<typeof useT>['v3']['activity']
type JobCount = ActivityView['open'][number]

const KEYS = ['tab', 'kind', 'state', 'host'] as const
const TABS = ['running', 'queued', 'all'] as const
type TabKey = (typeof TABS)[number]
/** While anything runs or waits the tables follow it closely; otherwise they refresh like the other screens. */
const BUSY = 5_000
const IDLE = 15_000

const n = (v: number) => v.toLocaleString('en-US')
const sum = (cs: readonly JobCount[], state?: string) => cs.filter((c) => !state || c.state === state).reduce((x, c) => x + c.count, 0)

/** The counts of one state by kind, biggest first: [["backup", 38], ["restore_test", 6]]. */
function byKind(cs: readonly JobCount[], state: string): [string, number][] {
  const kinds = new Map<string, number>()
  for (const c of cs) if (c.state === state) kinds.set(c.kind, (kinds.get(c.kind) ?? 0) + c.count)
  return [...kinds].filter(([, x]) => x > 0).sort((a, b) => b[1] - a[1])
}

/** A size in the unit that keeps it short: 820 MiB, 1.4 GiB. */
function size(bytes: number | null) {
  if (bytes === null) return ''
  const units = ['bytes', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let v = bytes, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${i === 0 || v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

/**
 * Activity (docs/design/v2/Activity.dc.html with v3's rules): what runs, what waits and what finished in the last
 * hour, then three tabs — the running jobs, the queued jobs folded by kind (the restore test queue with each test's
 * position and estimated start, ADR-007 C4/C13), and every job newest first, filtered by kind, state and host in the
 * URL. Job states are said in C16's words (JobState), never with a VM status mark.
 */
export function ActivityPage() {
  const t = useT().v3.activity
  const [f, setF] = useFilters(KEYS)
  const [refresh, setRefresh] = useState(IDLE)
  const totals = useJobTotals(refresh)
  const head = totals.data
  const running = head ? sum(head.open, 'running') : 0
  const queued = head ? sum(head.open, 'queued') : 0
  useEffect(() => setRefresh(running + queued > 0 ? BUSY : IDLE), [running, queued])
  if (totals.isPending) return <Loading />
  if (totals.isError) return <Failed error={totals.error} onRetry={() => totals.refetch()} />
  const v = totals.data
  const fmt = serverTime(v.clock)
  const now = Date.parse(v.clock.now)
  // No tab in the URL: what runs if anything does, else what waits, else everything. A filter in a link means "all".
  const tab: TabKey = (TABS as readonly string[]).includes(f.tab) ? (f.tab as TabKey)
    : f.kind || f.state || f.host ? 'all' : running > 0 ? 'running' : queued > 0 ? 'queued' : 'all'
  return (
    <main className={s.page}>
      <Totals v={v} />
      <Tabs label={t.tabs} value={tab} onChange={(k) => setF({ tab: k })}
        hint={<>{t.hint[tab]} · {t.updates(refresh / 1000)}</>}
        tabs={[
          { key: 'running', label: t.tab.running, count: n(running) },
          { key: 'queued', label: t.tab.queued, count: n(queued) },
          { key: 'all', label: t.tab.all, count: n(v.total) },
        ]} />
      <TabPanel id={tab}>
        {tab === 'running' && <Running fmt={fmt} now={now} refresh={refresh} />}
        {tab === 'queued' && <Queued v={v} fmt={fmt} now={now} refresh={refresh} />}
        {tab === 'all' && <AllJobs f={f} setF={setF} fmt={fmt} now={now} refresh={refresh} />}
      </TabPanel>
    </main>
  )
}

function Totals({ v }: { v: ActivityView }) {
  const t = useT().v3.activity
  const running = sum(v.open, 'running')
  const queued = sum(v.open, 'queued')
  const kinds = (state: string) => byKind(v.open, state).map(([k, c]) => t.kindCount(k, c)).join(' · ') || t.nothing
  return (
    <PageHead
      question={t.question}
      answer={running + queued === 0 ? t.idle : t.answer(running, queued)}
      lede={
        <dl className={s.stats} aria-label={t.totals}>
          <div><dt>{t.running}</dt><dd><b>{n(running)}</b><span>{kinds('running')}</span></dd></div>
          <div><dt>{t.queued}</dt><dd><b>{n(queued)}</b><span>{kinds('queued')}</span></dd></div>
          <div><dt>{t.lastHour}</dt><dd><b>{n(sum(v.last_hour))}</b><span>{t.finished(sum(v.last_hour, 'failed'))}</span></dd></div>
        </dl>
      }
    />
  )
}

/** Why a job exists, in words: "scheduled by policy std-linux, every 12 h"; anything else as the server wrote it. */
function jobWhy(r: ActivityRow, t: T) {
  const sch = scheduled(r.reason)
  return sch ? t.why.schedule(sch.policy, dur(sch.every_s)) : r.reason
}

/** What came of a job, said from its fields (rule 3): the raw error is in the row's detail, never here. */
function result(r: ActivityRow, fmt: Fmt, now: number, t: T) {
  const kind = t.kind(r.kind).toLowerCase()
  switch (r.state) {
    case 'queued': return t.result.queued(fmt.at(r.created_at))
    case 'running': return t.result.running(dur(r.started_at ? (now - Date.parse(r.started_at)) / 1000 : null))
    case 'cancelled': return t.result.cancelled
    case 'failed':
      if (r.kind === 'restore_test') return r.verdict === 'CRITICAL' ? t.result.fault : t.result.notRun
      return t.result.failed(kind)
  }
  const parts: string[] = []
  if (r.kind === 'restore_test') {
    parts.push(r.verdict === 'CRITICAL' ? t.result.fault : r.verdict === 'WARNING' ? t.result.warned : r.verdict === 'OK' ? t.result.passed : t.result.notRun)
    if (r.proven_rto_s !== null) parts.push(t.result.ready(dur(r.proven_rto_s, true)))
  } else if (r.point_id !== null) parts.push(t.result.point(r.point_id, size(r.point_bytes)))
  return parts.join(' · ') || t.result.done
}

const took = (r: ActivityRow, now: number) =>
  r.started_at && r.finished_at ? secondsBetween(r.started_at, r.finished_at) : r.started_at && r.state === 'running' ? (now - Date.parse(r.started_at)) / 1000 : null

function Target({ r, t }: { r: ActivityRow; t: T }) {
  return (
    <>
      {r.vm_id !== null ? <Link className={s.vm} to={`/vms/${r.vm_id}`}>{r.vm}</Link> : <span className={s.name}>{r.vm || t.noVM}</span>}
      {r.host && <span className={s.sub}>{r.host}</span>}
    </>
  )
}

/** A job opened: its ids, why it exists, and the raw texts folded. */
function JobDetail({ r, t }: { r: ActivityRow; t: T }) {
  const ids = [t.jobId(r.id), r.test_id !== null && t.ids.test(r.test_id), r.point_id !== null && t.ids.point(r.point_id),
    r.migration_check_id !== null && t.ids.check(r.migration_check_id)].filter(Boolean).join(' · ')
  const raw = [r.error, r.test_reason && r.test_reason !== r.error ? r.test_reason : ''].filter(Boolean).join('\n')
  return (
    <>
      <span className={s.fg2}><b className={s.fg0}>{t.cols.why}:</b> {jobWhy(r, t) || t.why.scheduled}</span>
      <span className={s.sub0}>{ids}{r.by ? ` · ${t.cols.by.toLowerCase()} ${r.by}` : ''}</span>
      {r.vm_id !== null && <span><Link to={`/vms/${r.vm_id}`}>{t.openVM(r.vm)}</Link></span>}
      <TechDetails>{raw}</TechDetails>
    </>
  )
}

function jobColumns(t: T, fmt: Fmt, now: number): Column<ActivityRow>[] {
  return [
    { key: 'state', header: t.cols.state, width: '150px', cell: (r, ctx) => <><RowToggle ctx={ctx} label={t.open} /><JobState state={r.state} verdict={r.verdict} /></> },
    {
      key: 'job', header: t.cols.job, width: '230px', title: (r) => `${t.kind(r.kind)} · ${t.jobId(r.id)}`,
      cell: (r) => <><span className={s.fg0}>{t.kind(r.kind)}</span><span className={s.mono}>{t.jobId(r.id)}</span></>,
    },
    { key: 'target', header: t.cols.target, width: '240px', title: (r) => [r.vm, r.host].filter(Boolean).join(' · '), cell: (r) => <Target r={r} t={t} /> },
    { key: 'result', header: t.cols.result, title: (r) => result(r, fmt, now, t), cell: (r) => <span className={s.fg2}>{result(r, fmt, now, t)}</span> },
    { key: 'started', header: t.cols.started, width: '140px', cell: (r) => <span className={s.mono}>{r.started_at ? fmt.at(r.started_at) : '—'}</span> },
    { key: 'took', header: t.cols.took, numeric: true, width: '100px', cell: (r) => dur(took(r, now), true) },
    { key: 'by', header: t.cols.by, width: '110px', title: (r) => r.by, cell: (r) => <span className={s.sub}>{r.by || t.nobody}</span> },
  ]
}

function Running({ fmt, now, refresh }: { fmt: Fmt; now: number; refresh: number }) {
  const t = useT().v3.activity
  const pages = useJobPages({ state: 'running' }, refresh)
  const cols = jobColumns(t, fmt, now).filter((c) => c.key !== 'took').map((c) => (c.key === 'result' ? { ...c, header: t.cols.runningFor } : c))
  if (pages.isError) return <Failed error={pages.error} onRetry={() => pages.refetch()} />
  return <DataTable caption={t.caption.running} columns={cols} pages={pages} rowKey={(r) => String(r.id)} minWidth={1000} empty={t.emptyRunning}
    detail={(r) => <JobDetail r={r} t={t} />} />
}

type KindRow = { kind: string; count: number }

/** The queued jobs folded by kind: one row per kind with its count; opened, that kind's queue. */
function Queued({ v, fmt, now, refresh }: { v: ActivityView; fmt: Fmt; now: number; refresh: number }) {
  const t = useT().v3.activity
  const rows: KindRow[] = byKind(v.open, 'queued').map(([kind, count]) => ({ kind, count }))
  const tq = useTestQueuePages(refresh)
  const tqHead = (tq.data as unknown as { head?: TestQueueView } | undefined)?.head
  const first = tq.rows[0]
  const cols: Column<KindRow>[] = [
    {
      key: 'kind', header: t.cols.kind, width: '280px',
      cell: (k, ctx) => <><RowToggle ctx={ctx} label={t.open} /><JobState state="queued" /><span className={s.fg0}>{t.kind(k.kind)}</span></>,
    },
    { key: 'count', header: t.cols.count, numeric: true, width: '110px', cell: (k) => <b className={s.fig}>{n(k.count)}</b> },
    {
      key: 'next', header: t.cols.next,
      cell: (k) => k.kind === 'restore_test' && first
        ? <span className={s.fg2}>{t.testQueueNext(first.vm, known(first.estimated_start) ? fmt.at(first.estimated_start) : '')}</span>
        : <span className={s.sub}>{k.kind === 'restore_test' ? '' : t.sinceOldest}</span>,
    },
  ]
  return (
    <DataTable caption={t.caption.queued} columns={cols} rows={rows} rowKey={(k) => k.kind} sizes={false} empty={t.emptyQueued}
      detail={(k) => (k.kind === 'restore_test'
        ? <TestQueue pages={tq} head={tqHead} fmt={fmt} />
        : <KindQueue kind={k.kind} fmt={fmt} now={now} refresh={refresh} />)} />
  )
}

/** The restore test queue in the order the tests start (ADR-007 C4): position, estimated start, why there. */
function TestQueue({ pages, head, fmt }: { pages: ReturnType<typeof useTestQueuePages>; head?: TestQueueView; fmt: Fmt }) {
  const t = useT().v3.activity
  const order = (q: QueueItem) => {
    const o = queueOrder(q.order_reason)
    return o.kind === 'critical' ? t.order.critical : o.kind === 'never' ? t.order.never
      : o.kind === 'expires' && o.at ? t.order.expires(fmt.at(o.at), o.policy ?? '') : q.order_reason
  }
  const cols: Column<QueueItem>[] = [
    { key: 'pos', header: t.cols.pos, numeric: true, width: '64px', cell: (q) => (q.queue_position > 0 ? n(q.queue_position) : '—') },
    { key: 'vm', header: t.cols.vm, width: '220px', title: (q) => q.vm, cell: (q) => <Link className={s.vm} to={`/vms/${q.vm_id}`}>{q.vm}</Link> },
    { key: 'start', header: t.cols.estimated, width: '150px', cell: (q) => <span className={s.mono}>{known(q.estimated_start) ? fmt.at(q.estimated_start) : t.noEstimate}</span> },
    { key: 'expected', header: t.cols.expected, numeric: true, width: '110px', cell: (q) => dur(q.expected_s) },
    { key: 'order', header: t.cols.order, title: order, cell: (q) => <span className={s.fg2}>{order(q)}</span> },
  ]
  if (pages.isError) return <Failed error={pages.error} onRetry={() => pages.refetch()} />
  return (
    <>
      {head && <span className={s.sub0}>{t.slots(head.slots, head.running.length, head.waiting_for_admission.length)}</span>}
      <DataTable caption={t.caption.testQueue} columns={cols} pages={pages} rowKey={(q) => String(q.job_id)} height="320px" empty={t.emptyQueue} />
    </>
  )
}

/** Another kind's queue: the API plans only restore tests ahead, so here the jobs say since when they wait. */
function KindQueue({ kind, fmt, now, refresh }: { kind: string; fmt: Fmt; now: number; refresh: number }) {
  const t = useT().v3.activity
  const pages = useJobPages({ kind, state: 'queued' }, refresh)
  const cols: Column<ActivityRow>[] = [
    { key: 'vm', header: t.cols.target, width: '260px', cell: (r) => <Target r={r} t={t} /> },
    { key: 'at', header: t.cols.queuedAt, width: '150px', cell: (r) => <span className={s.mono}>{fmt.at(r.created_at)}</span> },
    { key: 'wait', header: t.cols.waiting, numeric: true, width: '120px', cell: (r) => dur((now - Date.parse(r.created_at)) / 1000) },
    { key: 'why', header: t.cols.why, title: (r) => jobWhy(r, t), cell: (r) => <span className={s.fg2}>{jobWhy(r, t)}</span> },
  ]
  if (pages.isError) return <Failed error={pages.error} onRetry={() => pages.refetch()} />
  return (
    <>
      <span className={s.sub0}>{t.positionUnknown}</span>
      <DataTable caption={t.caption.kindQueue(t.kind(kind))} columns={cols} pages={pages} rowKey={(r) => String(r.id)} height="320px"
        empty={t.emptyQueue} />
    </>
  )
}

function AllJobs({ f, setF, fmt, now, refresh }: {
  f: Record<(typeof KEYS)[number], string>; setF: (p: Partial<Record<(typeof KEYS)[number], string>>) => void; fmt: Fmt; now: number; refresh: number
}) {
  const t = useT().v3.activity
  const hosts = useHosts().data ?? []
  const filter = useMemo(() => ({ kind: f.kind || undefined, state: f.state || undefined, host_id: f.host ? Number(f.host) : undefined }), [f.kind, f.state, f.host])
  const pages = useJobPages(filter, refresh)
  const filtered = !!(f.kind || f.state || f.host)
  return (
    <>
      <Toolbar>
        {t.kinds.map(([k, label]) => <FilterChip key={k || 'any'} set={f.kind === k} onClick={() => setF({ kind: k })}>{label}</FilterChip>)}
        <span className={s.gap} aria-hidden="true" />
        {t.states.map(([k, label]) => <FilterChip key={k || 'any'} set={f.state === k} onClick={() => setF({ state: k })}>{label}</FilterChip>)}
        <span className={s.gap} aria-hidden="true" />
        <PickChip label={t.cols.target} placeholder={t.pickHost} value={f.host} display={t.pickedHost} clearLabel={t.clearHost}
          options={hosts.map((h) => [String(h.id), h.name] as const)} onChange={(x) => setF({ host: x })} />
      </Toolbar>
      {pages.isError ? <Failed error={pages.error} onRetry={() => pages.refetch()} /> : (
        <DataTable caption={t.caption.all} columns={jobColumns(t, fmt, now)} pages={pages} rowKey={(r) => String(r.id)} minWidth={1100}
          rowState={(r) => (r.state === 'failed' ? 'critical' : undefined)} empty={filtered ? t.noneMatch : t.empty}
          detail={(r) => <JobDetail r={r} t={t} />} />
      )}
    </>
  )
}
