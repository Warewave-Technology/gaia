import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect } from 'react'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { unwrap } from './errors'
import { usePages } from './pages'
import { LIST_REFRESH, usePaged } from './paged'

// The Inventory's tables (T27; docs/design/v2/Inventory.dc.html, Security.dc.html): hosts, policies, repositories,
// users, the sign-in settings with the signed-in sessions, and the audit log. The lists the API pages are paged here;
// the changes are plain calls that take the step-up headers (layout/StepUp.tsx withStepUp).

export type HostRow = components['schemas']['HostRow']
export type HostInput = components['schemas']['HostInput']
export type Scanned = components['schemas']['Scanned']
export type HostCheck = components['schemas']['HostCheck']
export type Policy = components['schemas']['Policy']
export type PolicyInput = components['schemas']['PolicyInput']
export type PolicyPatch = components['schemas']['PolicyPatch']
export type PolicyChange = components['schemas']['PolicyChange']
export type CapacityView = components['schemas']['CapacityView']
export type CoverageGroup = components['schemas']['CoverageGroup']
export type RepositoryRow = components['schemas']['RepositoryRow']
export type Verification = components['schemas']['Verification']
export type User = components['schemas']['User']
export type NewUser = components['schemas']['NewUser']
export type UserPatch = components['schemas']['UserPatch']
export type AuditEntry = components['schemas']['AuditEntry']
export type SettingsView = components['schemas']['SettingsView']
export type SettingsUpdate = components['schemas']['SettingsUpdate']
export type Session = components['schemas']['Session']

/**
 * ADR-007 C12, from a parallel server change: how much of the time a VM on the policy can be OK at best (0..1) and the
 * test interval that would make it OK all the time (null: none needed). Optional: a server without the change does
 * not send them, and the screens then say nothing about it.
 */
export type PolicyOK = { max_ok_fraction?: number | null; suggested_test_interval_s?: number | null }
export type PolicyView = Policy & PolicyOK
/** The what-if answer with C12's numbers per changed policy, when the server sends them. */
export type WhatIfView = CapacityView & { policies?: (PolicyOK & { policy_id: number })[] | null }
type Headers = Record<string, string>

const KEY = 'inventory'

/**
 * Every host, for the Hosts table: its headline counts each state over all hosts (docs/design/v3 rule 7), and the
 * hosts whose key is not trusted are one group. An installation has tens to hundreds of hosts, so they are read in
 * pages of 200 until the last (one request at 100 hosts); the table still draws one page of rows at a time.
 */
export function useAllHosts() {
  const q = usePaged(
    [KEY, 'hosts', 'all'],
    (after) => unwrap(api.GET('/api/v1/views/hosts', { params: { query: { after: after ? Number(after) : undefined, limit: 200 } } })),
    (p) => p.rows,
  )
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = q
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) fetchNextPage()
  }, [hasNextPage, isFetchingNextPage, fetchNextPage])
  return { ...q, complete: q.isSuccess && !hasNextPage }
}

/** The repositories, a page at a time (the view is offset-paged: the cursor is the offset). */
export function useRepositoryPages() {
  return usePages([KEY, 'repositories', 'pages'], async (cursor, limit) => {
    const v = await unwrap(api.GET('/api/v1/views/repositories', { params: { query: { after: cursor ? Number(cursor) : undefined, limit } } }))
    return { rows: v.rows, total: v.total, next: v.next }
  })
}

/** The audit log, newest first, a page at a time: each page continues before the oldest entry of the last. */
export function useAuditPages() {
  return usePages([KEY, 'audit', 'pages'], async (cursor, limit) => {
    const v = await unwrap(api.GET('/api/v1/views/audit', { params: { query: { before: cursor ? Number(cursor) : undefined, limit } } }))
    return { rows: v.rows, total: v.total, next: v.next }
  })
}

export function useRepositoryRows(limit = 100) {
  return usePaged(
    [KEY, 'repositories', limit],
    (after) => unwrap(api.GET('/api/v1/views/repositories', { params: { query: { after: after ? Number(after) : undefined, limit } } })),
    (p) => p.rows,
  )
}

/** Every policy (the API answers them in one list: an installation has tens, not thousands). */
export function usePolicies() {
  return useQuery({ queryKey: ['policies'], queryFn: () => unwrap(api.GET('/api/v1/policies')) as Promise<PolicyView[]>, refetchInterval: LIST_REFRESH })
}

/** Each policy's VMs by status (the coverage view by policy, healthy ones included), in pages of 200. */
export function usePolicyCoverage() {
  return usePaged(
    [KEY, 'coverage', 'policy'],
    (after) => unwrap(api.GET('/api/v1/views/coverage', { params: { query: { by: 'policy', all: true, after: after ? Number(after) : undefined, limit: 200 } } })),
    (p) => p.groups,
  )
}

/**
 * The proof capacity with a policy's test interval or proof age changed, and without (both from the same model, so
 * "now" and "after" differ only by the change). Nothing is saved (ADR-007 C4).
 */
export function useWhatIf(change: PolicyChange | null) {
  const base = useQuery({
    queryKey: [KEY, 'what-if', null],
    queryFn: () => unwrap(api.POST('/api/v1/capacity/what-if', { body: {} })) as Promise<WhatIfView>,
    staleTime: 30_000,
  })
  const after = useQuery({
    queryKey: [KEY, 'what-if', change],
    queryFn: () => unwrap(api.POST('/api/v1/capacity/what-if', { body: { policies: change ? [change] : [] } })) as Promise<WhatIfView>,
    enabled: change !== null,
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  })
  return { base, after }
}

export function useUsers(enabled: boolean) {
  return useQuery({ queryKey: [KEY, 'users'], queryFn: () => unwrap(api.GET('/api/v1/users')), enabled, refetchInterval: LIST_REFRESH })
}

/** The side menu's counts: one row of each paged list is enough for its total. Lists a role may not read are not asked. */
export function useCounts(users: boolean, audit: boolean) {
  const one = { params: { query: { limit: 1 } } }
  const opt = { staleTime: 30_000, refetchInterval: 60_000 }
  const hosts = useQuery({ queryKey: [KEY, 'count', 'hosts'], queryFn: () => unwrap(api.GET('/api/v1/views/hosts', one)), ...opt })
  const repos = useQuery({ queryKey: [KEY, 'count', 'repositories'], queryFn: () => unwrap(api.GET('/api/v1/views/repositories', one)), ...opt })
  const log = useQuery({ queryKey: [KEY, 'count', 'audit'], queryFn: () => unwrap(api.GET('/api/v1/views/audit', one)), enabled: audit, ...opt })
  const pols = usePolicies()
  const us = useUsers(users)
  return {
    hosts: hosts.data?.total,
    policies: pols.data?.length,
    repositories: repos.data?.total,
    users: users ? us.data?.length : undefined,
    audit: audit ? log.data?.total : undefined,
  }
}

export function useSettings(enabled: boolean) {
  return useQuery({ queryKey: [KEY, 'settings'], queryFn: () => unwrap(api.GET('/api/v1/settings/signin')), enabled, staleTime: 30_000 })
}

export function useSessions(enabled: boolean) {
  return useQuery({ queryKey: [KEY, 'sessions'], queryFn: () => unwrap(api.GET('/api/v1/sessions')), enabled, refetchInterval: LIST_REFRESH })
}

/** After a change: the Inventory's tables, and the names the overview's filters offer, are read again. */
export function useRefresh() {
  const qc = useQueryClient()
  return useCallback(() => Promise.all([
    qc.invalidateQueries({ queryKey: [KEY] }),
    qc.invalidateQueries({ queryKey: ['hosts'] }),
    qc.invalidateQueries({ queryKey: ['policies'] }),
  ]), [qc])
}

// The changes. Each takes the step-up headers withStepUp adds when the server asks for them.
export const change = {
  addHost: (body: HostInput, h?: Headers) => unwrap(api.POST('/api/v1/hosts', { body, headers: h })),
  scanHost: (id: number, h?: Headers) => unwrap(api.POST('/api/v1/hosts/{id}/scan', { params: { path: { id } }, headers: h })),
  trustHost: (id: number, fingerprint: string, h?: Headers) =>
    unwrap(api.POST('/api/v1/hosts/{id}/trust', { params: { path: { id } }, body: { fingerprint }, headers: h })),
  acceptHostKey: (id: number, fingerprint: string, h?: Headers) =>
    unwrap(api.POST('/api/v1/hosts/{id}/accept-key', { params: { path: { id } }, body: { fingerprint }, headers: h })),
  checkHost: (id: number, h?: Headers) => unwrap(api.POST('/api/v1/hosts/{id}/check', { params: { path: { id } }, headers: h })),
  removeHost: (id: number, h?: Headers) => unwrap(api.DELETE('/api/v1/hosts/{id}', { params: { path: { id } }, headers: h })),
  addPolicy: (body: PolicyInput, h?: Headers) => unwrap(api.POST('/api/v1/policies', { body, headers: h })),
  patchPolicy: (id: number, body: PolicyPatch, h?: Headers) => unwrap(api.PATCH('/api/v1/policies/{id}', { params: { path: { id } }, body, headers: h })),
  removePolicy: (id: number, h?: Headers) => unwrap(api.DELETE('/api/v1/policies/{id}', { params: { path: { id } }, headers: h })),
  verifyRepository: (id: number, mode: 'sampled' | 'full', h?: Headers) =>
    unwrap(api.POST('/api/v1/repositories/{id}/verify', { params: { path: { id } }, body: { mode }, headers: h })),
  addUser: (body: NewUser, h?: Headers) => unwrap(api.POST('/api/v1/users', { body, headers: h })),
  patchUser: (id: number, body: UserPatch, h?: Headers) => unwrap(api.PATCH('/api/v1/users/{id}', { params: { path: { id } }, body, headers: h })),
  resetFactors: (id: number, h?: Headers) => unwrap(api.POST('/api/v1/users/{id}/factors/reset', { params: { path: { id } }, headers: h })),
  unlockUser: (id: number, h?: Headers) => unwrap(api.POST('/api/v1/users/{id}/unlock', { params: { path: { id } }, headers: h })),
  putSettings: (body: SettingsUpdate, h?: Headers) => unwrap(api.PUT('/api/v1/settings/signin', { body, headers: h })),
  endSession: (id: number, h?: Headers) => unwrap(api.DELETE('/api/v1/sessions/{id}', { params: { path: { id } }, headers: h })),
  endOthers: (h?: Headers) => unwrap(api.POST('/api/v1/sessions/end-others', { headers: h })),
}
