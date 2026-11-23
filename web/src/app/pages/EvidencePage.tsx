import { useState } from 'react'
import { Link, Navigate } from 'react-router'
import { useT } from '../../i18n'
import { ledgerState } from '../../i18n/en-evidence'
import { dur } from '../../lib/duration'
import { serverTime, type Fmt } from '../../lib/time'
import { useFilters } from '../../lib/url'
import { useAnyTestPassed, useEvidencePages, type EvidenceFilter, type EvidenceRow, type LedgerSummary } from '../api/evidence'
import { can, useMe } from '../api/session'
import { useCheckLedger, useDailyReports, useLedger, useSystem } from '../api/views'
import { Failed, Loading } from '../layout/States'
import { download, useStepUp } from '../layout/StepUp'
import { Button, DataTable, Fact, Facts, FilterChip, Notice, PageHead, SectionHead, StatusChip, TechDetails, Toolbar, type Column } from '../ui'
import s from './EvidencePage.module.css'

type T = ReturnType<typeof useT>['evidence']
type Report = NonNullable<ReturnType<typeof useDailyReports>['data']>[number]

const short = (h: string) => (h ? `${h.slice(0, 6)}…` : '—')
const KEYS = ['kind', 'result'] as const

/**
 * Evidence (docs/design/v2/Evidence.dc.html with the v3 rules), for auditors and admins: can this be proven to someone
 * who does not trust Gaia? The answer is one sentence from the ledger's state (ADR-007 C14); "Check now" sits next to
 * the ledger fact; the export verifies offline without Gaia; the entries are a data-table paged by the server.
 */
export function EvidencePage() {
  const me = useMe().data
  const sys = useSystem()
  const led = useLedger()
  const reps = useDailyReports()
  const l = led.data?.latest as LedgerSummary | undefined
  // An older server cannot say that an empty ledger is a fault: whether a test passed comes from the fleet.
  const passed = useAnyTestPassed(!!l && !l.code && l.entries === 0)
  if (!can(me, 'export')) return <Navigate to="/" replace />
  if (sys.isPending || led.isPending || reps.isPending) return <Loading />
  const err = sys.error ?? led.error ?? reps.error
  if (err || !sys.data || !l || !reps.data) return <Failed error={err} onRetry={() => { sys.refetch(); led.refetch(); reps.refetch() }} />
  const fmt = serverTime(sys.data.clock)
  return (
    <main className={s.page}>
      <Headline l={l} passed={passed} admin={can(me, 'admin')} rep={reps.data[0] ?? null} keyID={sys.data.evidence_key_id} fmt={fmt} now={Date.parse(sys.data.clock.now)} />
      <Export rep={reps.data[0] ?? null} />
      <Entries fmt={fmt} />
    </main>
  )
}

function Headline({ l, passed, admin, rep, keyID, fmt, now }: {
  l: LedgerSummary
  passed: boolean | null
  admin: boolean
  rep: Report | null
  keyID: string
  fmt: Fmt
  now: number
}) {
  const t = useT().evidence
  const w = useT().ledger
  const check = useCheckLedger()
  const st = ledgerState(l, now, passed)
  const at = l.checked_at ? fmt.at(l.checked_at) : ''
  const anchor = rep && rep.head_seq > 0 ? rep : null
  const since = anchor ? Math.max(0, l.head_seq - anchor.head_seq) : l.head_seq
  const raw = [...l.problems, ...(st.status !== 'OK' && l.reason ? [l.reason] : [])].join('\n')
  return (
    <PageHead
      question={t.question}
      answer={w.answer(st, l, l.checked_at ? Math.max(0, fmt.age(l.checked_at)) : 0)}
      lede={
        <>
          <p>
            {w.lede(st, l, at)}{' '}
            {l.entries > 0 && (anchor ? t.anchorLede(fmt.at(anchor.generated_at), anchor.head_seq) : t.noAnchorLede)}
          </p>
          {l.checked_at && l.entries > 0 && (
            <ol className={s.chain} aria-label={t.chain}>
              <li><b>{t.genesis}</b></li>
              {anchor && anchor.head_seq > 1 && (
                <>
                  {anchor.head_seq > 2 && <li className={s.gap}><span>{t.between(anchor.head_seq - 2)}</span></li>}
                  <li className={s.anch}><b>{t.anchored}</b><span>{t.entry(anchor.head_seq)} · {short(anchor.head_sha256)}</span></li>
                </>
              )}
              {since > 1 && <li className={s.gap}><span>{t.notAnchored(since - 1)}</span></li>}
              {l.head_seq > 1 && (!anchor || l.head_seq > anchor.head_seq) && (
                <li className={s.head}><b>{t.head}</b><span>{t.entry(l.head_seq)} · {short(l.head_sha256)}</span></li>
              )}
            </ol>
          )}
          {raw && <TechDetails summary={t.problems}>{raw}</TechDetails>}
        </>
      }
      aside={
        <Facts label={t.facts}>
          <Fact term={t.ledgerCheck}>
            <span className={s.ledgerFact}>
              <span><StatusChip status={st.status} size="xs" /> {w.fact(st, l, at)}</span>
              {admin && (
                <Button variant="quiet" onClick={() => check.mutate()} disabled={check.isPending}>{check.isPending ? t.checking : t.checkNow}</Button>
              )}
            </span>
            {check.error && <Notice tone="critical">{t.checkFailed(check.error.message)}</Notice>}
          </Fact>
          <Fact term={t.signingKey}>
            <code className={s.mono} title={keyID}>{keyID}</code>
            <span className={`${s.of} ${s.block}`}>{t.keyHint}</span>
          </Fact>
          <Fact term={t.lastAnchor}>
            {anchor ? t.anchorText(anchor.id, fmt.at(anchor.generated_at), anchor.head_seq, short(anchor.head_sha256)) : t.noAnchor}
          </Fact>
          <Fact term={t.format}>{t.formatText}</Fact>
        </Facts>
      }
    />
  )
}

/** The whole export: every bundle, the ledger, the keys, the newest daily report, the specification. A step-up. */
function Export({ rep }: { rep: Report | null }) {
  const t = useT().evidence
  const ask = useStepUp()
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const get = () => {
    setErr(null)
    setBusy(true)
    download(ask, '/api/v1/evidence-export', 'gaia-evidence-export.tar.gz')
      .catch((e: Error) => setErr(e.message))
      .finally(() => setBusy(false))
  }
  const head = rep ? ` -head ledger-head-${rep.id}.json` : ''
  return (
    <section className={s.box} aria-labelledby="export">
      <SectionHead id="export" title={t.exportTitle} hint={t.exportHint}>
        <Button onClick={get} disabled={busy}>{busy ? t.exporting : t.export}</Button>
      </SectionHead>
      {err && <Notice tone="critical">{err}</Notice>}
      <TechDetails summary={t.verify}>
        {`gaia evidence verify -pubkey anchor.pub -export .\ngaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles${head}\npython3 -I verify.py ledger ledger.jsonl bundles --key anchor.pub${rep ? ` --head ledger-head-${rep.id}.json` : ''}`}
      </TechDetails>
    </section>
  )
}

/** The bundle's result in its own words: a test passed or failed, a migration check ready or not, an export recorded. */
function result(b: EvidenceRow, t: T): string {
  const w = b.kind === 'export' ? t.word.export : b.migration_check_id !== null ? t.word.migration : t.word.test
  return (w as Record<string, string>)[b.verdict] ?? b.verdict
}

/** What the entry is: a restore test, a migration check, or the record of an evidence export (ADR-007 C7). */
function entry(b: EvidenceRow, t: T): string {
  if (b.kind === 'export') return t.kind.export
  return b.migration_check_id !== null ? t.kind.migration(b.migration_check_id) : t.kind.test(b.test_id ?? 0)
}

const KINDS = ['test', 'migration', 'export'] as const
type Kind = (typeof KINDS)[number]
const isKind = (k: string): k is Kind => (KINDS as readonly string[]).includes(k)

/** The sealed bundles, each a ledger entry, newest first, one page at a time; the filters live in the URL. */
function Entries({ fmt }: { fmt: Fmt }) {
  const t = useT().evidence
  const [f, setF] = useFilters(KEYS)
  const filter: EvidenceFilter = {
    ...(isKind(f.kind) ? { kind: f.kind } : {}),
    ...(f.result === 'failed' ? { verdict: 'CRITICAL' as const } : {}),
  }
  const pages = useEvidencePages(filter)
  const cols: Column<EvidenceRow>[] = [
    { key: 'seq', header: t.col.seq, numeric: true, width: '96px', cell: (b) => <span className={s.mono}>{b.ledger_seq.toLocaleString('en-US')}</span> },
    { key: 'when', header: t.col.when, width: '150px', cell: (b) => fmt.at(b.created_at) },
    { key: 'entry', header: t.col.entry, width: '190px', cell: (b) => entry(b, t) },
    { key: 'vm', header: t.col.vm, title: (b) => b.vm || undefined, cell: (b) => (b.kind === 'export'
      ? <span>{t.by(b.by)}</span>
      : b.vm_id !== null
        ? <Link className={s.vm} to={`/vms/${b.vm_id}`}>{b.vm || t.vmN(b.vm_id)}</Link>
        : <Link className={s.vm} to="/migration">{b.vm || t.migration}</Link>) },
    { key: 'result', header: t.col.result, width: '240px', cell: (b) => (
      <>
        <StatusChip status={b.verdict} size="xs" word={result(b, t)} />
        {b.proven_rto_s !== null && b.verdict !== 'CRITICAL' && <span className={s.of}>{t.rto(dur(b.proven_rto_s, true))}</span>}
      </>
    ) },
    { key: 'hash', header: t.col.hash, width: '110px', title: (b) => b.bundle_sha256, cell: (b) => <span className={s.mono}>{short(b.bundle_sha256)}</span> },
  ]
  const any = !!(f.kind || f.result)
  return (
    <section className={s.sec} aria-labelledby="entries">
      <SectionHead id="entries" title={t.entries} hint={t.entriesHint} />
      <Toolbar>
        <FilterChip set={!any} onClick={() => setF({ kind: '', result: '' })}>{t.f.clear}</FilterChip>
        <FilterChip set={f.kind === 'test'} onClick={() => setF({ kind: f.kind === 'test' ? '' : 'test' })}>{t.f.tests}</FilterChip>
        <FilterChip set={f.kind === 'migration'} onClick={() => setF({ kind: f.kind === 'migration' ? '' : 'migration' })}>{t.f.migrations}</FilterChip>
        <FilterChip set={f.kind === 'export'} onClick={() => setF({ kind: f.kind === 'export' ? '' : 'export' })}>{t.f.exports}</FilterChip>
        <FilterChip set={f.result === 'failed'} onClick={() => setF({ result: f.result === 'failed' ? '' : 'failed' })}>{t.f.failed}</FilterChip>
      </Toolbar>
      {pages.isError ? <Failed error={pages.error} onRetry={() => pages.refetch()} /> : (
        <DataTable caption={t.entries} columns={cols} pages={pages} rowKey={(b) => String(b.id)} empty={any ? t.noneMatch : t.none}
          rowState={(b) => (b.verdict === 'CRITICAL' ? 'critical' : undefined)}
          detail={(b) => (
            <dl className={s.kv}>
              <div><dt>{t.bundle}</dt><dd className={s.mono}>{b.bundle_id}</dd></div>
              <div><dt>{t.sha}</dt><dd className={s.mono}>{b.bundle_sha256}</dd></div>
              <p className={s.of}>{t.detailHint}</p>
            </dl>
          )} />
      )}
    </section>
  )
}
