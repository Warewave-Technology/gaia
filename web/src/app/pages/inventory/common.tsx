import { useState, type ReactNode } from 'react'
import { useT } from '../../../i18n'
import { ApiError } from '../../api/errors'
import { useRefresh } from '../../api/inventory'
import { useStepUp, withStepUp } from '../../layout/StepUp'
import { Button, Notice } from '../../ui'
import s from '../Inventory.module.css'

type Headers = Record<string, string>

/** The words of a failed call, for the line under the table. */
export function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/**
 * The Inventory's changes: each runs through withStepUp (the server asks for the factor when the action needs it,
 * ADR-008 §4), then the tables are read again. One runs at a time per table; its key says which button is busy, and
 * the outcome (done or the error) is the line shown under the table.
 */
export function useAction() {
  const t = useT().inventory
  const ask = useStepUp()
  const refresh = useRefresh()
  const [busy, setBusy] = useState<string | null>(null)
  const [done, setDone] = useState<string | null>(null)
  const [err, setErr] = useState<string | null>(null)
  async function run<T>(key: string, method: string, path: string, send: (h?: Headers) => Promise<T>, ok?: (r: T) => string): Promise<T | undefined> {
    setBusy(key)
    setErr(null)
    setDone(null)
    try {
      const r = await withStepUp(ask, method, path, send)
      if (ok) setDone(ok(r))
      await refresh()
      return r
    } catch (e) {
      if (e instanceof ApiError && e.code === 'step_up_cancelled') setDone(t.cancelled)
      else setErr(message(e))
      return undefined
    } finally {
      setBusy(null)
    }
  }
  const outcome = (err || done) && (
    err ? <Notice tone="critical">{err}</Notice> : <p className={s.ok} role="status">{done}</p>
  )
  return { busy, run, outcome, clear: () => { setErr(null); setDone(null) } }
}

/** A destructive button that asks once more in place before it acts (the step-up, if any, comes after). */
export function Confirm({ label, question, busy, onYes, danger }: { label: string; question: string; busy: boolean; onYes: () => void; danger?: boolean }) {
  const t = useT().inventory
  const [asking, setAsking] = useState(false)
  if (!asking) return <Button variant="quiet" className={danger ? s.danger : ''} onClick={() => setAsking(true)} disabled={busy}>{label}</Button>
  return (
    <span className={s.inline}>
      <span className={s.hint}>{question}</span>
      <Button className={s.danger} onClick={() => { setAsking(false); onYes() }} disabled={busy}>{busy ? t.working : label}</Button>
      <Button variant="quiet" onClick={() => setAsking(false)}>{t.cancel}</Button>
    </span>
  )
}

/** A labelled form field. */
export function Field({ label, hint, children, wide }: { label: string; hint?: string; children: ReactNode; wide?: boolean }) {
  return (
    <label className={`${s.field} ${wide ? s.wide : ''}`}>
      <span>{label}</span>
      {children}
      {hint && <small>{hint}</small>}
    </label>
  )
}

/** A rule ADR-008 fixes: labelled, with no control (a fixed setting has no switch). */
export function Fixed() {
  return <span className={s.lock}>{useT().security.fixed}</span>
}
