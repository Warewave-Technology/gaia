import { useEffect, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { useT } from '../../i18n'
import { ApiError, unwrap } from '../api/errors'
import { useSigninOptions } from '../api/session'
import { getAssertion, webauthnAvailable } from '../api/webauthn'
import { useStepUp, withStepUp } from '../layout/StepUp'
import { Button, Notice, TechDetails } from '../ui'
import { SigninFrame } from './Login'
import s from './Login.module.css'

// Approvals for the CLI (ADR-008 §9): a CLI sign-in (gaia login -browser), and a step-up the CLI cannot answer itself
// (a security key). The user is signed in here; the approval is a factor answered here. Both on the sign-in card.

export function ApproveCLI() {
  const t = useT().approve
  const ask = useStepUp()
  const opts = useSigninOptions()
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
        unwrap(api.POST('/api/v1/login/cli/approve', { body: { user_code: code.trim() }, headers: h })),
      { facts: [{ label: t.code, value: <code>{code.trim()}</code> }], confirm: t.approve })
      setState('done')
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e))
      setState('idle')
    }
  }
  return (
    <SigninFrame instance={opts.data?.instance}>
      <h1>{t.cliTitle}</h1>
      {state === 'done' ? <p className={s.done} role="status">{t.cliDone}</p> : (
        <form className={s.fields} onSubmit={submit}>
          <p className={s.sub}>{t.cliHint}</p>
          <label className={s.field}>
            <span>{t.code}</span>
            <input className={s.code} required value={code} onChange={(e) => setCode(e.target.value.toUpperCase())} autoFocus spellCheck={false} />
          </label>
          <Button type="submit" className={`${s.btn} ${s.pri}`} disabled={state === 'busy'}>{t.approve}</Button>
        </form>
      )}
      {err && <Notice tone="critical">{err}</Notice>}
    </SigninFrame>
  )
}

export function ApproveStepUp() {
  const t = useT().approve
  const su = useT().stepUp
  const opts = useSigninOptions()
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
    <SigninFrame instance={opts.data?.instance}>
      <h1>{t.stepUpTitle}</h1>
      {ch && (
        <dl className={s.kv}>
          <dt>{t.action}</dt><dd>{su.action(ch.action)}</dd>
          <dt>{t.target}</dt><dd>{su.targetOf(ch.target)}</dd>
        </dl>
      )}
      {ch && <TechDetails summary={su.request}>{`${ch.action.split(' ')[0]} ${ch.target}`}</TechDetails>}
      {done ? <p className={s.done} role="status">{t.stepUpDone}</p> : ch && (
        <>
          <p className={s.sub}>{t.stepUpHint}</p>
          {f.includes('webauthn') && (
            <Button className={`${s.btn} ${s.pri}`} disabled={!webauthnAvailable()} autoFocus
              onClick={async () => {
                let a: unknown
                try {
                  a = await getAssertion(ch.webauthn)
                } catch (e) {
                  setErr(su.keyFailed(e instanceof Error ? e.message : String(e))) // was an unhandled rejection: nothing shown
                  return
                }
                await approve({ method: 'webauthn', webauthn: a })
              }}>{t.key}</Button>
          )}
          {f.includes('totp') && (
            <>
              {f.includes('webauthn') && <p className={s.or}>{t.orCode}</p>}
              <form className={s.fields} onSubmit={(e) => { e.preventDefault(); approve({ method: 'totp', code }) }}>
                <label className={s.field}>
                  <span>{t.appCode}</span>
                  <input className={s.otp} inputMode="numeric" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)}
                    autoFocus={!f.includes('webauthn')} />
                </label>
                <Button type="submit" className={f.includes('webauthn') ? s.btn : `${s.btn} ${s.pri}`}>{t.approve}</Button>
              </form>
            </>
          )}
          <p className={s.sub}>{su.why}</p>
        </>
      )}
      {err && <Notice tone="critical">{err}</Notice>}
    </SigninFrame>
  )
}
