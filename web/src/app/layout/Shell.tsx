import { NavLink, Outlet, useNavigate } from 'react-router'
import { useT } from '../../i18n'
import { can, useLogout, useMe } from '../api/session'
import { useSystem } from '../api/views'
import { serverTime } from '../../lib/time'
import s from './Shell.module.css'

/** The bar every screen shares: the installation, the main places, the server's clock, the user. */
export function Shell() {
  const t = useT()
  const me = useMe().data
  const sys = useSystem().data
  const logout = useLogout()
  const nav = useNavigate()
  const fmt = sys ? serverTime(sys.clock) : null
  return (
    <>
      <header className={s.bar}>
        <NavLink className={s.mark} to="/" aria-label={t.nav.overview}>
          <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><circle cx="10" cy="10" r="7.25" /><path d="M10 2.75v14.5M2.75 10h14.5" /></svg>
          {t.app.name}
        </NavLink>
        {sys && <span className={s.inst}>{sys.hostname}</span>}
        <nav className={s.nav} aria-label={t.nav.main}>
          <NavLink to="/" end>{t.nav.overview}</NavLink>
          {can(me, 'export') && <NavLink to="/evidence">{t.nav.evidence}</NavLink>}
        </nav>
        {fmt && sys && (
          <span className={s.clock}>
            {fmt.hhmm(sys.clock.now)}
            <span>{t.app.serverTime(fmt.zone)}</span>
          </span>
        )}
        {me && (
          <span className={s.user}>
            {me.username} <span className={s.role}>{me.role}</span>
            <button className={s.out} onClick={() => logout.mutate(undefined, { onSettled: () => nav('/login') })}>{t.nav.logout}</button>
          </span>
        )}
      </header>
      <Outlet />
    </>
  )
}
