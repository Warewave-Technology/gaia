import { createContext, useCallback, useContext, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { api, markActivity } from '../../api/client'
import type { components } from '../../api/schema'
import { useT } from '../../i18n'
import { ApiError, unwrap } from '../api/errors'
import { getAssertion, jsonB64url, webauthnAvailable } from '../api/webauthn'
import { Button, Notice } from '../ui'
import s from './StepUp.module.css'

// The step-up dialog (ADR-008 §4): a destructive or security action asks for the factor again, every time. A local
// user touches the security key or types a code from the app; a single sign-on user signs in again at the IdP in a
// window that closes itself. The answer goes with the action as headers.

type Challenge = components['schemas']['StepUpChallenge']
type Headers = Record<string, string>
type Ask = (method: string, path: string) => Promise<Headers>

const Ctx = createContext<Ask | null>(null)

/** The step-up of the action method + path: resolves to the headers to send it with, rejects when the user cancels. */
export function useStepUp(): Ask {
  const ask = useContext(Ctx)
  if (!ask) throw new Error('useStepUp outside StepUpProvider')
  return ask
}

/** Runs send without a step-up, and again with one when the server asks for it. */
export async function withStepUp<T>(ask: Ask, method: string, path: string, send: (h?: Headers) => Promise<T>): Promise<T> {
  try {
    return await send()
  } catch (e) {
    if (!(e instanceof ApiError) || e.code !== 'step_up_required') throw e
    return send(await ask(method, path))
  }
}

/** Downloads an export that needs a step-up (an evidence export, a readiness report). */
export async function download(ask: Ask, path: string, name: string) {
  const get = async (h?: Headers) => {
    const hs = new Headers(h)
    markActivity(hs)
    const r = await fetch(path, { credentials: 'same-origin', headers: hs })
    if (!r.ok) {
      const b = (await r.json().catch(() => ({}))) as { code?: string; error?: string }
      throw new ApiError(r.status, b.code ?? 'internal', b.error ?? r.statusText)
    }
    return r.blob()
  }
  const blob = await withStepUp(ask, 'GET', path, get)
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 10_000)
}

type Open = { ch: Challenge; method: string; path: string; resolve: (h: Headers) => void; reject: (e: Error) => void }

export function StepUpProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState<Open | null>(null)
  const ask = useCallback<Ask>(async (method, path) => {
    const ch = await unwrap(api.POST('/api/v1/step-up', { body: { method, path } }))
    return new Promise<Headers>((resolve, reject) => setOpen({ ch, method, path, resolve, reject }))
  }, [])
  return (
    <Ctx.Provider value={ask}>
      {children}
      {open && <StepUpDialog o={open} done={() => setOpen(null)} />}
    </Ctx.Provider>
  )
}

function StepUpDialog({ o, done }: { o: Open; done: () => void }) {
  const t = useT().stepUp
  const [code, setCode] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const ref = useRef<HTMLDialogElement>(null)
  const finish = (h: Headers) => { o.resolve(h); done() }
  const cancel = () => { o.reject(new ApiError(403, 'step_up_cancelled', t.cancelled)); done() }
  const factors = o.ch.factors ?? []
  async function key() {
    setBusy(true)
    setErr(null)
    try {
      const a = await getAssertion(o.ch.webauthn)
      finish({ 'X-Gaia-Step-Up': o.ch.id, 'X-Gaia-Step-Up-Proof': jsonB64url({ method: 'webauthn', webauthn: a }) })
    } catch (e) {
      setErr(t.keyFailed(e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }
  function totp(e: FormEvent) {
    e.preventDefault()
    finish({ 'X-Gaia-Step-Up': o.ch.id, 'X-Gaia-Step-Up-Proof': jsonB64url({ method: 'totp', code }) })
  }
  function idp() {
    const w = window.open(o.ch.redirect_url ?? '', 'gaia-step-up', 'width=520,height=680')
    if (!w) { setErr(t.popup); return }
    const timer = setInterval(() => {
      try {
        if (w.closed) { clearInterval(timer); return }
        if (w.location.origin === window.location.origin && w.location.search.includes('step_up=')) {
          clearInterval(timer)
          w.close()
          finish({ 'X-Gaia-Step-Up': o.ch.id })
        }
      } catch {
        // still at the identity provider (another origin)
      }
    }, 400)
  }
  return (
    <dialog ref={(d) => { ref.current = d; if (d && !d.open) d.showModal() }} className={s.dialog} onCancel={cancel} aria-labelledby="su-title">
      <h2 id="su-title">{t.title}</h2>
      <p className={s.action}><code>{o.method} {o.path}</code></p>
      <p className={s.hint}>{t.why}</p>
      {o.ch.redirect_url && <Button onClick={idp}>{t.idp}</Button>}
      {factors.includes('webauthn') && (
        <Button onClick={key} disabled={busy || !webauthnAvailable()}>{busy ? t.waiting : t.key}</Button>
      )}
      {factors.includes('totp') && (
        <form onSubmit={totp} className={s.form}>
          <label>
            <span>{t.code}</span>
            <input inputMode="numeric" autoComplete="one-time-code" pattern="[0-9 ]{6,7}" required value={code} onChange={(e) => setCode(e.target.value)} autoFocus />
          </label>
          <Button type="submit">{t.verify}</Button>
        </form>
      )}
      {err && <Notice tone="critical">{err}</Notice>}
      <Button variant="quiet" onClick={cancel}>{t.cancel}</Button>
    </dialog>
  )
}
