import { useState } from 'react'
import { useT } from '../../../i18n'
import type { Fmt } from '../../../lib/time'
import { can, useMe } from '../../api/session'
import type { TimelineEvent, VMView } from '../../api/views'
import { HistoryStrip, Legend, SectionHead, Seg, StatusChip } from '../../ui'
import type { SetVMFilters, VMFilters } from '../VMPage'
import { proofOf, when, type Point } from './common'
import { Forget } from './Points'
import s from '../VMPage.module.css'

const KIND_STATUS: Record<string, string> = {
  test_ok: 'OK', test_warning: 'WARNING', test_failed: 'CRITICAL', test_unknown: 'UNKNOWN', backup_failed: 'CRITICAL', damaged: 'CRITICAL',
}

/** An event chosen on the strip that is not a point the page knows (a failed backup, a test of a point not kept). */
type Other = { kind: string; id: number; at: string }

/**
 * The history strip (24 h or 7 days) with every mark a button: a backup or a damaged point selects that point, a test
 * the point it tested. The selected point is in the URL (`?point=`), so the global search can open the page on it.
 */
export function History({ v, fmt, f, setF, windowH }: { v: VMView; fmt: Fmt; f: VMFilters; setF: SetVMFilters; windowH: 24 | 168 }) {
  const t = useT().vm
  const [other, setOther] = useState<Other | null>(null)
  const events = v.status.events
  const end = Date.parse(v.clock.now)
  const start = end - v.window_s * 1000
  const pointOf = (e: TimelineEvent): number | null => {
    if (e.kind === 'backup' || e.kind === 'damaged') return e.id
    if (e.kind.startsWith('test_')) return v.tests.find((x) => x.id === e.id)?.point_id ?? null
    return null
  }
  const newest = [...v.points].sort((a, b) => b.taken_at.localeCompare(a.taken_at))[0]
  const selID = f.point ? Number(f.point) : other ? null : v.status.proven?.point_id ?? newest?.id ?? null
  const pick = (e: TimelineEvent) => {
    const p = pointOf(e)
    if (p !== null) {
      setOther(null)
      setF({ point: String(p) })
    } else {
      setOther({ kind: e.kind, id: e.id, at: e.at })
      setF({ point: '' })
    }
  }
  const isSel = (e: TimelineEvent) => (other ? other.kind === e.kind && other.id === e.id : selID !== null && pointOf(e) === selID)
  const mid = new Date(start + (end - start) / 2).toISOString()
  return (
    <section className={s.sec} aria-labelledby="history">
      <SectionHead id="history" title={t.history} hint={t.historyHint}>
        <Seg label={t.range} value={windowH === 168 ? '7d' : '24h'} options={[['24h', t.h24], ['7d', t.d7]]}
          onChange={(r) => setF({ range: r === '7d' ? '7d' : '' })} />
      </SectionHead>
      <div className={s.strip}>
        <HistoryStrip events={events} now={v.clock.now} windowS={v.window_s} returnAt={v.status.proven?.point_taken_at} width={1300} height={64} big />
        <div className={s.marks}>
          {events.map((e) => {
            const x = (100 * (Date.parse(e.at) - start)) / (end - start)
            if (x < 0 || x > 100) return null
            return (
              <button key={`${e.kind}${e.id}`} type="button" className={s.mark} style={{ left: `${x}%` }} aria-pressed={isSel(e)}
                aria-label={t.eventLabel(e.kind, e.id, when(fmt, v.clock, e.at, t))} title={t.eventLabel(e.kind, e.id, when(fmt, v.clock, e.at, t))}
                onClick={() => pick(e)} />
            )
          })}
        </div>
      </div>
      <div className={s.axis} aria-hidden="true">
        <span>{when(fmt, v.clock, new Date(start).toISOString(), t)}</span>
        <span>{when(fmt, v.clock, mid, t)}</span>
        <span>{t.now}</span>
      </div>
      <Legend />
      {other ? <OtherPanel o={other} v={v} fmt={fmt} /> : selID !== null && <PointPanel id={selID} v={v} fmt={fmt} />}
    </section>
  )
}

function PointPanel({ id, v, fmt }: { id: number; v: VMView; fmt: Fmt }) {
  const t = useT().vm
  const me = useMe().data
  const p: Point | undefined = v.points.find((x) => x.id === id)
  if (!p) {
    return (
      <div className={s.sel}>
        <StatusChip status="UNKNOWN" size="sm" word={t.word.notKept} />
        <b>{t.pointN(id)}</b>
        <span>{t.pt.notKept}</span>
      </div>
    )
  }
  const pf = proofOf(p, v, t)
  return (
    <div className={s.sel}>
      <StatusChip status={pf.status} size="sm" word={pf.word} />
      <b>{t.pointAt(p.id, when(fmt, v.clock, p.taken_at, t))}</b>
      <span>{pf.text}</span>
      {p.check_state === 'failed' && can(me, 'admin') && <Forget point={p} />}
    </div>
  )
}

function OtherPanel({ o, v, fmt }: { o: Other; v: VMView; fmt: Fmt }) {
  const t = useT().vm
  const st = KIND_STATUS[o.kind] ?? 'UNKNOWN'
  const word = ({ backup_failed: t.word.backupFailed, test_ok: t.word.passed, test_warning: t.word.warned, test_failed: t.word.failed } as Record<string, string>)[o.kind]
    ?? t.word.notProven
  return (
    <div className={s.sel}>
      <StatusChip status={st} size="sm" word={word} />
      <b>{t.eventLabel(o.kind, o.id, when(fmt, v.clock, o.at, t))}</b>
      <span>{o.kind === 'backup_failed' ? t.pt.backupFailed(o.id) : t.pt.testGone(o.id)}</span>
    </div>
  )
}
