import { useState } from 'react'
import { Link, Navigate } from 'react-router'
import { useT } from '../../i18n'
import { serverTime, type Fmt, type ServerClock } from '../../lib/time'
import { can, useMe } from '../api/session'
import { useDailyReports, useEvidence, useLedger, useSystem, useVMNames, type EvidenceBundle } from '../api/views'
import { Failed, Loading } from '../layout/States'
import { download, useStepUp } from '../layout/StepUp'
import { Button, Fact, Facts, Notice, PageHead, SectionHead, StatusChip } from '../ui'
import s from './EvidencePage.module.css'

type T = ReturnType<typeof useT>['evidence']
type Ledger = NonNullable<ReturnType<typeof useLedger>['data']>['latest']
type Report = NonNullable<ReturnType<typeof useDailyReports>['data']>[number]

const PAGE = 25

/** "Today 14:30" or "Nov 17 14:30" in the server's zone. */
function when(fmt: Fmt, clock: ServerClock, iso: string, t: T) {
  return fmt.date(iso) === fmt.date(clock.now) ? t.today(fmt.hhmm(iso)) : `${fmt.date(iso)} ${fmt.hhmm(iso)}`
}

const short = (h: string) => (h ? `${h.slice(0, 6)}…` : '—')

/**
 * Evidence (docs/design/v2/Evidence.dc.html), for auditors and admins: can this be proven to someone who does not
 * trust Gaia? The answer is the ledger's latest check; the facts are the chain, its key and its last outside anchor;
 * the export verifies offline without Gaia.
 */
export function EvidencePage() {
  const me = useMe().data
  const sys = useSystem()
  const led = useLedger()
  const reps = useDailyReports()
  if (!can(me, 'export')) return <Navigate to="/" replace />
  if (sys.isPending || led.isPending || reps.isPending) return <Loading />
  const err = sys.error ?? led.error ?? reps.error
  if (err || !sys.data || !led.data || !reps.data) return <Failed error={err} onRetry={() => { sys.refetch(); led.refetch(); reps.refetch() }} />
  const fmt = serverTime(sys.data.clock)
  return (
    <main className={s.page}>
      <Headline l={led.data.latest} rep={reps.data[0] ?? null} keyID={sys.data.evidence_key_id} fmt={fmt} clock={sys.data.clock} />
      <Export rep={reps.data[0] ?? null} />
      <Entries fmt={fmt} clock={sys.data.clock} />
    </main>
  )
}

function Headline({ l, rep, keyID, fmt, clock }: { l: Ledger; rep: Report | null; keyID: string; fmt: Fmt; clock: ServerClock }) {
  const t = useT().evidence
  const checked = l.checked_at !== ''
  const v = checked ? l.verdict : 'UNKNOWN'
  const empty = checked && v === 'OK' && l.entries === 0
  const answer = !checked ? t.answer.unchecked : empty ? t.answer.empty : v === 'OK' ? t.answer.ok : v === 'CRITICAL' ? t.answer.critical : t.answer.other
  const anchor = rep && rep.head_seq > 0 ? rep : null
  const at = checked ? when(fmt, clock, l.checked_at, t) : ''
  const since = anchor ? Math.max(0, l.head_seq - anchor.head_seq) : l.head_seq
  return (
    <PageHead
      question={t.question}
      answer={answer}
      lede={
        <>
          <p>
            {empty && t.ledeEmpty(at)}
            {checked && v === 'OK' && !empty && t.ledeOk(l.entries, at)}
            {checked && v !== 'OK' && t.ledeBad(at, l.problems.length ? l.problems.join('; ') : l.reason || l.text)}
            {!checked && t.ledeUnchecked}{' '}
            {!empty && (anchor ? t.anchorLede(when(fmt, clock, anchor.generated_at, t), anchor.head_seq) : t.noAnchorLede)}
          </p>
          {checked && l.entries > 0 && (
            <ol className={s.chain} aria-label={t.chain}>
              <li><b>{t.genesis}</b><span>{t.entry(1)}</span></li>
              {anchor && anchor.head_seq > 1 && (
                <>
                  {anchor.head_seq > 2 && <li className={s.gap}><span>{t.between(anchor.head_seq - 2)}</span></li>}
                  <li className={s.anch}><b>{t.anchored}</b><span>{t.entry(anchor.head_seq)} · {short(anchor.head_sha256)}</span><span>{when(fmt, clock, anchor.generated_at, t)}</span></li>
                </>
              )}
              {since > 1 && <li className={s.gap}><span>{t.notAnchored(since - 1)}</span></li>}
              {l.head_seq > 1 && (!anchor || l.head_seq > anchor.head_seq) && (
                <li className={s.head}><b>{t.head}</b><span>{t.entry(l.head_seq)} · {short(l.head_sha256)}</span><span>{at}</span></li>
              )}
            </ol>
          )}
        </>
      }
      aside={
        <Facts label={t.facts}>
          <Fact term={t.ledgerCheck}>
            <StatusChip status={v} size="xs" />
            {checked ? t.checked(l.entries, at) : t.unchecked}
          </Fact>
          <Fact term={t.signingKey}>
            <code className={s.mono} title={keyID}>{keyID}</code>
            <span className={s.of}>{t.keyHint}</span>
          </Fact>
          <Fact term={t.lastAnchor}>
            {anchor ? t.anchorText(anchor.id, when(fmt, clock, anchor.generated_at, t), anchor.head_seq, short(anchor.head_sha256)) : t.noAnchor}
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
  return (
    <section className={s.box} aria-labelledby="export">
      <SectionHead id="export" title={t.exportTitle}>
        <Button onClick={get} disabled={busy}>{busy ? t.exporting : t.export}</Button>
      </SectionHead>
      <p className={s.of}>{t.exportHint}</p>
      {err && <Notice tone="critical">{err}</Notice>}
      <div className={s.verify}>
        <span className={s.of}>{t.verify}</span>
        <code>gaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles{rep ? ` -head ledger-head-${rep.id}.json` : ''}</code>
        <code>python3 -I verify.py ledger ledger.jsonl bundles --key anchor.pub{rep ? ` --head ledger-head-${rep.id}.json` : ''}</code>
      </div>
    </section>
  )
}

/** The bundle's result in its own words: a test passed or failed, a migration check ready or not. */
function result(b: EvidenceBundle, t: T): string {
  const w = b.migration_check_id !== null ? t.word.migration : t.word.test
  return (w as Record<string, string>)[b.verdict] ?? b.verdict
}

/** The newest sealed bundles, each a ledger entry; older ones on request. */
function Entries({ fmt, clock }: { fmt: Fmt; clock: ServerClock }) {
  const t = useT().evidence
  const [limit, setLimit] = useState(PAGE)
  const q = useEvidence(limit)
  const rows = q.data ?? []
  const names = useVMNames([...new Set(rows.slice(0, limit).flatMap((b) => (b.vm_id !== null ? [b.vm_id] : [])))])
  return (
    <section className={s.sec} aria-labelledby="entries">
      <SectionHead id="entries" title={t.entries} hint={t.entriesHint} />
      {q.isPending ? <Loading /> : q.isError ? <Failed error={q.error} onRetry={() => q.refetch()} /> : rows.length === 0 ? (
        <p className={s.muted}>{t.none}</p>
      ) : (
        <>
          <div className={s.scroll}>
            <table className={s.tbl}>
              <thead>
                <tr>
                  <th scope="col" className={s.num}>{t.col.seq}</th>
                  <th scope="col">{t.col.when}</th>
                  <th scope="col">{t.col.entry}</th>
                  <th scope="col">{t.col.vm}</th>
                  <th scope="col">{t.col.result}</th>
                  <th scope="col">{t.col.hash}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((b) => (
                  <tr key={b.id}>
                    <td className={`${s.num} ${s.mono}`}>{b.ledger_seq.toLocaleString('en-US')}</td>
                    <td className={s.nowrap}>{when(fmt, clock, b.created_at, t)}</td>
                    <td>
                      {b.migration_check_id !== null ? t.kind.migration(b.migration_check_id) : t.kind.test(b.test_id ?? 0)}
                      <span className={s.of} title={b.bundle_id}>{b.bundle_id}</span>
                    </td>
                    <td>
                      {b.vm_id !== null
                        ? <Link className={s.vm} to={`/vms/${b.vm_id}`}>{names.get(b.vm_id) ?? t.vmN(b.vm_id)}</Link>
                        : <Link className={s.vm} to="/migration">{t.migration}</Link>}
                    </td>
                    <td><StatusChip status={b.verdict} size="xs" word={result(b, t)} /></td>
                    <td className={s.mono} title={b.bundle_sha256}>{short(b.bundle_sha256)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className={s.more}>
            <span className={s.of}>{t.shown(rows.length)}</span>
            {rows.length === limit && limit < 1000 && (
              <Button variant="quiet" disabled={q.isFetching} onClick={() => setLimit(Math.min(1000, limit * 2))}>{t.older}</Button>
            )}
          </div>
        </>
      )}
    </section>
  )
}
