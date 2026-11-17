import type { ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router'
import { useMe } from '../api/session'
import { Loading, Failed } from './States'

/** Sends a visitor without a session to the login page, back here afterwards. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const me = useMe()
  const loc = useLocation()
  if (me.isPending) return <Loading />
  if (me.isError) return <Failed error={me.error} onRetry={() => me.refetch()} />
  if (!me.data) return <Navigate to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />
  return <>{children}</>
}
