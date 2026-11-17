import { useState, type FormEvent } from 'react'
import { Navigate, useNavigate, useSearchParams } from 'react-router'
import { useT } from '../../i18n'
import { ApiError } from '../api/errors'
import { useLogin, useMe } from '../api/session'
import { Button, GaiaMark, Notice } from '../ui'
import s from './Login.module.css'

export function Login() {
  const t = useT().login
  const me = useMe()
  const login = useLogin()
  const nav = useNavigate()
  const [params] = useSearchParams()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const next = safeNext(params.get('next'))
  if (me.data) return <Navigate to={next} replace />

  function submit(e: FormEvent) {
    e.preventDefault()
    login.mutate({ username, password }, { onSuccess: () => nav(next, { replace: true }) })
  }
  let err: string | null = null
  if (login.error instanceof ApiError) {
    err = login.error.status === 401 ? t.wrong : login.error.status === 429 ? login.error.message : t.failed(login.error.message)
  }
  return (
    <main className={s.page}>
      <form className={s.form} onSubmit={submit} aria-labelledby="login-title">
        <div className={s.mark} aria-hidden="true">
          <GaiaMark size={30} />
        </div>
        <h1 id="login-title">{t.title}</h1>
        <label className={s.field}>
          <span>{t.username}</span>
          <input name="username" autoComplete="username" required value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
        </label>
        <label className={s.field}>
          <span>{t.password}</span>
          <input name="password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        {err && <Notice tone="critical">{err}</Notice>}
        <Button type="submit" disabled={login.isPending}>{login.isPending ? t.working : t.submit}</Button>
      </form>
    </main>
  )
}

/** Only a path on this site: never an absolute URL from the query. */
function safeNext(v: string | null): string {
  return v && v.startsWith('/') && !v.startsWith('//') ? v : '/'
}
