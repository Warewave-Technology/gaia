import { Link, Navigate, useParams } from 'react-router'
import { useT } from '../../i18n'
import type { Ability } from '../access'
import { useCounts } from '../api/inventory'
import { can, useMe } from '../api/session'
import { Loading } from '../layout/States'
import { Audit } from './inventory/Audit'
import { Hosts } from './inventory/Hosts'
import { Policies } from './inventory/Policies'
import { Repositories } from './inventory/Repositories'
import { Security } from './inventory/Security'
import { Users } from './inventory/Users'
import s from './Inventory.module.css'

const TABS = ['hosts', 'policies', 'repositories', 'users', 'security', 'audit'] as const
type Tab = (typeof TABS)[number]

/** Who sees each table (access.json: /inventory, /inventory/users, /inventory/security, /inventory/audit). */
const NEEDS: Record<Tab, Ability> = { hosts: 'any', policies: 'any', repositories: 'any', users: 'export', security: 'admin', audit: 'export' }

function isTab(v: string): v is Tab {
  return (TABS as readonly string[]).includes(v)
}

/**
 * The Inventory (docs/design/v2/Inventory.dc.html, Security.dc.html): what Gaia backs up from, the policies it judges
 * by, where points are stored, who may sign in and how, and what everyone did. A side menu with counts; each place a
 * table with status where status applies. A table a role may not read is not in its menu, and its URL leads back to
 * the hosts; the actions a role may not take are not offered (the API refuses them again).
 */
export function Inventory() {
  const t = useT().inventory
  const me = useMe().data
  const { tab = 'hosts' } = useParams()
  const counts = useCounts(can(me, 'export'), can(me, 'export'))
  if (!me) return <Loading />
  if (!isTab(tab) || !can(me, NEEDS[tab])) return <Navigate to="/inventory" replace />
  const seen = TABS.filter((x) => can(me, NEEDS[x]))
  const count: Partial<Record<Tab, number | undefined>> = counts
  return (
    <main className={s.page}>
      <nav className={s.side} aria-label={t.menu}>
        {seen.map((x) => {
          const c = count[x]
          return (
            <Link key={x} to={x === 'hosts' ? '/inventory' : `/inventory/${x}`} aria-current={x === tab ? 'page' : undefined}>
              {t.tabs[x]}
              {c !== undefined && <span>{t.count(c)}</span>}
            </Link>
          )
        })}
      </nav>
      <div className={s.content}>
        {tab === 'hosts' && <Hosts me={me} />}
        {tab === 'policies' && <Policies me={me} />}
        {tab === 'repositories' && <Repositories me={me} />}
        {tab === 'users' && <Users me={me} />}
        {tab === 'security' && <Security me={me} />}
        {tab === 'audit' && <Audit />}
      </div>
    </main>
  )
}
