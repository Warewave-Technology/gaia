import { createContext, useCallback, useContext, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { api, markActivity } from '../../api/client'
import type { components } from '../../api/schema'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { ApiError, unwrap } from '../api/errors'
import { getAssertion, jsonB64url, webauthnAvailable } from '../api/webauthn'
import { Button, Notice, TechDetails } from '../ui'
import { stepUpPhase } from './stepUpClock'
import s from './StepUp.module.css'

// The step-up dialog (ADR-008 §4 and amendment 4; v2 design docs/design/v2/Login.dc.html, "Confirm action"): a
// destructive or security action asks for the factor again, every time. It names the action and what it acts on; a
// local user touches the security key or types a code from the app, a single sign-on user signs in again at the IdP in
// a window that closes itself; then the action's own button sends it with the answer as headers. More than 120 s
// between the step-up and the button, and the factor is asked again (stepUpClock.ts). Nothing is remembered: the next
// action asks again.

type Challenge = components['schemas']['StepUpChallenge']
type Headers = Record<string, string>

/** What the caller can say about the action, for the dialog: its title (else named from the route), what it acts on,
 * the action button's word (else named from the route), and a name the user must type first (a restore over a running
 * VM, ADR-008 §4). */
export type StepUpAbout = {
  title?: string
  facts?: { label: string; value: ReactNode }[]
  confirm?: string
  typeToConfirm?: string
}
type Ask = (method: string, path: string, about?: StepUpAbout) => Promise<Headers>

const Ctx = createContext<Ask | null>(null)

/** The step-up of the action method + path: resolves to the headers to send it with, rejects when the user cancels. */
export function useStepUp(): Ask {
  const ask = useContext(Ctx)
  if (!ask) throw new Error('useStepUp outside StepUpProvider')
  return ask
}

/** Runs send without a step-up, and again with one when the server asks for it. */
export async function withStepUp<T>(ask: Ask, method: string, path: string, send: (h?: Headers) => Promise<T>, about?: StepUpAbout): Promise<T> {
  try {
    return await send()
  } catch (e) {
    if (!(e instanceof ApiError) || e.code !== 'step_up_required') throw e
    return send(await ask(method, path, about))
  }
}

/** Downloads an export that needs a step-up (an evidence export, a readiness report). */
export async function download(ask: Ask, path: string, name: string, about?: StepUpAbout) {
  const get = async (h?: Headers) => {
    const hs = new Headers(h)
    markActivity(hs)
    const r = await fetch(path, { credentials: 'same-origin', headers: hs })
    if (!r.ok) {
      const b = (await r.json().catch(() => ({}))) as { code?: string; error?: string }
      throw new ApiError(r.status, b.code ?? 'internal', b.error ?? r.statusText)
    }
    return { blob: await r.blob(), headers: r.headers }
  }
  const { blob, headers } = await withStepUp(ask, 'GET', path, get, about)
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 10_000)
  // The response's headers: what the server says about the file (e.g. an export the ledger could not record).
  return headers
}

/** An open dialog: a challenge to answer, or (ch null) the identity provider's refusal to check a factor now. */
type Open = { ch: Challenge | null; failed?: ApiError; method: string; path: string; about?: StepUpAbout; resolve: (h: Headers) => void; reject: (e: Error) => void }

/** Asks the server for a step-up of one action: a challenge bound to the action and its target. */
const askServer = (method: string, path: string) => unwrap(api.POST('/api/v1/step-up', { body: { method, path } }))

export function StepUpProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState<Open | null>(null)
  const ask = useCallback<Ask>(async (method, path, about) => {
    let ch: Challenge
    try {
      ch = await askServer(method, path)
    } catch (e) {
      // A single sign-on session whose IdP did not answer at the last refresh cannot step up (§2): the dialog says so,
      // and closing it rejects with the server's refusal.
      if (!(e instanceof ApiError) || e.code !== 'idp_unreachable') throw e
      return new Promise<Headers>((resolve, reject) => setOpen({ ch: null, failed: e, method, path, about, resolve, reject }))
    }
    return new Promise<Headers>((resolve, reject) => setOpen({ ch, method, path, about, resolve, reject }))
  }, [])
  return (
    <Ctx.Provider value={ask}>
      {children}
      {open && <StepUpDialog o={open} done={() => setOpen(null)} />}
    </Ctx.Provider>
  )
}

/** The current time, ticking every second while `on`. */
function useNow(on: boolean) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!on) return
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [on])
  return now
}

function StepUpDialog({ o, done }: { o: Open; done: () => void }) {
  const t = useT().stepUp
  const [ch, setCh] = useState<Challenge | null>(o.ch)
  const [askedAt, setAskedAt] = useState(() => Date.now())
  const [code, setCode] = useState('')
  const [typed, setTyped] = useState('')
  const [proof, setProof] = useState<Headers | null>(null) // the key's assertion or the IdP's re-sign-in, for this action
  const [factorAt, setFactorAt] = useState<number | null>(null)
  const [again, setAgain] = useState(false) // the factor is asked again: the last one came too late
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState<'key' | 'idp' | 'ask' | null>(null)
  const timer = useRef<ReturnType<typeof setInterval> | null>(null)
  useEffect(() => () => { if (timer.current) clearInterval(timer.current) }, [])
  const factors = ch?.factors ?? []
  const codeOK = /^[0-9]{3} ?[0-9]{3}$/.test(code.trim())
  // A code from the app is the factor from the moment it is complete; another code typed is a new factor.
  useEffect(() => {
    if (!proof) setFactorAt(codeOK ? Date.now() : null)
  }, [code, codeOK, proof])
  const now = useNow(!!ch)
  const clock = ch ? stepUpPhase(askedAt, factorAt, now) : null
  const expired = clock?.phase === 'expired'
  const cancel = () => { o.reject(o.failed ?? new ApiError(403, 'step_up_cancelled', t.cancelled)); done() }
  const title = o.about?.title ?? (ch ? t.action(ch.action) : t.title)
  const facts = o.about?.facts ?? [{ label: t.target, value: t.targetOf(o.path) }]
  const named = !o.about?.typeToConfirm || typed.trim() === o.about.typeToConfirm
  const given = proof !== null || (factors.includes('totp') && codeOK)
  const ready = !!ch && named && given && !expired
  const button = o.about?.confirm ?? (ch ? t.button(ch.action) : t.verify)

  /** A new step-up for the same action: the old one is too old to use. */
  async function askAgain() {
    const after = clock?.phase === 'expired' && clock.afterFactor
    setBusy('ask')
    setErr(null)
    try {
      const next = await askServer(o.method, o.path)
      setCh(next)
      setAskedAt(Date.now())
      setProof(null)
      setCode('')
      setFactorAt(null)
      setAgain(after)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }
  async function key() {
    if (!ch) return
    setBusy('key')
    setErr(null)
    try {
      const a = await getAssertion(ch.webauthn)
      setProof({ 'X-Gaia-Step-Up': ch.id, 'X-Gaia-Step-Up-Proof': jsonB64url({ method: 'webauthn', webauthn: a }) })
      setFactorAt(Date.now())
    } catch (e) {
      setErr(t.keyFailed(e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy(null)
    }
  }
  function idp() {
    if (!ch) return
    setErr(null)
    const w = window.open(ch.redirect_url ?? '', 'gaia-step-up', 'width=520,height=680')
    if (!w) { setErr(t.popup); return }
    setBusy('idp')
    if (timer.current) clearInterval(timer.current)
    timer.current = setInterval(() => {
      try {
        if (w.closed) { if (timer.current) clearInterval(timer.current); setBusy(null); return }
        if (w.location.origin === window.location.origin && w.location.search.includes('step_up=')) {
          if (timer.current) clearInterval(timer.current)
          w.close()
          setBusy(null)
          setProof({ 'X-Gaia-Step-Up': ch.id })
          setFactorAt(Date.now())
        }
      } catch {
        // still at the identity provider (another origin)
      }
    }, 400)
  }
  function confirm(e: FormEvent) {
    e.preventDefault()
    if (!ch || !ready) return
    // Checked again at the press itself: the tick may be up to a second behind.
    if (stepUpPhase(askedAt, factorAt, Date.now()).phase === 'expired') return
    o.resolve(proof ?? { 'X-Gaia-Step-Up': ch.id, 'X-Gaia-Step-Up-Proof': jsonB64url({ method: 'totp', code }) })
    done()
  }

  let control: ReactNode
  if (!ch) {
    control = <Notice tone="critical">{t.idpUnreachable}</Notice>
  } else if (clock?.phase === 'expired') {
    control = (
      <>
        <Notice tone="warning">{clock.afterFactor ? t.tooLate(button) : t.tooOld}</Notice>
        <Button type="button" className={s.btn} onClick={askAgain} disabled={busy === 'ask'} autoFocus>{busy === 'ask' ? t.asking : t.askAgain}</Button>
      </>
    )
  } else if (proof) {
    control = <p className={s.done} role="status">{ch.redirect_url ? t.idpDone : t.keyDone}</p>
  } else {
    control = (
      <>
        {again && <p className={s.again} role="status">{t.askedAgain}</p>}
        {ch.redirect_url && <Button type="button" className={s.btn} onClick={idp} disabled={busy === 'idp'}>{busy === 'idp' ? t.idpWaiting : t.idp}</Button>}
        {factors.includes('webauthn') && (
          <Button type="button" className={s.btn} onClick={key} disabled={busy !== null || !webauthnAvailable()} autoFocus={!o.about?.typeToConfirm}>
            {busy === 'key' ? t.waiting : t.key}
          </Button>
        )}
        {factors.includes('totp') && (
          <>
            {factors.includes('webauthn') && <p className={s.or}>{t.orCode}</p>}
            <label className={s.field}>
              <span>{t.code}</span>
              <input className={s.otp} inputMode="numeric" autoComplete="one-time-code" pattern="[0-9 ]{6,7}" value={code} onChange={(e) => setCode(e.target.value)}
                autoFocus={!o.about?.typeToConfirm && !factors.includes('webauthn')} />
            </label>
          </>
        )}
      </>
    )
  }
  return (
    <dialog ref={(d) => { if (d && !d.open) d.showModal() }} className={s.dialog} onCancel={(e) => { e.preventDefault(); cancel() }}
      aria-labelledby="su-title" aria-describedby="su-why">
      <form onSubmit={confirm} className={s.form}>
        <h2 id="su-title">{title}</h2>
        <dl className={s.kv}>
          {facts.map((f) => <div key={f.label} className={s.fact}><dt>{f.label}</dt><dd>{f.value}</dd></div>)}
        </dl>
        {ch && o.about?.typeToConfirm && (
          <label className={s.field}>
            <span>{t.typeName(o.about.typeToConfirm)}</span>
            <input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} autoFocus />
          </label>
        )}
        {control}
        {err && <Notice tone="critical">{err}</Notice>}
        {clock?.phase === 'ready' && (
          <p className={s.left} data-step-up-left={clock.left}>{t.pressWithin(button, dur(clock.left))}</p>
        )}
        <div className={s.actions}>
          <Button type="button" variant="quiet" className={s.btn} onClick={cancel}>{ch ? t.cancel : t.close}</Button>
          {ch && <Button type="submit" className={`${s.btn} ${s.danger}`} disabled={!ready}>{button}</Button>}
        </div>
        <p id="su-why" className={s.hint}>{t.why}</p>
        <TechDetails summary={t.request}>{`${o.method} ${o.path}`}</TechDetails>
      </form>
    </dialog>
  )
}
