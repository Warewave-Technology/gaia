import { useState, type FormEvent } from 'react'
import { useT } from '../../../i18n'
import { serverTime, type Fmt, type ServerClock } from '../../../lib/time'
import { can, roles } from '../../access'
import { change, useSettings, useUsers, type User as Account, type UserPatch } from '../../api/inventory'
import type { User } from '../../api/session'
import { useSystem } from '../../api/views'
import { Failed, Loading } from '../../layout/States'
import { Button, PageHead, Pick, StatusChip } from '../../ui'
import s from '../Inventory.module.css'
import { Confirm, Field, useAction, when } from './common'

type T = ReturnType<typeof useT>['inventory']

const after = (iso: string, now: string) => !!iso && Date.parse(iso) > Date.parse(now)

/**
 * The users (admins and auditors read it; only admins change it). Every change is a step-up action (ADR-008 §4):
 * role, disable, factor reset, unlock, renewal and break-glass all go through withStepUp.
 */
export function Users({ me }: { me: User }) {
  const t = useT().inventory
  const u = t.users
  const admin = can(me, 'admin')
  const q = useUsers(true)
  const sys = useSystem().data
  const settings = useSettings(admin).data
  const act = useAction()
  const [adding, setAdding] = useState(false)
  if (q.isPending || !sys) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const clock = sys.clock
  const fmt = serverTime(clock)
  const patch = (x: Account, key: string, body: UserPatch) =>
    act.run(`${key}/${x.id}`, 'PATCH', `/api/v1/users/${x.id}`, (h) => change.patchUser(x.id, body, h))
  return (
    <>
      <PageHead question={u.question} answer={u.answer(q.data.length)} lede={<p>{u.hint}</p>} />
      <section className={s.sec}>
        {admin && (
          <div className={s.bar}>
            <Button className={s.end} variant={adding ? 'quiet' : 'primary'} onClick={() => setAdding(!adding)}>{adding ? t.cancel : u.add}</Button>
          </div>
        )}
        {adding && settings && (
          <AddUser min={settings.fixed.password_min_length} auditorDays={settings.fixed.auditor_max_days} busy={act.busy === 'add'}
            onAdd={async (body) => {
              const r = await act.run('add', 'POST', '/api/v1/users', (h) => change.addUser(body, h), (x) => u.added(x.username))
              if (r) setAdding(false)
            }} />
        )}
        <div className={s.scroll}>
          <table className={s.tbl}>
            <thead>
              <tr>
                <th scope="col">{u.cols.user}</th>
                <th scope="col">{u.cols.role}</th>
                <th scope="col">{u.cols.factors}</th>
                <th scope="col">{u.cols.last}</th>
                <th scope="col">{u.cols.state}</th>
                {admin && <th scope="col"><span className={s.vh}>{t.action}</span></th>}
              </tr>
            </thead>
            <tbody>
              {q.data.map((x) => (
                <UserRow key={x.id} x={x} me={me} admin={admin} fmt={fmt} clock={clock} t={t} busy={act.busy}
                  onRole={(role) => patch(x, 'role', { role })}
                  onDisable={(disabled) => patch(x, 'disable', { disabled })}
                  onRenew={() => patch(x, 'renew', { expires_at: '' })}
                  onBreakGlass={(break_glass) => patch(x, 'bg', { break_glass })}
                  onReset={() => act.run(`reset/${x.id}`, 'POST', `/api/v1/users/${x.id}/factors/reset`, (h) => change.resetFactors(x.id, h))}
                  onUnlock={() => act.run(`unlock/${x.id}`, 'POST', `/api/v1/users/${x.id}/unlock`, (h) => change.unlockUser(x.id, h))} />
              ))}
            </tbody>
          </table>
        </div>
        {act.outcome}
        <p className={s.foot}>{u.foot}</p>
      </section>
    </>
  )
}

function UserRow({ x, me, admin, fmt, clock, t, busy, onRole, onDisable, onRenew, onBreakGlass, onReset, onUnlock }: {
  x: Account
  me: User
  admin: boolean
  fmt: Fmt
  clock: ServerClock
  t: T
  busy: string | null
  onRole: (r: string) => void
  onDisable: (d: boolean) => void
  onRenew: () => void
  onBreakGlass: (b: boolean) => void
  onReset: () => void
  onUnlock: () => void
}) {
  const u = t.users
  const locked = after(x.locked_until, clock.now)
  const expired = x.role === 'auditor' && !!x.expires_at && !after(x.expires_at, clock.now)
  const sso = x.source === 'oidc'
  const self = x.id === me.id
  const st = locked ? 'WARNING' : expired ? 'WARNING' : null
  return (
    <tr className={x.disabled ? s.fg2 : ''}>
      <th scope="row">
        <span className={s.name}>{x.username}</span>
        <span className={s.of}>{u.source(x.source)}{x.break_glass && ` · ${u.breakGlass}`}</span>
      </th>
      <td>
        {admin && !sso && !self ? (
          <Pick label={u.role} value={x.role} onChange={onRole} options={roles.map((r) => [r, r] as const)} />
        ) : <span className={s.val}>{x.role}</span>}
        {sso && <span className={s.of}>{u.roleFromIdP}</span>}
        {x.role === 'auditor' && x.expires_at && <span className={s.of}>{u.expires(`${fmt.date(x.expires_at)}`)}</span>}
      </td>
      <td className={s.fg2}>
        {sso ? u.idpFactor : x.factors.length === 0 ? u.noFactor : x.factors.map(u.factor).join(', ')}
      </td>
      <td className={s.fg2}>{x.last_login_at ? when(fmt, clock, x.last_login_at, t) : t.never}</td>
      <td>
        <span className={s.state}>
          {st && <StatusChip status={st} word={locked ? u.word.locked : u.word.expired} size="sm" />}
          <span>
            {locked ? u.lockedUntil(when(fmt, clock, x.locked_until, t)) : x.disabled ? u.disabled : u.active}
            {x.password_change && ` · ${u.mustChange(x.password_change)}`}
          </span>
        </span>
      </td>
      {admin && (
        <td>
          <span className={s.acts}>
            {locked && <Button variant="quiet" disabled={busy === `unlock/${x.id}`} onClick={onUnlock}>{u.unlock}</Button>}
            {x.role === 'auditor' && <Button variant="quiet" disabled={busy === `renew/${x.id}`} onClick={onRenew}>{u.renew}</Button>}
            {!sso && x.role === 'admin' && (
              <Button variant="quiet" disabled={busy === `bg/${x.id}`} onClick={() => onBreakGlass(!x.break_glass)}>
                {x.break_glass ? u.unmarkBreakGlass : u.markBreakGlass}
              </Button>
            )}
            {!sso && !self && x.factors.length > 0 && (
              <Confirm label={u.resetFactors} question={u.resetConfirm(x.username)} busy={busy === `reset/${x.id}`} onYes={onReset} />
            )}
            {!self && <Button variant="quiet" disabled={busy === `disable/${x.id}`} onClick={() => onDisable(!x.disabled)}>{x.disabled ? u.enable : u.disable}</Button>}
          </span>
        </td>
      )}
    </tr>
  )
}

function AddUser({ min, auditorDays, busy, onAdd }: {
  min: number
  auditorDays: number
  busy: boolean
  onAdd: (b: { username: string; role: string; password: string; break_glass?: boolean }) => void
}) {
  const t = useT().inventory
  const f = t.users.form
  const [username, setUsername] = useState('')
  const [role, setRole] = useState<string>('operator')
  const [password, setPassword] = useState('')
  const [bg, setBg] = useState(false)
  function submit(e: FormEvent) {
    e.preventDefault()
    onAdd({ username: username.trim(), role, password, break_glass: role === 'admin' ? bg : undefined })
  }
  return (
    <form className={s.form} onSubmit={submit}>
      <h3>{f.title}</h3>
      <div className={s.fields}>
        <Field label={f.username}><input required value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" autoFocus /></Field>
        <Field label={f.role}>
          <select value={role} onChange={(e) => setRole(e.target.value)}>
            {roles.map((r) => <option key={r} value={r}>{r}</option>)}
          </select>
        </Field>
        <Field label={f.password} hint={f.passwordHint(min)} wide>
          <input required type="password" minLength={min} value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
        </Field>
      </div>
      {role === 'admin' && (
        <label className={s.check}><input type="checkbox" checked={bg} onChange={(e) => setBg(e.target.checked)} />{f.breakGlass}</label>
      )}
      {role === 'auditor' && <p className={s.hint}>{f.auditor(auditorDays)}</p>}
      <div className={s.buttons}><Button type="submit" disabled={busy}>{busy ? t.working : f.save}</Button></div>
    </form>
  )
}
