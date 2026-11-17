import { Link, useParams } from 'react-router'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { secondsBetween, serverTime, type Fmt } from '../../lib/time'
import { ApiError } from '../api/errors'
import { can, useMe } from '../api/session'
import { useVMAction, useVMView, type TestView, type VMView } from '../api/views'
import { Failed, Loading } from '../layout/States'
import { Button, DataTable, HistoryStrip, Legend, Notice, ProofBar, RowToggle, StatusChip, asStatus, by, type Column } from '../ui'
import s from './VMPage.module.css'

/** One VM (L3): where it can return to, how that is proven, and the record behind it. */
export function VMPage() {
  const id = Number(useParams().id)
  const q = useVMView(id)
  const t = useT()
  if (q.isPending) return <Loading />
  if (q.isError) {
    if (q.error instanceof ApiError && q.error.status === 404) return <main className={s.vmp}><Notice tone="warning">{t.vm.notFound}</Notice></main>
    return <Failed error={q.error} onRetry={() => q.refetch()} />
  }
  return <VMBody v={q.data} />
}

type Check = { name: string; verdict: string; seconds: number; detail: string }
type Detail = { restore_s?: number; checks?: Check[] }

function detailOf(t: TestView): Detail {
  return (t.detail && typeof t.detail === 'object' ? t.detail : {}) as Detail
}

function VMBody({ v }: { v: VMView }) {
  const t = useT().vm
  const tt = useT()
  const me = useMe().data
  const fmt = serverTime(v.clock)
  const st = v.status
  const pol = v.policy
  const hours = Math.round(v.window_s / 3600)
  const latest = v.tests.find((x) => x.finished_at)
  const at = (iso: string) => `${fmt.date(iso)} ${fmt.clock(iso)}`
  return (
    <main className={s.vmp}>
      <nav className={s.crumbs} aria-label={t.crumbs}><Link to="/">{tt.nav.overview}</Link><span aria-hidden="true">/</span><span>{v.vm.name}</span></nav>

      <section className={s.head}>
        <div className={s.id}>
          <h1>{v.vm.name}</h1>
          <StatusChip status={st.status} size="lg" />
          <p className={s.sub}>
            {t.since(asStatus(st.status), at(st.changed_at))} {t.where(v.host.name, v.host.address)}
            {pol && <>, {t.policy} <b>{pol.name}</b></>}{v.repository && <>, {t.repository} <b>{v.repository.name}</b></>}.
          </p>
        </div>
        <div className={s.answer}>
          {st.proven ? (
            <>
              <span className={s.lbl}>{t.returnTo}</span>
              <span className={s.when}>{fmt.clock(st.proven.point_taken_at)}</span>
              <span className={s.what}>
                {t.returnWhat(fmt.date(st.proven.point_taken_at), st.proven.point_id, st.proven.test_id, dur(st.proven_rto_s, true),
                  dur(secondsBetween(st.proven.test_finished_at, v.clock.now)))}
              </span>
            </>
          ) : (
            <>
              <span className={s.lbl}>{t.returnTo}</span>
              <span className={`${s.when} ${s.nothing}`}>{t.nothing}</span>
              <span className={s.what}>{t.nothingWhat}</span>
            </>
          )}
        </div>
        {pol && (
          <div className={s.kbs}>
            <Bullet label={t.provenRpo} value={st.proven_rpo_s} target={pol.rpo_target_s} word={tt.overview.target} />
            <Bullet label={t.theoreticalRpo} hint={t.newestHint} value={st.theoretical_rpo_s} target={pol.rpo_target_s} word={tt.overview.target} />
            <Bullet label={t.provenRto} value={st.proven_rto_s} target={pol.rto_target_s} word={tt.overview.target} />
            <Bullet label={t.proofAge} value={st.proof_age_s} target={pol.max_proof_age_s} word={tt.overview.max} />
          </div>
        )}
        {can(me, 'act') && <Actions id={v.vm.id} />}
      </section>

      <section className={s.hist}>
        <h2>{t.history(hours)}{pol && <span className={s.h2s}>{t.cadence(dur(pol.backup_interval_s), dur(pol.test_interval_s), pol.keep_last)}</span>}</h2>
        <div className={s.bigstrip}>
          <HistoryStrip events={st.events} now={v.clock.now} windowS={v.window_s} returnAt={st.proven?.point_taken_at} width={1080} height={60} big />
          <div className={s.hours} aria-hidden="true">
            {Array.from({ length: hours + 1 }, (_, k) => {
              const iso = new Date(Date.parse(v.clock.now) - (hours - k) * 3600_000).toISOString()
              return <span key={k} style={{ left: `${(100 * k) / hours}%` }}>{fmt.hhmm(iso)}</span>
            })}
          </div>
        </div>
        <Legend />
      </section>

      <div className={s.two}>
        <section>
          <h2>{t.latestTest}{latest && <span className={s.h2s}>{t.testOf(latest.id, latest.point_id, at(latest.finished_at))}</span>}</h2>
          {latest ? <LatestTest test={latest} rtoTarget={pol?.rto_target_s ?? null} /> : <p className={s.muted}>{t.noTests}</p>}
        </section>
        <Evidence v={v} fmt={fmt} />
      </div>

      <StatusHistory v={v} fmt={fmt} />

      <div className={s.two}>
        <section>
          <h2>{t.points}<span className={s.h2s}>{t.pointsSub(v.points.length, v.points_total - v.points.length)}</span></h2>
          <PointsTable v={v} fmt={fmt} />
        </section>
        <section>
          <h2>{t.tests}<span className={s.h2s}>{t.testsSub}</span></h2>
          <TestsTable v={v} fmt={fmt} />
        </section>
      </div>
    </main>
  )
}

function Bullet({ label, hint, value, target, word }: { label: string; hint?: string; value: number | null; target: number; word: (d: string) => string }) {
  const over = value !== null && value > target
  return (
    <div className={s.kb}>
      <span className={s.kl}>{label}{hint && <span className={s.hint}>{hint}</span>}</span>
      <span className={s.kv}><b className={over ? s.over : ''}>{dur(value, true)}</b><span className={s.of}>{word(dur(target))}</span></span>
      <ProofBar value={value} target={target} width={250} label={label} />
    </div>
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
      <Button onClick={() => backup.mutate()} disabled={backup.isPending}>{t.backupNow}</Button>
      <Button onClick={() => test.mutate()} disabled={test.isPending}>{t.testNow}</Button>
      {done && <span className={s.muted}>{t.queued(done === backup.data ? t.backupNow : t.testNow, done.id)}</span>}
      {err && <Notice tone="critical">{err.message}</Notice>}
    </div>
  )
}

function LatestTest({ test, rtoTarget }: { test: TestView; rtoTarget: number | null }) {
  const t = useT().vm
  const d = detailOf(test)
  const checks = d.checks ?? []
  const boot = checks.find((c) => c.name === 'boot')?.seconds ?? null
  const restore = d.restore_s ?? null
  const rto = test.proven_rto_s
  return (
    <>
      {test.screenshots.length > 0 && test.evidence_id > 0 && (
        <div className={s.shots}>
          {test.screenshots.map((n) => (
            <figure key={n}>
              <img src={`/api/v1/evidence/${test.evidence_id}/files/${encodeURIComponent(n)}`} width={720} height={400} alt={t.shot(n)} />
              <figcaption>{n.replace(/^screenshot-|\.png$/g, '')}</figcaption>
            </figure>
          ))}
        </div>
      )}
      {rto !== null && restore !== null && boot !== null && (
        <div className={s.rto}>
          <span className={s.lbl}>{t.rto} <b>{dur(rto, true)}</b>{rtoTarget && <span className={s.of}> {t.ofTarget(dur(rtoTarget))}</span>}</span>
          <span className={s.track}>
            <span className={s.split} style={{ width: `${Math.min(100, (100 * rto) / (rtoTarget ?? rto))}%` }}>
              <span className={s.rs} style={{ width: `${(100 * restore) / rto}%` }} />
              <span className={s.bs} style={{ width: `${100 - (100 * restore) / rto}%` }} />
            </span>
          </span>
          <span className={s.keys}><span className={`${s.k} ${s.krs}`}>{t.restore} {dur(restore, true)}</span><span className={`${s.k} ${s.kbs}`}>{t.boot} {dur(boot, true)}</span></span>
        </div>
      )}
      {test.verdict !== 'OK' && <Notice tone={test.verdict === 'CRITICAL' ? 'critical' : 'warning'}>{test.reason}</Notice>}
      <Checks checks={checks} />
    </>
  )
}

function Evidence({ v, fmt }: { v: VMView; fmt: Fmt }) {
  const t = useT().vm
  const b = v.latest_bundle
  const latest = v.tests.find((x) => x.finished_at)
  const rep = v.daily_report
  return (
    <section>
      <h2>{t.evidence}</h2>
      {b && latest ? (
        <dl className={s.ev}>
          <div><dt>{t.bundle}</dt><dd><code>{b.bundle_id}</code></dd></div>
          <div><dt>{t.signature}</dt><dd><StatusChip status={b.verdict} size="xs" />{b.verdict === 'OK' ? t.signed(b.files) : b.problems.join('; ')}</dd></div>
          <div><dt>{t.signedBy}</dt><dd><code title={b.key_id}>{b.key_id.slice(0, 16)}…</code></dd></div>
          <div>
            <dt>{t.ledgerRow}</dt>
            <dd>{t.ledgerEntry(latest.ledger_seq, v.ledger.checked_at ? fmt.hhmm(v.ledger.checked_at) : '—', v.ledger.entries)}<StatusChip status={v.ledger.verdict} size="xs" /></dd>
          </div>
          <div><dt>{t.anchor}</dt><dd>{rep ? t.anchorText(rep.id, `${fmt.date(rep.generated_at)} ${fmt.hhmm(rep.generated_at)}`, rep.head_seq) : t.noAnchor}</dd></div>
        </dl>
      ) : (
        <p className={s.muted}>{t.noTests}</p>
      )}
      <div className={s.cmd}>
        <span>{t.verifyOffline}</span>
        <code>gaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles{rep ? ` -head ledger-head-${rep.id}.json` : ''}</code>
      </div>
    </section>
  )
}

// the order a status column sorts in: the worst first when descending
const ORDER = ['OK', 'WARNING', 'UNKNOWN', 'CRITICAL'] as const

function StatusHistory({ v, fmt }: { v: VMView; fmt: Fmt }) {
  const t = useT().vm
  const tt = useT()
  const h = v.status_history
  if (h.length === 0) return null
  const first = h[0].at
  const total = Math.max(secondsBetween(first, v.clock.now), 1)
  const segs = h.map((c, i) => ({ c, end: i + 1 < h.length ? h[i + 1].at : v.clock.now }))
  type Row = (typeof segs)[number] & { last: boolean; s: number }
  const rows: Row[] = segs.map((x, i) => ({ ...x, last: i === segs.length - 1, s: secondsBetween(x.c.at, x.end) })).reverse()
  const cols: Column<Row>[] = [
    { key: 'at', header: t.col.when, numeric: true, compare: by((r) => r.c.at), cell: (r, ctx) => <><RowToggle ctx={ctx} label={tt.table.details} />{fmt.date(r.c.at)} {fmt.clock(r.c.at)}</> },
    {
      key: 'change', header: t.col.change, compare: by((r) => ORDER.indexOf(asStatus(r.c.to))),
      cell: (r) => (
        <span className={s.tr}>
          {r.c.from ? <StatusChip status={r.c.from} size="xs" /> : <span className={s.new}>{t.added}</span>}
          <span className={s.ar} aria-label="to">→</span>
          <StatusChip status={r.c.to} size="xs" />
        </span>
      ),
    },
    { key: 'for', header: t.col.lasted, numeric: true, compare: by((r) => r.s), cell: (r) => <span className={s.for}>{r.last ? t.soFar(dur(r.s)) : dur(r.s)}</span> },
    { key: 'why', header: t.col.why, clip: true, cell: (r) => <span title={r.c.reason}>{r.c.reason}</span> },
  ]
  return (
    <section className={s.sh}>
      <h2>{t.statusHistory}<span className={s.h2s}>{t.sinceAdded}</span></h2>
      <div className={s.band} aria-hidden="true">
        {segs.map(({ c, end }, i) => (
          <span key={i} className={`${s.seg} ${s[`seg${asStatus(c.to)}`]}`}
            style={{ flex: Math.max(secondsBetween(c.at, end) / total, 0.012) }}
            title={`${c.to} ${fmt.date(c.at)} ${fmt.clock(c.at)}, ${dur(secondsBetween(c.at, end))}`} />
        ))}
      </div>
      <div className={s.bandaxis} aria-hidden="true"><span>{fmt.date(first)} {fmt.hhmm(first)}</span><span>{fmt.date(v.clock.now)} {fmt.hhmm(v.clock.now)}</span></div>
      <DataTable caption={t.statusHistory} columns={cols} rows={rows} rowKey={(r) => String(r.c.seq)} pageSize={8}
        detail={(r) => <p className={s.reasonFull}>{r.c.reason}</p>}
        rowState={(r) => (r.c.to === 'CRITICAL' ? 'critical' : r.c.to === 'OK' ? undefined : 'bad')} />
    </section>
  )
}

type PointRow = VMView['points'][number]

function PointsTable({ v, fmt }: { v: VMView; fmt: Fmt }) {
  const t = useT().vm
  const tested = new Map(v.tests.filter((x) => x.verdict === 'OK').map((x) => [x.point_id, x.id]))
  const ret = v.status.proven?.point_id
  const check = (p: PointRow) => (p.check_state === 'ok' ? 'OK' : p.check_state === 'failed' ? 'CRITICAL' : 'UNKNOWN')
  const cols: Column<PointRow>[] = [
    { key: 'id', header: t.col.point, numeric: true, compare: by((p) => p.id), cell: (p) => p.id },
    { key: 'taken', header: t.col.taken, numeric: true, compare: by((p) => p.taken_at), cell: (p) => `${fmt.date(p.taken_at)} ${fmt.clock(p.taken_at)}` },
    { key: 'kind', header: t.col.kind, compare: by((p) => p.kind), cell: (p) => p.kind },
    { key: 'size', header: t.col.size, numeric: true, compare: by((p) => p.bytes), cell: (p) => `${Math.round(p.bytes / 1048576)} MiB` },
    { key: 'check', header: t.col.check, compare: by(check), cell: (p) => <StatusChip status={check(p)} size="xs" /> },
    {
      key: 'test', header: t.col.test, cell: (p) => (
        <>
          {tested.has(p.id) ? <span className={s.proved}>test {tested.get(p.id)}</span> : <span className={s.muted}>{t.notTested}</span>}
          {p.id === ret && <span className={s.rmark}>{t.returnHere}</span>}
        </>
      ),
    },
  ]
  return <DataTable caption={t.pointsCaption} columns={cols} rows={v.points} rowKey={(p) => String(p.id)}
    rowState={(p) => (p.id === ret ? 'mark' : check(p) === 'CRITICAL' ? 'critical' : undefined)} />
}

function TestsTable({ v, fmt }: { v: VMView; fmt: Fmt }) {
  const t = useT().vm
  const tt = useT()
  const ev = (x: TestView) => (x.evidence === 'checked' && v.ledger.checked_at ? t.ev.checked(fmt.hhmm(v.ledger.checked_at)) : t.ev[x.evidence as Exclude<keyof typeof t.ev, 'checked'>])
  const cols: Column<TestView>[] = [
    { key: 'id', header: t.col.id, compare: by((x) => x.id), cell: (x, ctx) => <><RowToggle ctx={ctx} label={tt.table.details} /><span className={s.tnum}>{x.id}</span></> },
    { key: 'point', header: t.col.point, numeric: true, compare: by((x) => x.point_id), cell: (x) => x.point_id },
    {
      key: 'result', header: t.col.result, compare: by((x) => x.verdict || 'running'),
      cell: (x) => (x.finished_at ? <StatusChip status={x.verdict} size="xs" /> : <span className={s.running}>{t.running}</span>),
    },
    { key: 'rto', header: t.col.rto, numeric: true, compare: by((x) => x.proven_rto_s), cell: (x) => dur(x.proven_rto_s) },
    {
      key: 'finished', header: t.col.finished, numeric: true, compare: by((x) => x.finished_at || x.started_at),
      cell: (x) => (x.finished_at ? `${fmt.date(x.finished_at)} ${fmt.clock(x.finished_at)}` : <span className={s.muted}>{t.runningSince(fmt.clock(x.started_at))}</span>),
    },
    {
      key: 'evidence', header: t.col.evidence,
      cell: (x) => (
        <>
          {x.evidence === 'verified' || x.evidence === 'checked' ? <StatusChip status="OK" size="xs" /> : x.evidence === 'invalid' ? <StatusChip status="CRITICAL" size="xs" /> : null}
          <span className={x.evidence === 'not_checked' ? s.pending : s.muted}>{x.finished_at ? ev(x) : ''}</span>
        </>
      ),
    },
  ]
  return <DataTable caption={t.testsCaption} columns={cols} rows={v.tests} rowKey={(x) => String(x.id)}
    rowState={(x) => (x.finished_at && x.verdict !== 'OK' ? (x.verdict === 'CRITICAL' ? 'critical' : 'bad') : undefined)}
    detail={(x) => (x.finished_at ? <TestDetail test={x} /> : null)} />
}

/** What a restore test's row opens to: why it did not pass, and every check with its time. */
function TestDetail({ test }: { test: TestView }) {
  const t = useT().vm
  const d = detailOf(test)
  const checks = d.checks ?? []
  return (
    <div className={s.tdetail}>
      {test.verdict !== 'OK' && <p className={test.verdict === 'CRITICAL' ? s.whyCrit : s.why}>{test.reason}</p>}
      {d.restore_s !== undefined && <p className={s.muted}>{t.restore} {dur(d.restore_s, true)}{test.proven_rto_s !== null && <>, {t.rto} {dur(test.proven_rto_s, true)}</>}</p>}
      {checks.length > 0 ? <Checks checks={checks} /> : test.verdict === 'OK' && <p className={s.muted}>{t.noChecks}</p>}
    </div>
  )
}

function Checks({ checks }: { checks: Check[] }) {
  return (
    <ul className={s.checks}>
      {checks.map((c) => (
        <li key={c.name}>
          <StatusChip status={c.verdict} size="xs" />
          <span className={s.cn}>{c.name.replace(/_/g, ' ')}</span>
          <span className={s.ct}>{c.seconds ? dur(c.seconds, true) : ''}</span>
          <span className={s.cd}>{c.detail}</span>
        </li>
      ))}
    </ul>
  )
}
