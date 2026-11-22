import { Fragment, useState, type FormEvent } from 'react'
import { useT } from '../../../i18n'
import { dur } from '../../../lib/duration'
import { secondsBetween, type Fmt } from '../../../lib/time'
import { can, useMe } from '../../api/session'
import { useForgetPoint, type VMView } from '../../api/views'
import { useStepUp } from '../../layout/StepUp'
import { Button, MoreRows, Notice, SectionHead, StatusChip } from '../../ui'
import { proofOf, when, type Point } from './common'
import s from '../VMPage.module.css'

const PAGE = 12

/** Every kept point, newest first, with its proof; a damaged one can be forgotten (admins, ADR-007 C6). */
export function Points({ v, fmt, selected, onSelect }: { v: VMView; fmt: Fmt; selected: number | null; onSelect: (id: number) => void }) {
  const t = useT().vm
  const me = useMe().data
  const admin = can(me, 'admin')
  const [shown, setShown] = useState(PAGE)
  const [forget, setForget] = useState<number | null>(null)
  const pts = [...v.points].sort((a, b) => b.taken_at.localeCompare(a.taken_at))
  const ret = v.status.proven?.point_id
  return (
    <section className={s.sec} aria-labelledby="points">
      <SectionHead id="points" title={t.points} hint={t.pointsSub(v.points.length, v.points_total - v.points.length)} />
      {pts.length === 0 ? <p className={s.muted}>{t.noPoints}</p> : (
        <>
          <div className={s.scroll}>
            <table className={s.tbl}>
              <thead>
                <tr>
                  <th scope="col">{t.col.point}</th>
                  <th scope="col">{t.col.taken}</th>
                  <th scope="col" className={s.num}>{t.col.size}</th>
                  <th scope="col">{t.col.proof}</th>
                  {admin && <th scope="col"><span className={s.vh}>{t.col.action}</span></th>}
                </tr>
              </thead>
              <tbody>
                {pts.slice(0, shown).map((p) => {
                  const pf = proofOf(p, v, t)
                  const damaged = p.check_state === 'failed'
                  return (
                    <Fragment key={p.id}>
                      <tr className={`${p.id === selected ? s.rsel : ''} ${damaged ? s.rCRITICAL : ''}`}>
                        <th scope="row">
                          <button type="button" className={s.pid} aria-pressed={p.id === selected} onClick={() => onSelect(p.id)}>{p.id}</button>
                          {p.id === ret && <span className={s.rmark}>{t.returnHere}</span>}
                        </th>
                        <td className={s.nowrap}>{when(fmt, v.clock, p.taken_at, t)}<span className={s.of}>{p.kind}</span></td>
                        <td className={`${s.num} ${s.mono}`}>{t.size(p.bytes)}</td>
                        <td><StatusChip status={pf.status} size="xs" word={pf.word} /> <span className={s.of}>{pf.note}</span></td>
                        {admin && (
                          <td className={s.num}>
                            {damaged && <Button variant="quiet" aria-expanded={forget === p.id} onClick={() => setForget(forget === p.id ? null : p.id)}>{t.forget}</Button>}
                          </td>
                        )}
                      </tr>
                      {forget === p.id && (
                        <tr className={s.subrow}><td colSpan={5}><Forget point={p} open onDone={() => setForget(null)} /></td></tr>
                      )}
                    </Fragment>
                  )
                })}
              </tbody>
            </table>
          </div>
          <MoreRows auto={false} shown={Math.min(shown, pts.length)} total={pts.length} more={shown < pts.length} loading={false} onMore={() => setShown(shown + 25)} />
        </>
      )}
    </section>
  )
}

/**
 * Forget a damaged point (ADR-007 C6): it is deleted from its repository and marked pruned, as retention would. Admins
 * only, a reason that goes to the audit log, and a step-up for this action (ADR-008 §4).
 */
export function Forget({ point, open: start = false, onDone }: { point: Point; open?: boolean; onDone?: () => void }) {
  const t = useT().vm
  const ask = useStepUp()
  const m = useForgetPoint(ask)
  const [open, setOpen] = useState(start)
  const [reason, setReason] = useState('')
  if (m.isSuccess) return <span role="status" className={s.muted}>{t.forgotten(point.id)}</span>
  if (!open) return <Button variant="quiet" onClick={() => setOpen(true)}>{t.forget}</Button>
  const send = (e: FormEvent) => {
    e.preventDefault()
    m.mutate({ id: point.id, reason: reason.trim() }, { onSuccess: () => onDone?.() })
  }
  return (
    <form className={s.forget} onSubmit={send}>
      <p className={s.of}>{t.forgetWhat(point.id)}</p>
      <label>
        <span>{t.forgetReason}</span>
        <input value={reason} onChange={(e) => setReason(e.target.value)} required minLength={3} autoFocus />
      </label>
      <Button type="submit" disabled={m.isPending || reason.trim() === ''}>{t.forgetConfirm(point.id)}</Button>
      <Button type="button" variant="quiet" onClick={() => { setOpen(false); onDone?.() }}>{t.cancel}</Button>
      {m.error && <Notice tone="critical">{m.error.message}</Notice>}
    </form>
  )
}

/** Every change of the VM's status since it was added, newest first. */
export function StatusHistory({ v, fmt }: { v: VMView; fmt: Fmt }) {
  const t = useT().vm
  const [shown, setShown] = useState(8)
  const h = v.status_history
  if (h.length === 0) return null
  const rows = h.map((c, i) => ({ c, end: i + 1 < h.length ? h[i + 1].at : v.clock.now, last: i === h.length - 1 })).reverse()
  return (
    <section className={s.sec} aria-labelledby="status-history">
      <SectionHead id="status-history" title={t.statusHistory} hint={t.sinceAdded} />
      <div className={s.scroll}>
        <table className={s.tbl}>
          <thead>
            <tr>
              <th scope="col">{t.col.when}</th>
              <th scope="col">{t.col.change}</th>
              <th scope="col" className={s.num}>{t.col.lasted}</th>
              <th scope="col">{t.col.why}</th>
            </tr>
          </thead>
          <tbody>
            {rows.slice(0, shown).map(({ c, end, last }) => (
              <tr key={c.seq}>
                <td className={s.nowrap}>{when(fmt, v.clock, c.at, t)}</td>
                <td className={s.nowrap}>
                  {c.from ? <StatusChip status={c.from} size="xs" /> : <span className={s.muted}>{t.added}</span>}
                  <span className={s.arrow} aria-label={t.to}>→</span>
                  <StatusChip status={c.to} size="xs" />
                </td>
                <td className={`${s.num} ${s.mono}`}>{last ? t.soFar(dur(secondsBetween(c.at, end))) : dur(secondsBetween(c.at, end))}</td>
                <td className={s.fg2}>{c.reason}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <MoreRows auto={false} shown={Math.min(shown, rows.length)} total={rows.length} more={shown < rows.length} loading={false} onMore={() => setShown(shown + 25)} />
    </section>
  )
}
