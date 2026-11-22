import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Navigate, useNavigate, useSearchParams } from 'react-router'
import { useT } from '../../i18n'
import { ApiError } from '../api/errors'
import { ME_KEY, endedReason, sessionEnded, signin, useMe, useSigninOptions, type EnrolChallenge, type SigninStep } from '../api/session'
import { createCredential, getAssertion, webauthnAvailable } from '../api/webauthn'
import { Button, ButtonLink, GaiaMark, Notice } from '../ui'
import s from './Login.module.css'

// Sign-in (ADR-008; v2 design docs/design/v2/Login.dc.html): single sign-on first when it is on, a local account below
// it with the reason local accounts exist; then the second factor, or its enrolment at the first sign-in; the recovery
// codes once; a new password when one is needed. A session that ended says why on its own card; too many failed
// attempts show the wait the server gave (Retry-After), counting down.

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
  const [wait, setWait] = useState<Wait | null>(null)
  const [busy, setBusy] = useState(false)
  const [ended, setEnded] = useState(() => { const r = endedReason; sessionEnded(null); return r })
  if (me.data && !codes) return <Navigate to={next} replace />

  async function run(f: () => Promise<SigninStep>) {
    setBusy(true)
    setErr(null)
    setWait(null)
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
      const w = waitOf(e)
      if (w) setWait(w)
      else setErr(message(t, e))
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
        <p className={s.sub}>{t.codesHint}</p>
        <ol className={s.codes}>{codes.map((c) => <li key={c}><code>{c}</code></li>)}</ol>
        <Button className={`${s.btn} ${s.pri}`} onClick={finish} autoFocus>{t.codesDone}</Button>
      </>
    )
  } else if (wait?.paused) {
    body = <Locked t={t} wait={wait} done={() => setWait(null)} />
  } else if (ended && !step) {
    body = (
      <>
        <h1>{t.endedTitle}</h1>
        <p className={s.note} role="status">{t.ended(ended)}</p>
        <Button className={`${s.btn} ${s.pri}`} onClick={() => setEnded(null)} autoFocus>{t.signInAgain}</Button>
      </>
    )
  } else if (!step) {
    const sso = params.get('sso_error')
    body = (
      <>
        {sso && <Notice tone="critical">{t.ssoError(sso)}</Notice>}
        <PasswordForm t={t} busy={busy} wait={wait} sso={opts.data} onSubmit={(u, p) => run(() => signin.password(u, p))} />
      </>
    )
  } else if (step.stage === 'second_factor') {
    body = <FactorForm t={t} step={step} busy={busy} run={run} />
  } else if (step.stage === 'enrol') {
    body = <EnrolForm t={t} step={step} busy={busy} run={run} setErr={setErr} />
  } else if (step.stage === 'change_password') {
    body = <NewPasswordForm t={t} step={step} instance={opts.data?.instance ?? ''} busy={busy} run={run} />
  }
  return (
    <SigninFrame instance={opts.data?.instance}>
      {body}
      {err && <Notice tone="critical">{err}</Notice>}
    </SigninFrame>
  )
}

/** The sign-in pages' frame: a centred card with the brand and the instance's name, the footer under it. */
export function SigninFrame({ instance, children }: { instance?: string; children: ReactNode }) {
  const t = useT()
  return (
    <div className={s.page}>
      <main className={s.stage}>
        <div className={s.card}>
          <div className={s.brand}>
            <GaiaMark size={24} />
            <span>{t.app.name}</span>
            {instance && <span className={s.inst}>{instance}</span>}
          </div>
          {children}
        </div>
      </main>
      <footer className={s.foot}>
        <span>{t.login.license}</span>
        <span>{t.login.monitored}</span>
      </footer>
    </div>
  )
}

type T = ReturnType<typeof useT>['login']
type Run = (f: () => Promise<SigninStep>) => Promise<void>

/** A refused attempt (429): until when, the server's reason, and whether the account is paused (ADR-008 §7: after 10
 * failures in 15 minutes, from every address) or only this attempt must wait (the delay from the 3rd failure). */
type Wait = { until: number; why: string; paused: boolean }

function waitOf(e: unknown): Wait | null {
  if (!(e instanceof ApiError) || e.status !== 429 || !e.retryAfter || !Number.isFinite(e.retryAfter)) return null
  const why = e.message.replace(/;\s*try again after .*$/, '') // the countdown says when
  return { until: Date.now() + e.retryAfter * 1000, why: why.charAt(0).toUpperCase() + why.slice(1), paused: e.reason === 'account_paused' }
}

/** Seconds left until a moment, ticking. */
function useLeft(until: number | null): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (until === null) return
    setNow(Date.now())
    const id = setInterval(() => {
      const n = Date.now()
      setNow(n)
      if (n >= until) clearInterval(id)
    }, 500)
    return () => clearInterval(id)
  }, [until])
  return until === null ? 0 : Math.max(0, Math.ceil((until - now) / 1000))
}

function message(t: T, e: unknown): string {
  if (!(e instanceof ApiError)) return t.failed(e instanceof Error ? e.message : String(e))
  if (e.status === 401) return e.code === 'unauthorized' && e.message.includes('expired') ? e.message : t.wrong
  if (e.status === 429) return t.paused(e.message)
  return e.message
}

function Locked({ t, wait, done }: { t: T; wait: Wait; done: () => void }) {
  const left = useLeft(wait.until)
  return (
    <>
      <h1>{t.lockedTitle}</h1>
      {left > 0 ? <Notice tone="critical"><span className="num">{t.lockedNotice(wait.why, t.left(left))}</span></Notice>
        : <p className={s.note} role="status">{t.lockedOver(wait.why)}</p>}
      <p className={s.sub}>{t.lockedHint}</p>
      {left > 0 ? <Button variant="quiet" className={s.btn} onClick={done}>{t.otherAccount}</Button>
        : <Button className={`${s.btn} ${s.pri}`} onClick={done} autoFocus>{t.signInAgain}</Button>}
    </>
  )
}

function PasswordForm({ t, busy, wait, sso, onSubmit }: {
  t: T; busy: boolean; wait: Wait | null; sso?: { sso: boolean; sso_name?: string }; onSubmit: (u: string, p: string) => void
}) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [params] = useSearchParams()
  const left = useLeft(wait?.until ?? null)
  function submit(e: FormEvent) {
    e.preventDefault()
    onSubmit(username, password)
  }
  return (
    <>
      <h1 id="login-title">{t.title}</h1>
      {sso?.sso && (
        <>
          <ButtonLink className={`${s.btn} ${s.pri}`} href={`/api/v1/oidc/start?next=${encodeURIComponent(safeNext(params.get('next')))}`}>
            {t.sso(sso.sso_name ?? '')}
          </ButtonLink>
          <p className={s.or}>{t.orLocal}</p>
        </>
      )}
      {wait && left > 0 && <Notice tone="warning"><span className="num">{t.waitNotice(wait.why, t.left(left))}</span></Notice>}
      <form onSubmit={submit} className={s.fields} aria-labelledby="login-title">
        <label className={s.field}>
          <span>{t.username}</span>
          <input name="username" autoComplete="username" required value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
        </label>
        <label className={s.field}>
          <span>{t.password}</span>
          <input name="password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        <Button type="submit" className={sso?.sso ? s.btn : `${s.btn} ${s.pri}`} disabled={busy || left > 0}>{busy ? t.working : t.submit}</Button>
      </form>
      <p className={s.sub}>{sso?.sso ? t.localHint : t.localOnlyHint}</p>
    </>
  )
}

function FactorForm({ t, step, busy, run }: { t: T; step: SigninStep; busy: boolean; run: Run }) {
  const [code, setCode] = useState('')
  const [recovery, setRecovery] = useState(false)
  const f = step.factors ?? []
  const tok = step.signin_token ?? ''
  const key = f.includes('webauthn')
  const app = f.includes('totp')
  function submit(e: FormEvent) {
    e.preventDefault()
    run(() => signin.factor(tok, recovery ? 'recovery_code' : 'totp', { code }))
  }
  return (
    <>
      <h1>{t.factorTitle(step.username)}</h1>
      {key && (
        <Button className={`${s.btn} ${s.pri}`} disabled={busy || !webauthnAvailable()} autoFocus
          onClick={() => run(async () => signin.factor(tok, 'webauthn', { webauthn: await getAssertion(step.webauthn) }))}>{t.useKey}</Button>
      )}
      {key && !webauthnAvailable() && <Notice tone="warning">{t.noWebauthn}</Notice>}
      {(app || recovery) && (
        <>
          {key && <p className={s.or}>{recovery ? t.orRecovery : t.orCode}</p>}
          <form onSubmit={submit} className={s.fields}>
            <label className={s.field}>
              <span>{recovery ? t.recoveryCode : t.appCode}</span>
              <input className={recovery ? s.recovery : s.otp} inputMode={recovery ? 'text' : 'numeric'} autoComplete="one-time-code" required
                spellCheck={false} value={code} onChange={(e) => setCode(e.target.value)} autoFocus={!key || recovery} />
            </label>
            <Button type="submit" className={key ? s.btn : `${s.btn} ${s.pri}`} disabled={busy}>{t.verify}</Button>
          </form>
        </>
      )}
      {f.includes('recovery_code') && !recovery && (
        <Button variant="quiet" className={s.btn} onClick={() => { setRecovery(true); setCode('') }}>{t.useRecovery}</Button>
      )}
      {recovery && app && <Button variant="quiet" className={s.btn} onClick={() => { setRecovery(false); setCode('') }}>{t.useApp}</Button>}
      <p className={s.sub}>{t.lostFactor}</p>
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
        <p className={s.sub}>{t.enrolAppHint}</p>
        <dl className={s.secretBox}>
          <dt>{t.secret}</dt><dd><code className={s.secret}>{totp.totp_secret}</code></dd>
          <dt>{t.link}</dt><dd><code className={s.uri}>{totp.totp_uri}</code></dd>
        </dl>
        <form className={s.fields} onSubmit={(e) => { e.preventDefault(); run(() => signin.enrolFinish(tok, 'totp', { code, name: t.appName })) }}>
          <label className={s.field}>
            <span>{t.appCode}</span>
            <input className={s.otp} inputMode="numeric" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)} autoFocus />
          </label>
          <Button type="submit" className={`${s.btn} ${s.pri}`} disabled={busy}>{t.enrolFinish}</Button>
        </form>
      </>
    )
  }
  return (
    <>
      <h1>{t.enrolTitle}</h1>
      <p className={s.sub}>{kinds.includes('totp') ? t.enrolHint : t.enrolAdminHint}</p>
      <Button className={`${s.btn} ${s.pri}`} onClick={key} disabled={busy || !webauthnAvailable()} autoFocus>{t.enrolKey}</Button>
      {!webauthnAvailable() && <Notice tone="warning">{t.noWebauthn}</Notice>}
      {kinds.includes('totp') && (
        <>
          <p className={s.or}>{t.orApp}</p>
          <Button className={s.btn} onClick={app} disabled={busy}>{t.enrolApp}</Button>
        </>
      )}
    </>
  )
}

/** A password rule: met or broken as typed (the length; the names, the same test as the server's), or one only the
 * server can check (the blocklist, the last 5): shown as a rule, its refusal message comes from the server. */
type RuleState = 'met' | 'broken' | 'open' | 'server'
const RULE_GLYPH: Record<RuleState, string> = { met: '✓', broken: '✗', open: '·', server: '○' }

function Rule({ t, state, children }: { t: T; state: RuleState; children: ReactNode }) {
  const word = { met: t.ruleMet, broken: t.ruleBroken, open: t.ruleBroken, server: t.ruleLater }[state]
  return (
    <li className={`${s.rule} ${s[state]}`}>
      <b className={s.g} aria-hidden="true">{RULE_GLYPH[state]}</b>
      <span><span className={s.sr}>{word} </span>{children}</span>
    </li>
  )
}

function NewPasswordForm({ t, step, instance, busy, run }: { t: T; step: SigninStep; instance: string; busy: boolean; run: Run }) {
  const [p1, setP1] = useState('')
  const [p2, setP2] = useState('')
  const n = [...p1].length
  const lp = p1.toLowerCase()
  const has = (v: string) => { v = v.toLowerCase(); return v.length >= 3 && lp.includes(v) } // internal/auth CheckPassword
  const names: RuleState = p1 === '' ? 'open'
    : !has(step.username) && !has('gaia') && !has(instance) && !has(instance.split('.')[0]) ? 'met' : 'broken'
  const length: RuleState = n > 128 ? 'broken' : n >= 15 ? 'met' : 'open' // short while typing: not yet, not wrong
  return (
    <>
      <h1>{t.newPasswordTitle}</h1>
      <Notice tone="warning">{t.newPasswordWhy(step.password_change ?? '')}</Notice>
      <form className={s.fields} onSubmit={(e) => { e.preventDefault(); run(() => signin.newPassword(step.signin_token ?? '', p1)) }}>
        <label className={s.field}>
          <span>{t.newPassword}</span>
          <input type="password" autoComplete="new-password" required minLength={15} maxLength={128} value={p1} onChange={(e) => setP1(e.target.value)}
            autoFocus aria-describedby="pw-rules" />
        </label>
        <ul className={s.rules} id="pw-rules" aria-label={t.rulesLabel}>
          <Rule t={t} state={length}><span className="num">{t.ruleLength(n)}</span></Rule>
          <Rule t={t} state={names}>{t.ruleNames}</Rule>
          <Rule t={t} state="server">{t.ruleBlocklist} <span className={s.by}>({t.ruleByServer})</span></Rule>
          <Rule t={t} state="server">{t.ruleHistory} <span className={s.by}>({t.ruleByServer})</span></Rule>
        </ul>
        <label className={s.field}>
          <span>{t.again}</span>
          <input type="password" autoComplete="new-password" required value={p2} onChange={(e) => setP2(e.target.value)} />
        </label>
        {p2 !== '' && p1 !== p2 && <p className={s.mismatch} role="status">{t.mismatch}</p>}
        <Button type="submit" className={`${s.btn} ${s.pri}`} disabled={busy || p1 !== p2 || n < 15}>{t.savePassword}</Button>
      </form>
      <p className={s.sub}>{t.otherSessions}</p>
    </>
  )
}

/** Only a path on this site: never an absolute URL from the query. */
function safeNext(v: string | null): string {
  return v && v.startsWith('/') && !v.startsWith('//') ? v : '/'
}
