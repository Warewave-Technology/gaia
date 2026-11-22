import { useState, type FormEvent } from 'react'
import { useT } from '../../../i18n'
import { serverTime, type Fmt, type ServerClock } from '../../../lib/time'
import { can, roles } from '../../access'
import { change, useSettings, useUsers, type User as Account, type UserPatch } from '../../api/inventory'
import type { User } from '../../api/session'
import { useSystem } from '../../api/views'
import { Failed, Loading } from '../../layout/States'
import { Button, by, DataTable, PageHead, Pick, StatusChip, Toolbar, type Column } from '../../ui'
import s from '../Inventory.module.css'
import { Confirm, Field, useAction } from './common'

type T = ReturnType<typeof useT>['inventory']

const after = (iso: string, now: string) => !!iso && Date.parse(iso) > Date.parse(now)

/**
 * The users (admins and auditors read it; only admins change it), one line each in a data-table. A row opens to the
 * user's facts and every change; the row itself offers only what is pressing (unlock a paused user, renew an
 * expired auditor). Every change is a step-up action (ADR-008 §4) through withStepUp.
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
  const locked = (x: Account) => after(x.locked_until, clock.now)
  const expired = (x: Account) => x.role === 'auditor' && !!x.expires_at && !after(x.expires_at, clock.now)

  const cols: Column<Account>[] = [
    {
      key: 'user', header: u.cols.user, width: '220px', compare: by((x) => x.username),
      cell: (x) => (
        <>
          <span className={s.name}>{x.username}</span>
          <span className={s.cellOf}>{u.source(x.source)}{x.break_glass && ` · ${u.breakGlass}`}{x.id === me.id && ` · ${t.users.you}`}</span>
        </>
      ),
    },
    {
      key: 'role', header: u.cols.role, width: '175px', compare: by((x) => x.role),
      cell: (x) => (admin && x.source !== 'oidc' && x.id !== me.id
        ? <Pick label={u.role} value={x.role} onChange={(role) => patch(x, 'role', { role })} options={roles.map((r) => [r, r] as const)} />
        : <span className={s.val}>{x.role}</span>),
    },
    {
      key: 'factors', header: u.cols.factors, title: (x) => factors(x, u),
      cell: (x) => factors(x, u),
    },
    { key: 'last', header: u.cols.last, width: '150px', compare: by((x) => x.last_login_at || null), cell: (x) => (x.last_login_at ? fmt.at(x.last_login_at) : t.never) },
    {
      key: 'state', header: u.cols.state, title: (x) => stateText(x, locked(x), fmt, u),
      cell: (x) => (
        <>
          {locked(x) && <StatusChip status="WARNING" word={u.word.locked} size="sm" />}
          {!locked(x) && expired(x) && <StatusChip status="WARNING" word={u.word.expired} size="sm" />}
          <span>{stateText(x, locked(x), fmt, u)}</span>
        </>
      ),
    },
  ]
  if (admin) {
    cols.push({
      key: 'action', header: u.cols.action, width: '120px',
      cell: (x) => (locked(x)
        ? <Button variant="quiet" className={s.cellBtn} disabled={act.busy === `unlock/${x.id}`}
          onClick={() => act.run(`unlock/${x.id}`, 'POST', `/api/v1/users/${x.id}/unlock`, (h) => change.unlockUser(x.id, h))}>{u.unlock}</Button>
        : expired(x)
          ? <Button variant="quiet" className={s.cellBtn} disabled={act.busy === `renew/${x.id}`} onClick={() => patch(x, 'renew', { expires_at: '' })}>{u.renew}</Button>
          : null),
    })
  }
  return (
    <>
      <PageHead question={u.question} answer={u.answer(q.data.length)} lede={<p>{u.hint}</p>} />
      <section className={s.sec}>
        {admin && (
          <Toolbar end={<Button variant={adding ? 'quiet' : 'primary'} onClick={() => setAdding(!adding)}>{adding ? t.cancel : u.add}</Button>}>{null}</Toolbar>
        )}
        {adding && settings && (
          <AddUser min={settings.fixed.password_min_length} auditorDays={settings.fixed.auditor_max_days} busy={act.busy === 'add'}
            onAdd={async (body) => {
              const r = await act.run('add', 'POST', '/api/v1/users', (h) => change.addUser(body, h), (x) => u.added(x.username))
              if (r) setAdding(false)
            }} />
        )}
        <DataTable<Account>
          caption={u.caption}
          columns={cols}
          rows={q.data}
          rowKey={(x) => String(x.id)}
          minWidth={980}
          detail={(x) => (
            <UserDetail x={x} me={me} admin={admin} fmt={fmt} clock={clock} t={t} busy={act.busy} locked={locked(x)}
              onDisable={(disabled) => patch(x, 'disable', { disabled })}
              onRenew={() => patch(x, 'renew', { expires_at: '' })}
              onBreakGlass={(break_glass) => patch(x, 'bg', { break_glass })}
              onReset={() => act.run(`reset/${x.id}`, 'POST', `/api/v1/users/${x.id}/factors/reset`, (h) => change.resetFactors(x.id, h))}
              onUnlock={() => act.run(`unlock/${x.id}`, 'POST', `/api/v1/users/${x.id}/unlock`, (h) => change.unlockUser(x.id, h))} />
          )}
        />
        {act.outcome}
        <p className={s.foot}>{u.foot}</p>
      </section>
    </>
  )
}

function factors(x: Account, u: T['users']) {
  return x.source === 'oidc' ? u.idpFactor : x.factors.length === 0 ? u.noFactor : x.factors.map(u.factor).join(', ')
}

function stateText(x: Account, locked: boolean, fmt: Fmt, u: T['users']) {
  const base = locked ? u.lockedUntil(fmt.at(x.locked_until)) : x.disabled ? u.disabled : u.active
  return x.password_change ? `${base} · ${u.mustChange(x.password_change)}` : base
}

function UserDetail({ x, me, admin, fmt, clock, t, busy, locked, onDisable, onRenew, onBreakGlass, onReset, onUnlock }: {
  x: Account
  me: User
  admin: boolean
  fmt: Fmt
  clock: ServerClock
  t: T
  busy: string | null
  locked: boolean
  onDisable: (d: boolean) => void
  onRenew: () => void
  onBreakGlass: (b: boolean) => void
  onReset: () => void
  onUnlock: () => void
}) {
  const u = t.users
  const sso = x.source === 'oidc'
  const self = x.id === me.id
  return (
    <div className={s.facts}>
      <p>{sso ? u.factsSso(fmt.at(x.created_at)) : u.facts(fmt.at(x.created_at), fmt.at(x.password_changed_at))}</p>
      {sso && <p>{u.roleFromIdP}</p>}
      {x.role === 'auditor' && x.expires_at && <p>{after(x.expires_at, clock.now) ? u.expires(fmt.at(x.expires_at)) : u.expired(fmt.at(x.expires_at))}</p>}
      {admin && (
        <span className={s.inline}>
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
      )}
    </div>
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
