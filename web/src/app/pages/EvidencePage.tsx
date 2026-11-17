import { Navigate } from 'react-router'
import { useT } from '../../i18n'
import { serverTime } from '../../lib/time'
import { can, useMe } from '../api/session'
import { useDailyReports, useEvidence, useSystem, useVMs } from '../api/views'
import { Failed, Loading } from '../layout/States'
import { ButtonLink, StatusChip } from '../ui'
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
  return (
    <main className={s.page}>
      <section className={s.head}>
        <div>
          <h1>{t.title}</h1>
          <p className={s.lede}>{t.lede}</p>
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
        <table className={s.list}>
          <thead><tr><th>{t.col.seq}</th><th>{t.col.bundle}</th><th>{t.col.vm}</th><th>{t.col.test}</th><th>{t.col.verdict}</th><th>{t.col.sealed}</th></tr></thead>
          <tbody>
            {ev.data.map((b) => (
              <tr key={b.id}>
                <td className={s.num}>{b.ledger_seq}</td>
                <td><code>{b.bundle_id}</code></td>
                <td>{b.vm_id ? names.get(b.vm_id) ?? b.vm_id : '—'}</td>
                <td className={s.num}>{b.test_id ?? '—'}</td>
                <td><StatusChip status={b.verdict} size="xs" /></td>
                <td className={s.num}>{fmt.date(b.created_at)} {fmt.clock(b.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </main>
  )
}
