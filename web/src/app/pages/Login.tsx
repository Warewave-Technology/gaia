import { useState, type FormEvent, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Navigate, useNavigate, useSearchParams } from 'react-router'
import { useT } from '../../i18n'
import { ApiError } from '../api/errors'
import { ME_KEY, endedReason, sessionEnded, signin, useMe, useSigninOptions, type EnrolChallenge, type SigninStep } from '../api/session'
import { createCredential, getAssertion, webauthnAvailable } from '../api/webauthn'
import { Button, ButtonLink, GaiaMark, Notice } from '../ui'
import s from './Login.module.css'

// Sign-in (ADR-008): single sign-on first when it is on, a local account below it; then the second factor, or its
// enrolment at the first sign-in; the recovery codes once; a new password when one is needed. The full v2 screens are
// T27's (docs/design/v2/Login.dc.html); this one has every step.

export function Login() {
  const t = useT().login
  const me = useMe()
  const opts = useSigninOptions()
  const qc = useQueryClient()
  const nav = useNavigate()
  const [params] = useSearchParams()
  const next = safeNext(params.get('next'))
  const [step, setStep] = useState<SigninStep | null>(null)
  const [codes, setCodes] = useState<string[] | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [ended] = useState(() => { const r = endedReason; sessionEnded(null); return r })
  if (me.data && !codes) return <Navigate to={next} replace />

  async function run(f: () => Promise<SigninStep>) {
    setBusy(true)
    setErr(null)
    try {
      const st = await f()
      if (st.stage === 'done') {
        if (st.recovery_codes?.length) setCodes(st.recovery_codes)
        else finish()
        qc.setQueryData(ME_KEY, null)
        await qc.invalidateQueries({ queryKey: ME_KEY })
      }
      setStep(st)
    } catch (e) {
      setErr(message(t, e))
      if (e instanceof ApiError && (e.status === 401 || e.status === 429)) setStep(null) // start again with the password
    } finally {
      setBusy(false)
    }
  }
  const finish = () => nav(next, { replace: true })

  let body: ReactNode
  if (codes) {
    body = (
      <>
        <h1>{t.codesTitle}</h1>
        <p className={s.hint}>{t.codesHint}</p>
        <ol className={s.codes}>{codes.map((c) => <li key={c}><code>{c}</code></li>)}</ol>
        <Button onClick={finish}>{t.codesDone}</Button>
      </>
    )
  } else if (!step) {
    body = <PasswordForm t={t} busy={busy} sso={opts.data} onSubmit={(u, p) => run(() => signin.password(u, p))} />
  } else if (step.stage === 'second_factor') {
    body = <FactorForm t={t} step={step} busy={busy} run={run} />
  } else if (step.stage === 'enrol') {
    body = <EnrolForm t={t} step={step} busy={busy} run={run} setErr={setErr} />
  } else if (step.stage === 'change_password') {
    body = <NewPasswordForm t={t} step={step} busy={busy} run={run} />
  }
  const sso = params.get('sso_error')
  return (
    <main className={s.page}>
      <div className={s.form}>
        <div className={s.mark} aria-hidden="true"><GaiaMark size={30} /></div>
        {opts.data && <p className={s.inst}>{opts.data.instance}</p>}
        {ended && !step && <Notice tone="warning">{t.ended(ended)}</Notice>}
        {sso && !step && <Notice tone="critical">{t.ssoError(sso)}</Notice>}
        {body}
        {err && <Notice tone="critical">{err}</Notice>}
        <p className={s.foot}>{t.monitored}</p>
      </div>
    </main>
  )
}

type T = ReturnType<typeof useT>['login']
type Run = (f: () => Promise<SigninStep>) => Promise<void>

function message(t: T, e: unknown): string {
  if (!(e instanceof ApiError)) return t.failed(e instanceof Error ? e.message : String(e))
  if (e.status === 401) return e.code === 'unauthorized' && e.message.includes('expired') ? e.message : t.wrong
  if (e.status === 429) return t.paused(e.message)
  return e.message
}

function PasswordForm({ t, busy, sso, onSubmit }: { t: T; busy: boolean; sso?: { sso: boolean; sso_name?: string }; onSubmit: (u: string, p: string) => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [params] = useSearchParams()
  function submit(e: FormEvent) {
    e.preventDefault()
    onSubmit(username, password)
  }
  return (
    <>
      <h1 id="login-title">{t.title}</h1>
      {sso?.sso && (
        <>
          <ButtonLink href={`/api/v1/oidc/start?next=${encodeURIComponent(safeNext(params.get('next')))}`}>{t.sso(sso.sso_name ?? '')}</ButtonLink>
          <p className={s.or}>{t.orLocal}</p>
        </>
      )}
      <form onSubmit={submit} className={s.fields} aria-labelledby="login-title">
        <label className={s.field}>
          <span>{t.username}</span>
          <input name="username" autoComplete="username" required value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
        </label>
        <label className={s.field}>
          <span>{t.password}</span>
          <input name="password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        <Button type="submit" disabled={busy}>{busy ? t.working : t.submit}</Button>
      </form>
      {sso?.sso && <p className={s.hint}>{t.localHint}</p>}
    </>
  )
}

function FactorForm({ t, step, busy, run }: { t: T; step: SigninStep; busy: boolean; run: Run }) {
  const [code, setCode] = useState('')
  const [recovery, setRecovery] = useState(false)
  const f = step.factors ?? []
  const tok = step.signin_token ?? ''
  function submit(e: FormEvent) {
    e.preventDefault()
    run(() => signin.factor(tok, recovery ? 'recovery_code' : 'totp', { code }))
  }
  return (
    <>
      <h1>{t.factorTitle(step.username)}</h1>
      {f.includes('webauthn') && (
        <Button disabled={busy || !webauthnAvailable()} autoFocus
          onClick={() => run(async () => signin.factor(tok, 'webauthn', { webauthn: await getAssertion(step.webauthn) }))}>{t.useKey}</Button>
      )}
      {f.includes('webauthn') && !webauthnAvailable() && <Notice tone="warning">{t.noWebauthn}</Notice>}
      {(f.includes('totp') || recovery) && (
        <form onSubmit={submit} className={s.fields}>
          <label className={s.field}>
            <span>{recovery ? t.recoveryCode : t.appCode}</span>
            <input inputMode={recovery ? 'text' : 'numeric'} autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)}
              autoFocus={!f.includes('webauthn')} />
          </label>
          <Button type="submit" disabled={busy}>{t.verify}</Button>
        </form>
      )}
      {f.includes('recovery_code') && !recovery && <Button variant="quiet" onClick={() => { setRecovery(true); setCode('') }}>{t.useRecovery}</Button>}
      <p className={s.hint}>{t.lostFactor}</p>
    </>
  )
}

function EnrolForm({ t, step, busy, run, setErr }: { t: T; step: SigninStep; busy: boolean; run: Run; setErr: (s: string | null) => void }) {
  const [totp, setTotp] = useState<EnrolChallenge | null>(null)
  const [code, setCode] = useState('')
  const tok = step.signin_token ?? ''
  const kinds = step.enrol ?? []
  async function key() {
    await run(async () => {
      const ch = await signin.enrol(tok, 'webauthn')
      return signin.enrolFinish(tok, 'webauthn', { webauthn: await createCredential(ch.webauthn), name: t.keyName })
    })
  }
  async function app() {
    try {
      setTotp(await signin.enrol(tok, 'totp'))
    } catch (e) {
      setErr(message(t, e))
    }
  }
  if (totp) {
    return (
      <>
        <h1>{t.enrolAppTitle}</h1>
        <p className={s.hint}>{t.enrolAppHint}</p>
        <code className={s.secret}>{totp.totp_secret}</code>
        <code className={s.uri}>{totp.totp_uri}</code>
        <form className={s.fields} onSubmit={(e) => { e.preventDefault(); run(() => signin.enrolFinish(tok, 'totp', { code, name: t.appName })) }}>
          <label className={s.field}>
            <span>{t.appCode}</span>
            <input inputMode="numeric" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)} autoFocus />
          </label>
          <Button type="submit" disabled={busy}>{t.enrolFinish}</Button>
        </form>
      </>
    )
  }
  return (
    <>
      <h1>{t.enrolTitle}</h1>
      <p className={s.hint}>{kinds.includes('totp') ? t.enrolHint : t.enrolAdminHint}</p>
      <Button onClick={key} disabled={busy || !webauthnAvailable()} autoFocus>{t.enrolKey}</Button>
      {!webauthnAvailable() && <Notice tone="warning">{t.noWebauthn}</Notice>}
      {kinds.includes('totp') && <Button variant="quiet" onClick={app} disabled={busy}>{t.enrolApp}</Button>}
    </>
  )
}

function NewPasswordForm({ t, step, busy, run }: { t: T; step: SigninStep; busy: boolean; run: Run }) {
  const [p1, setP1] = useState('')
  const [p2, setP2] = useState('')
  const n = [...p1].length
  return (
    <>
      <h1>{t.newPasswordTitle}</h1>
      <p className={s.hint}>{t.newPasswordWhy(step.password_change ?? '')}</p>
      <form className={s.fields} onSubmit={(e) => { e.preventDefault(); run(() => signin.newPassword(step.signin_token ?? '', p1)) }}>
        <label className={s.field}>
          <span>{t.newPassword}</span>
          <input type="password" autoComplete="new-password" required minLength={15} maxLength={128} value={p1} onChange={(e) => setP1(e.target.value)} autoFocus />
        </label>
        <p className={s.rule}>{n >= 15 ? '✓' : '·'} {t.lengthRule(n)}</p>
        <label className={s.field}>
          <span>{t.again}</span>
          <input type="password" autoComplete="new-password" required value={p2} onChange={(e) => setP2(e.target.value)} />
        </label>
        <Button type="submit" disabled={busy || p1 !== p2 || n < 15}>{t.savePassword}</Button>
      </form>
      <p className={s.hint}>{t.otherSessions}</p>
    </>
  )
}

/** Only a path on this site: never an absolute URL from the query. */
function safeNext(v: string | null): string {
  return v && v.startsWith('/') && !v.startsWith('//') ? v : '/'
}
