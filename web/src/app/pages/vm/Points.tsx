import { useState, type FormEvent } from 'react'
import { useT } from '../../../i18n'
import type { Fmt } from '../../../lib/time'
import { can, useMe } from '../../api/session'
import { pointsFromView, useVMPoints, type VMPoint } from '../../api/vm'
import { useForgetPoint, type VMView } from '../../api/views'
import { useStepUp } from '../../layout/StepUp'
import { Button, DataTable, Notice, PointProof, type Column } from '../../ui'
import { proofOf } from './logic'
import s from '../VMPage.module.css'

/** Every kept point, newest first, in server pages; a damaged one can be forgotten by an admin (ADR-007 C6). */
export function Points({ v, fmt, selected }: { v: VMView; fmt: Fmt; selected: number | null }) {
  const t = useT().vm3
  const me = useMe().data
  const admin = can(me, 'admin')
  const { pages, pruned, missing } = useVMPoints(v.vm.id)
  const ret = v.status.proven?.point_id ?? null
  const local = missing ? pointsFromView(v) : undefined
  const kept = missing ? v.points.length : pages.total ?? v.points.length
  const columns: Column<VMPoint>[] = [
    { key: 'point', header: t.col.point, width: '110px', cell: (r) => <span className={s.mono}>{r.id}</span> },
    { key: 'taken', header: t.col.taken, width: '150px', cell: (r) => t.cap(t.day(fmt.at(r.taken_at))) },
    { key: 'kind', header: t.col.kind, width: '110px', title: (r) => r.kind_reason || undefined, cell: (r) => <span className={s.sub}>{r.kind}</span> },
    { key: 'size', header: t.col.size, width: '100px', numeric: true, cell: (r) => <span className={s.mono}>{t.size(r.bytes)}</span> },
    { key: 'proof', header: t.col.proof, width: '230px', cell: (r) => { const p = proofOf(r, ret, t); return <PointProof proof={p.proof} note={p.note} /> } },
    { key: 'detail', header: t.col.detail, title: (r) => proofOf(r, ret, t).detail || undefined, cell: (r) => <span className={s.sub}>{proofOf(r, ret, t).detail}</span> },
  ]
  if (admin) {
    columns.push({
      key: 'act', header: t.col.action, width: '120px',
      cell: (r, ctx) => (r.check_state === 'failed'
        ? <Button variant="quiet" aria-expanded={ctx.open} aria-controls={ctx.open ? ctx.detailId : undefined} onClick={ctx.toggle}>{t.forget}</Button> : null),
    })
  }
  return (
    <div className={s.tabbody}>
      <p className={s.sub}>{t.pointsHint(kept, missing ? v.points_total - v.points.length : pruned ?? 0)}</p>
      <DataTable<VMPoint> caption={t.pointsTable} columns={columns} rowKey={(r) => String(r.id)} empty={t.noPoints} height="420px"
        {...(local ? { rows: local } : { pages })}
        rowState={(r) => (r.id === selected ? 'mark' : r.check_state === 'failed' ? 'critical' : undefined)}
        detail={admin ? (r) => (r.check_state === 'failed' ? <Forget point={r} /> : null) : undefined} />
    </div>
  )
}

/**
 * Forget a damaged point (ADR-007 C6): it is deleted from its repository and marked pruned, as retention would. Admins
 * only, a reason that goes to the audit log, and the step-up dialog for this action (ADR-008 §4).
 */
export function Forget({ point }: { point: { id: number } }) {
  const t = useT().vm3
  const ask = useStepUp()
  const m = useForgetPoint(ask)
  const [reason, setReason] = useState('')
  if (m.isSuccess) return <span role="status" className={s.muted}>{t.forgotten(point.id)}</span>
  const send = (e: FormEvent) => {
    e.preventDefault()
    m.mutate({ id: point.id, reason: reason.trim() })
  }
  return (
    <form className={s.form} onSubmit={send}>
      <p className={s.sub}>{t.forgetWhat(point.id)}</p>
      <label>
        <span>{t.forgetReason}</span>
        <input value={reason} onChange={(e) => setReason(e.target.value)} required minLength={3} autoFocus />
      </label>
      <Button type="submit" disabled={m.isPending || reason.trim().length < 3}>{t.forgetConfirm(point.id)}</Button>
      {m.error && <Notice tone="critical">{m.error.message}</Notice>}
    </form>
  )
}
