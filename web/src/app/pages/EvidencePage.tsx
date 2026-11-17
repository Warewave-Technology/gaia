import { Navigate } from 'react-router'
import { useT } from '../../i18n'
import { serverTime } from '../../lib/time'
import { can, useMe } from '../api/session'
import { useDailyReports, useEvidence, useSystem, useVMs } from '../api/views'
import { Failed, Loading } from '../layout/States'
import { ButtonLink, DataTable, StatusChip, by, type Column } from '../ui'
import type { components } from '../../api/schema'

type Bundle = components['schemas']['EvidenceBundle']
import s from './EvidencePage.module.css'

/** The auditor's place (read-only): what the evidence is, its state, and the export that verifies without Gaia. */
export function EvidencePage() {
  const t = useT().evidence
  const me = useMe().data
  const sys = useSystem()
  const ev = useEvidence()
  const vms = useVMs()
  const reps = useDailyReports()
  if (!can(me, 'export')) return <Navigate to="/" replace />
  if (sys.isPending || ev.isPending || vms.isPending || reps.isPending) return <Loading />
  const err = sys.error ?? ev.error ?? vms.error ?? reps.error
  if (err || !sys.data || !ev.data || !vms.data || !reps.data) return <Failed error={err} />
  const fmt = serverTime(sys.data.clock)
  const names = new Map(vms.data.map((v) => [v.id, v.name]))
  const l = sys.data.ledger
  const rep = reps.data[0]
  const cols: Column<Bundle>[] = [
    { key: 'seq', header: t.col.seq, numeric: true, compare: by((b) => b.ledger_seq), cell: (b) => b.ledger_seq },
    { key: 'bundle', header: t.col.bundle, clip: true, compare: by((b) => b.bundle_id), cell: (b) => <code title={b.bundle_id}>{b.bundle_id}</code> },
    { key: 'vm', header: t.col.vm, compare: by((b) => (b.vm_id ? names.get(b.vm_id) ?? '' : '')), cell: (b) => (b.vm_id ? names.get(b.vm_id) ?? b.vm_id : '—') },
    { key: 'test', header: t.col.test, numeric: true, compare: by((b) => b.test_id), cell: (b) => b.test_id ?? '—' },
    { key: 'verdict', header: t.col.verdict, compare: by((b) => b.verdict), cell: (b) => <StatusChip status={b.verdict} size="xs" /> },
    { key: 'sealed', header: t.col.sealed, numeric: true, compare: by((b) => b.created_at), cell: (b) => `${fmt.date(b.created_at)} ${fmt.clock(b.created_at)}` },
  ]
  return (
    <main className={s.page}>
      <section className={s.head}>
        <div>
          <h1>{t.title}</h1>
        </div>
        <div className={s.dl}>
          <ButtonLink href="/api/v1/evidence-export" download>{t.export}</ButtonLink>
          <span>{t.exportHint}</span>
        </div>
      </section>
      <dl className={s.facts}>
        <div><dt>{t.ledger}</dt><dd><StatusChip status={l.verdict} size="xs" />{l.checked_at ? t.ledgerChecked(`${fmt.date(l.checked_at)} ${fmt.hhmm(l.checked_at)}`, l.entries, l.problems) : l.text}</dd></div>
        <div><dt>{t.anchor}</dt><dd><code>{sys.data.evidence_key_id}</code><span className={s.hint}>{t.anchorHint}</span></dd></div>
        <div><dt>{t.dailyReport}</dt><dd>{rep ? `${rep.id}, ${fmt.date(rep.generated_at)} ${fmt.hhmm(rep.generated_at)}, ledger head at entry ${rep.head_seq}` : '—'}</dd></div>
      </dl>
      <section className={s.verify}>
        <h2>{t.verify}</h2>
        <code>gaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles{rep ? ` -head ledger-head-${rep.id}.json` : ''}</code>
        <code>python3 -I verify.py ledger ledger.jsonl bundles --key anchor.pub{rep ? ` --head ledger-head-${rep.id}.json` : ''}</code>
      </section>
      <section>
        <h2>{t.bundles} <span className={s.h2s}>{ev.data.length}</span></h2>
        <DataTable caption={t.bundles} columns={cols} rows={ev.data} rowKey={(b) => String(b.id)} pageSize={15}
          rowState={(b) => (b.verdict === 'OK' ? undefined : b.verdict === 'CRITICAL' ? 'critical' : 'bad')} />
      </section>
    </main>
  )
}
