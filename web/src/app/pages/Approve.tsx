import { useEffect, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { useT } from '../../i18n'
import { ApiError, unwrap } from '../api/errors'
import { getAssertion, webauthnAvailable } from '../api/webauthn'
import { useStepUp, withStepUp } from '../layout/StepUp'
import { Button, Notice } from '../ui'
import s from './Login.module.css'

// Approvals for the CLI (ADR-008 §9): a CLI sign-in (gaia login -browser), and a step-up the CLI cannot answer itself
// (a security key). The user is signed in here; the approval is a factor answered here.

export function ApproveCLI() {
  const t = useT().approve
  const ask = useStepUp()
  const [params] = useSearchParams()
  const [code, setCode] = useState(params.get('code') ?? '')
  const [state, setState] = useState<'idle' | 'busy' | 'done'>('idle')
  const [err, setErr] = useState<string | null>(null)
  async function submit(e: FormEvent) {
    e.preventDefault()
    setState('busy')
    setErr(null)
    try {
      await withStepUp(ask, 'POST', '/api/v1/login/cli/approve', (h) =>
        unwrap(api.POST('/api/v1/login/cli/approve', { body: { user_code: code.trim() }, headers: h })))
      setState('done')
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e))
      setState('idle')
    }
  }
  return (
    <main className={s.page}>
      <div className={s.form}>
        <h1>{t.cliTitle}</h1>
        {state === 'done' ? <p className={s.hint}>{t.cliDone}</p> : (
          <form className={s.fields} onSubmit={submit}>
            <p className={s.hint}>{t.cliHint}</p>
            <label className={s.field}>
              <span>{t.code}</span>
              <input required value={code} onChange={(e) => setCode(e.target.value.toUpperCase())} autoFocus />
            </label>
            <Button type="submit" disabled={state === 'busy'}>{t.approve}</Button>
          </form>
        )}
        {err && <Notice tone="critical">{err}</Notice>}
      </div>
    </main>
  )
}

export function ApproveStepUp() {
  const t = useT().approve
  const [params] = useSearchParams()
  const id = params.get('id') ?? ''
  const [ch, setCh] = useState<components['schemas']['StepUpChallenge'] | null>(null)
  const [code, setCode] = useState('')
  const [done, setDone] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  useEffect(() => {
    unwrap(api.GET('/api/v1/step-up/{sid}', { params: { path: { sid: id } } })).then(setCh, (e) => setErr(e instanceof ApiError ? e.message : String(e)))
  }, [id])
  async function approve(body: { method: string; code?: string; webauthn?: unknown }) {
    setErr(null)
    try {
      await unwrap(api.POST('/api/v1/step-up/approve', { body: { id, ...body, webauthn: body.webauthn as never } }))
      setDone(true)
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e))
    }
  }
  const f = ch?.factors ?? []
  return (
    <main className={s.page}>
      <div className={s.form}>
        <h1>{t.stepUpTitle}</h1>
        {ch && <p className={s.hint}><code>{ch.action}</code> · {ch.target}</p>}
        {done ? <p className={s.hint}>{t.stepUpDone}</p> : ch && (
          <>
            {f.includes('webauthn') && (
              <Button disabled={!webauthnAvailable()} onClick={async () => approve({ method: 'webauthn', webauthn: await getAssertion(ch.webauthn) })}>{t.key}</Button>
            )}
            {f.includes('totp') && (
              <form className={s.fields} onSubmit={(e) => { e.preventDefault(); approve({ method: 'totp', code }) }}>
                <label className={s.field}>
                  <span>{t.appCode}</span>
                  <input inputMode="numeric" required value={code} onChange={(e) => setCode(e.target.value)} />
                </label>
                <Button type="submit">{t.approve}</Button>
              </form>
            )}
          </>
        )}
        {err && <Notice tone="critical">{err}</Notice>}
      </div>
    </main>
  )
}
