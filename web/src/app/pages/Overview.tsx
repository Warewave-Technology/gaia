import { useState } from 'react'
import { Link, useLocation } from 'react-router'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { serverTime, type Fmt, type ServerClock } from '../../lib/time'
import { useFilters } from '../../lib/url'
import {
  useAttention, useCapacitySummary, useCoverage, useFilterNames, useFleet, useVMList,
  type AttentionGroup, type CoverageGroup, type FleetView, type VMFilter, type VMListRow,
} from '../api/fleet'
import { Failed, Loading } from '../layout/States'
import {
  Button, Fact, Facts, HistoryStrip, Legend, MoreRows, Notice, PageHead, Pick, ProofBar, ProofScale, SectionHead, Seg, StatusBar,
  StatusChip, StatusCounts, asStatus,
} from '../ui'
import s from './Overview.module.css'

const ORDER = ['CRITICAL', 'UNKNOWN', 'WARNING', 'OK'] as const
const KEYS = ['show', 'status', 'host', 'policy', 'tag', 'code', 'scope_kind', 'scope_id', 'q', 'by', 'all'] as const
type Filters = Record<(typeof KEYS)[number], string>
type SetFilters = (patch: Partial<Filters>) => void

/** The overview as it is now, filters included: a VM page's breadcrumb leads back to it. */
function useFrom() {
  const l = useLocation()
  return l.pathname + l.search
}

/** "Today 04:51" or "Nov 17 04:51" in the server's zone. */
function when(fmt: Fmt, clock: ServerClock, iso: string, t: ReturnType<typeof useT>['home']) {
  return fmt.date(iso) === fmt.date(clock.now) ? t.today(fmt.hhmm(iso)) : `${fmt.date(iso)} ${fmt.hhmm(iso)}`
}

/**
 * The overview (docs/design/v2/Main.dc.html): can every VM return right now; the causes of those that cannot, each
 * once; coverage by host, policy or tag; and the VM table, needs-attention first. Every region is its own request
 * and every list is paged, so a fleet of thousands draws as fast as a few (ADR-007 C1).
 */
export function Overview() {
  const q = useFleet()
  const [f, setF] = useFilters(KEYS)
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  return (
    <main className={s.page}>
      <Headline v={q.data} setF={setF} />
      {q.data.vms > 0 && (
        <>
          {q.data.cannot > 0 && <Attention v={q.data} setF={setF} />}
          <Coverage f={f} setF={setF} />
          <VMTable v={q.data} f={f} setF={setF} />
        </>
      )}
    </main>
  )
}

function Headline({ v, setF }: { v: FleetView; setF: SetFilters }) {
  const t = useT().home
  const fmt = serverTime(v.clock)
  const cap = useCapacitySummary().data
  const worst = v.worst_proven_rpo
  const running = (v.running ?? []).filter((c) => c.state === 'running')
  const queued = (v.running ?? []).filter((c) => c.state === 'queued').reduce((n, c) => n + c.count, 0)
  const lede = v.vms === 0 ? t.empty : (
    <>
      {t.canReturn}{' '}
      {v.cannot > 0 && <><b>{t.cannot(v.cannot)}</b>{t.causes(v.causes)}{' '}</>}
      {worst && (
        <>
          {t.worst}{' '}
          <b className={worst.rpo_target_s !== null && worst.proven_rpo_s > worst.rpo_target_s ? s.bad : ''}>{dur(worst.proven_rpo_s)}</b>{' '}
          {t.worstOf(worst.vm, worst.rpo_target_s !== null ? dur(worst.rpo_target_s) : null)}
        </>
      )}
      {v.uncomputed > 0 && <> {t.uncomputed(v.uncomputed)}</>}
    </>
  )
  const testsPerDay = cap ? cap.by_os_class.reduce((n, g) => n + g.tests_per_day, 0) : 0
  return (
    <>
      <PageHead
        size="hero"
        question={t.question}
        answer={<>{v.can_return.toLocaleString('en-US')} <small>{t.ofVMs(v.vms)}</small></>}
        lede={
          <>
            <p>{lede}</p>
            <ul className={s.tally} aria-label={t.byStatus}>
              {ORDER.map((st) => (
                <li key={st}>
                  <a
                    href="#vms"
                    className={`${s.tile} ${s[`t${st}`]}`}
                    onClick={() => setF({ status: st, show: st === 'OK' ? 'all' : '' })}
                  >
                    <span className={s.tn}>{(v.by_status[st] ?? 0).toLocaleString('en-US')}</span>
                    <StatusChip status={st} size="sm" />
                  </a>
                </li>
              ))}
            </ul>
          </>
        }
        aside={
          <Facts label={t.facts}>
            <Fact term={t.capacity}>
              {cap ? (
                <>
                  <StatusChip status={cap.coverage >= 1 ? 'OK' : 'WARNING'} size="xs" />
                  {cap.coverage >= 1 ? t.capacityOk(Math.round(testsPerDay), cap.slots) : t.capacityBehind(Math.round(testsPerDay), cap.slots, Math.floor(cap.coverage * 100))}
                  {cap.expiring_total > 0 && <> {t.expiring(cap.expiring_total)}</>}{' '}
                  <Link to="/capacity">{t.planCapacity}</Link>
                </>
              ) : t.loading}
            </Fact>
            <Fact term={t.ledger}>
              <StatusChip status={v.ledger.checked_at ? v.ledger.verdict : 'UNKNOWN'} size="xs" />
              {v.ledger.checked_at ? t.ledgerText(v.ledger.entries, when(fmt, v.clock, v.ledger.checked_at, t)) : t.ledgerUnchecked}
            </Fact>
            <Fact term={t.kit}>
              <StatusChip status={v.recovery_kit.state === 'confirmed' ? 'OK' : 'WARNING'} size="xs" />
              {v.recovery_kit.confirmed_at
                ? t.kitSaved(v.recovery_kit.generation, `${fmt.date(v.recovery_kit.confirmed_at)}`, v.recovery_kit.confirmed_by ?? '')
                : v.recovery_kit.text}
            </Fact>
            <Fact term={t.running}>
              {running.length === 0 && queued === 0 ? t.idle : t.runningText(running.map((c) => t.kind(c.kind, c.count)).join(', '), queued)}{' '}
              <Link to="/activity">{t.activity}</Link>
            </Fact>
          </Facts>
        }
      />
      {v.notifications.state !== 'OK' && <Notice tone="warning">{v.notifications.text}</Notice>}
      {v.ledger.verdict !== 'OK' && v.ledger.checked_at && <Notice tone="critical">{v.ledger.text}</Notice>}
    </>
  )
}

/** Where a group's VMs are: its scope, or its hosts with their counts. */
function where(g: AttentionGroup, t: ReturnType<typeof useT>['home']) {
  if (g.scope) return t.scope(g.scope.kind, g.scope.name)
  const hosts = g.hosts.map((h) => (g.hosts.length > 1 || h.count !== g.count ? `${h.name} ×${h.count}` : h.name))
  return hosts.slice(0, 3).join(' · ') + (hosts.length > 3 ? ` ${t.andMore(hosts.length - 3)}` : '')
}

function Attention({ v, setF }: { v: FleetView; setF: SetFilters }) {
  const t = useT().home
  const q = useAttention()
  const [open, setOpen] = useState<string | null>(null)
  const fmt = serverTime(v.clock)
  return (
    <section className={s.sec} aria-labelledby="attention">
      <SectionHead id="attention" title={t.attention} hint={t.attentionHint(v.cannot)} />
      {q.isPending ? <Loading /> : q.isError ? <Failed error={q.error} onRetry={() => q.refetch()} /> : (
        <>
          <table className={s.tbl}>
            <thead>
              <tr>
                <th scope="col" className={s.w130}>{t.cols.status}</th>
                <th scope="col">{t.cols.cause}</th>
                <th scope="col" className={s.num}>{t.cols.vms}</th>
                <th scope="col" className={s.w260}>{t.cols.where}</th>
                <th scope="col" className={s.w90}>{t.cols.since}</th>
                <th scope="col" className={s.w170}><span className={s.vh}>{t.cols.action}</span></th>
              </tr>
            </thead>
            <tbody>
              {q.rows.map((g) => {
                const key = `${g.code}/${g.scope?.kind ?? ''}/${g.scope?.id ?? 0}`
                const isOpen = open === key
                const filter: VMFilter = { code: g.code, scope_kind: g.scope?.kind, scope_id: g.scope?.id }
                return (
                  <GroupRows key={key} g={g} isOpen={isOpen} filter={filter} fmt={fmt} clock={v.clock}
                    where={where(g, t)} onToggle={() => setOpen(isOpen ? null : key)}
                    onList={() => setF({ code: g.code, scope_kind: g.scope?.kind ?? '', scope_id: g.scope ? String(g.scope.id) : '', show: 'all', status: '' })} />
                )
              })}
            </tbody>
          </table>
          {q.hasNextPage && <MoreRows auto={false} shown={q.rows.length} total={q.head?.total ?? 0} more loading={q.isFetchingNextPage} onMore={() => q.fetchNextPage()} />}
        </>
      )}
    </section>
  )
}

function GroupRows({ g, isOpen, filter, fmt, clock, where, onToggle, onList }: {
  g: AttentionGroup
  isOpen: boolean
  filter: VMFilter
  fmt: Fmt
  clock: ServerClock
  where: string
  onToggle: () => void
  onList: () => void
}) {
  const t = useT().home
  return (
    <>
      <tr className={`${s[`g${asStatus(g.status)}`]} ${isOpen ? s.open : ''}`}>
        <td><StatusChip status={g.status} size="sm" /></td>
        <td>
          <b className={s.cause}>{g.cause}</b>
          <span className={`${s.detail} ${s.clamp}`} title={g.example.reason}>{t.example(g.example.vm, g.example.reason)}</span>
        </td>
        <td className={s.num}><b className={s.count}>{g.count.toLocaleString('en-US')}</b></td>
        <td className={s.where}>{where}</td>
        <td className={s.mono}>{g.since ? when(fmt, clock, g.since, t) : '—'}</td>
        <td className={s.num}>
          <Button variant="quiet" aria-expanded={isOpen} onClick={onToggle}>{isOpen ? t.hideVMs : t.showVMs(g.count)}</Button>
        </td>
      </tr>
      {isOpen && (
        <tr className={s.sub}>
          <td />
          <td colSpan={5}>
            <GroupVMs filter={filter} fmt={fmt} clock={clock} />
            <a href="#vms" className={s.asList} onClick={onList}>{t.asList}</a>
          </td>
        </tr>
      )}
    </>
  )
}

function GroupVMs({ filter, fmt, clock }: { filter: VMFilter; fmt: Fmt; clock: ServerClock }) {
  const t = useT().home
  const from = useFrom()
  const q = useVMList(filter, 25)
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  return (
    <>
      <table className={`${s.tbl} ${s.inner}`}>
        <thead>
          <tr>
            <th scope="col">{t.cols.vm}</th>
            <th scope="col">{t.cols.ret}</th>
            <th scope="col">{t.cols.rpo}</th>
            <th scope="col">{t.cols.why}</th>
          </tr>
        </thead>
        <tbody>
          {q.rows.map((r) => (
            <tr key={r.vm_id}>
              <td><Link className={s.vm} to={`/vms/${r.vm_id}`} state={{ from }}>{r.vm}</Link><span className={s.of}>{r.host}</span></td>
              <td className={s.fg2}>{r.proven ? t.pointAt(when(fmt, clock, r.proven.point_taken_at, t), r.proven.point_id) : t.nothingProven}</td>
              <td>
                <b className={r.targets && r.proven_rpo_s !== null && r.proven_rpo_s > r.targets.rpo_target_s ? s.bad : s.fig}>{dur(r.proven_rpo_s)}</b>
                {r.targets && <span className={s.of}>{t.target(dur(r.targets.rpo_target_s))}</span>}
              </td>
              <td className={s.fg2}>{r.reason}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <MoreRows auto={false} shown={q.rows.length} total={q.head?.total ?? 0} more={!!q.hasNextPage} loading={q.isFetchingNextPage} onMore={() => q.fetchNextPage()} />
    </>
  )
}

function Coverage({ f, setF }: { f: Filters; setF: SetFilters }) {
  const t = useT().home
  const by = f.by === 'policy' || f.by === 'tag' ? f.by : 'host'
  const all = f.all === 'true'
  const q = useCoverage(by, all)
  const word = t.by[by]
  const pick = (g: CoverageGroup) => setF({
    host: by === 'host' ? String(g.id) : '', policy: by === 'policy' ? String(g.id) : '', tag: by === 'tag' ? g.name : '', show: 'all',
    status: '', code: '', scope_kind: '', scope_id: '',
  })
  return (
    <section className={s.sec} aria-labelledby="coverage">
      <SectionHead id="coverage" title={t.coverage(word)} hint={t.coverageHint}>
        <Seg label={t.groupBy} value={by} options={[['host', t.by.host], ['policy', t.by.policy], ['tag', t.by.tag]]} onChange={(b) => setF({ by: b === 'host' ? '' : b })} />
      </SectionHead>
      {q.isPending ? <Loading /> : q.isError ? <Failed error={q.error} onRetry={() => q.refetch()} /> : (
        <>
          {q.rows.length > 0 && (
            <table className={s.tbl}>
              <thead>
                <tr>
                  <th scope="col" className={s.w220}>{word}</th>
                  <th scope="col" className={s.num}>{t.cols.vms}</th>
                  <th scope="col">{t.cols.every}</th>
                  <th scope="col" className={s.w170}>{t.cols.worstRpo}</th>
                  <th scope="col" className={s.w130}>{t.cols.oldestProof}</th>
                </tr>
              </thead>
              <tbody>
                {q.rows.map((g) => (
                  <tr key={`${g.id}/${g.name}`}>
                    <td><a href="#vms" className={s.vm} onClick={() => pick(g)}>{g.name}</a></td>
                    <td className={s.num}><b className={s.fig}>{g.vms.toLocaleString('en-US')}</b></td>
                    <td className={s.mid}>
                      <StatusBar counts={g.by_status} />
                      <StatusCounts counts={g.by_status} />
                    </td>
                    <td>
                      {g.worst_proven_rpo ? (
                        <>
                          <b className={g.worst_proven_rpo.rpo_target_s !== null && g.worst_proven_rpo.proven_rpo_s > g.worst_proven_rpo.rpo_target_s ? s.bad : s.fig}>
                            {dur(g.worst_proven_rpo.proven_rpo_s)}
                          </b>
                          <span className={s.of}>{g.worst_proven_rpo.vm}{g.worst_proven_rpo.rpo_target_s !== null && `, ${t.target(dur(g.worst_proven_rpo.rpo_target_s))}`}</span>
                        </>
                      ) : <span className={s.of}>{t.nothingProven}</span>}
                    </td>
                    <td><b className={s.fig}>{dur(g.oldest_proof_s)}</b></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {q.head && q.head.healthy > 0 && (
            <p className={s.foot}>
              {t.coverageShown(q.head.shown, q.head.total, q.head.healthy, word, all)}{' '}
              <button type="button" className={s.link} onClick={() => setF({ all: all ? '' : 'true' })}>{all ? t.hideHealthy : t.showAll(q.head.total, word)}</button>
            </p>
          )}
          {q.hasNextPage && <MoreRows auto={false} shown={q.rows.length} total={q.head?.shown ?? 0} more loading={q.isFetchingNextPage} onMore={() => q.fetchNextPage()} />}
        </>
      )}
    </section>
  )
}

function VMTable({ v, f, setF }: { v: FleetView; f: Filters; setF: SetFilters }) {
  const t = useT().home
  const names = useFilterNames()
  const attention = f.show !== 'all'
  const filter: VMFilter = {
    attention: attention || undefined,
    status: f.status || undefined,
    host_id: f.host ? Number(f.host) : undefined,
    policy_id: f.policy ? Number(f.policy) : undefined,
    tag: f.tag || undefined,
    code: f.code || undefined,
    scope_kind: f.scope_kind || undefined,
    scope_id: f.scope_id ? Number(f.scope_id) : undefined,
    q: f.q || undefined,
  }
  const q = useVMList(filter)
  const fmt = serverTime(v.clock)
  const hours = q.head ? Math.round(q.head.window_s / 3600) : 24
  const filtered = !!(f.status || f.host || f.policy || f.tag || f.code || f.q)
  return (
    <section className={s.sec} id="vms" aria-labelledby="vms-h">
      <SectionHead id="vms-h" title={t.vms}>
        <Seg label={t.show} value={attention ? 'attention' : 'all'}
          options={[['attention', t.needsAttention(v.cannot)], ['all', t.all(v.vms)]]} onChange={(x) => setF({ show: x === 'all' ? 'all' : '' })} />
        <Pick label={t.f.status} value={f.status} onChange={(x) => setF({ status: x })}
          options={[['', t.f.any], ...ORDER.map((st) => [st, st] as const)]} />
        <Pick label={t.f.host} value={f.host} onChange={(x) => setF({ host: x })}
          options={[['', t.f.any], ...names.hosts.map((h) => [String(h.id), h.name] as const)]} />
        <Pick label={t.f.policy} value={f.policy} onChange={(x) => setF({ policy: x })}
          options={[['', t.f.any], ...names.policies.map((p) => [String(p.id), p.name] as const)]} />
        <Pick label={t.f.tag} value={f.tag} onChange={(x) => setF({ tag: x })}
          options={[['', t.f.any], ...names.tags.map((n) => [n, n] as const)]} />
        {f.code && (
          <span className={s.cf}>
            {t.f.cause(f.code)}
            <button type="button" className={s.link} onClick={() => setF({ code: '', scope_kind: '', scope_id: '' })}>{t.f.clear}</button>
          </span>
        )}
        {filtered && <button type="button" className={s.link} onClick={() => setF({ status: '', host: '', policy: '', tag: '', code: '', scope_kind: '', scope_id: '', q: '' })}>{t.f.clearAll}</button>}
        <span className={s.sorted}>{t.sorted}</span>
      </SectionHead>
      {q.isPending ? <Loading /> : q.isError ? <Failed error={q.error} onRetry={() => q.refetch()} /> : q.rows.length === 0 ? (
        <p className={s.empty}>{attention && !filtered ? t.noneNeedAttention : t.noneMatch}</p>
      ) : (
        <>
          <div className={s.scroll}>
            <table className={`${s.tbl} ${s.vmt}`}>
              <thead>
                <tr>
                  <th scope="col">{t.cols.vm}</th>
                  <th scope="col">{t.cols.ret}</th>
                  <th scope="col" className={s.cProof}>{t.cols.rpo}<ProofScale width={160} /></th>
                  <th scope="col" className={s.num}>{t.cols.rto}</th>
                  <th scope="col" className={s.num}>{t.cols.age}</th>
                  <th scope="col">{t.cols.hist(hours)}</th>
                  <th scope="col">{t.cols.why}</th>
                </tr>
              </thead>
              <tbody>
                {q.rows.map((r) => <VMRow key={r.vm_id} r={r} fmt={fmt} clock={v.clock} windowS={q.head?.window_s ?? 86400} />)}
              </tbody>
            </table>
          </div>
          <div className={s.legend}><Legend /></div>
          <MoreRows shown={q.rows.length} total={q.head?.total ?? 0} more={!!q.hasNextPage} loading={q.isFetchingNextPage} onMore={() => q.fetchNextPage()} />
        </>
      )}
    </section>
  )
}

function VMRow({ r, fmt, clock, windowS }: { r: VMListRow; fmt: Fmt; clock: ServerClock; windowS: number }) {
  const t = useT().home
  const from = useFrom()
  const st = asStatus(r.status)
  const tg = r.targets
  return (
    <tr className={s[`r${st}`]}>
      <th scope="row" className={s.cVm}>
        <span className={s.name}>
          <StatusChip status={st} size="sm" />
          <Link className={s.vm} to={`/vms/${r.vm_id}`} state={{ from }}>{r.vm}</Link>
        </span>
        <span className={s.of}>{r.host}{r.policy ? ` · ${r.policy}` : `, ${t.noPolicy}`}{r.tags.length > 0 && ` · ${r.tags.join(', ')}`}</span>
      </th>
      <td className={s.cRet}>
        {r.proven ? (
          <>
            <b className={s.rt}>{when(fmt, clock, r.proven.point_taken_at, t)}</b>
            <span className={s.of}>{t.provenBy(r.proven.point_id, r.proven.test_id)}</span>
          </>
        ) : (
          <>
            <b className={`${s.rt} ${s.none}`}>{t.nothingProven}</b>
            <span className={s.of}>{t.noPassingTest}</span>
          </>
        )}
      </td>
      <td className={s.cProof}>
        {tg && <ProofBar value={r.proven_rpo_s} newest={r.theoretical_rpo_s} target={tg.rpo_target_s} width={160} label={t.cols.rpo} />}
        <b className={tg && r.proven_rpo_s !== null && r.proven_rpo_s > tg.rpo_target_s ? s.bad : s.fig}>{dur(r.proven_rpo_s)}</b>
        {tg && <span className={s.of}>{t.target(dur(tg.rpo_target_s))}</span>}
      </td>
      <td className={s.num}>
        <b className={tg && r.proven_rto_s !== null && r.proven_rto_s > tg.rto_target_s ? s.bad : s.fig}>{dur(r.proven_rto_s)}</b>
        {tg && <span className={s.of}>{t.target(dur(tg.rto_target_s))}</span>}
      </td>
      <td className={s.num}>
        <b className={tg && r.proof_age_s !== null && r.proof_age_s > tg.max_proof_age_s ? s.bad : s.fig}>{dur(r.proof_age_s)}</b>
        {tg && <span className={s.of}>{t.max(dur(tg.max_proof_age_s))}</span>}
      </td>
      <td className={s.mid}>
        <HistoryStrip events={r.events} now={clock.now} windowS={windowS} returnAt={r.proven?.point_taken_at} width={190} />
      </td>
      <td className={s.cWhy}>
        <p className={s.clamp} title={r.reason}>{r.reason}</p>
        {r.notes.map((n, i) => <p key={i} className={s.note}><span className={s.nl}>{t.note}</span>{n.text}</p>)}
      </td>
    </tr>
  )
}
