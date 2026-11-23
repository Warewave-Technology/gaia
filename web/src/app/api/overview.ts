import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import type { ReasonIn } from '../../i18n/en-reasons'
import { unwrap } from './errors'
import { usePages, type Page } from './pages'

// The v3 Overview and Activity (T27 step 1b): every list one server page at a time (usePages, docs/design/v3 rule 1).

export type VMListRow = components['schemas']['VMListRow'] & {
  /** The deciding reason's parameters; a parallel server change adds them (read as optional until then). */
  params?: ReasonIn['params']
}
export type LedgerSummary = components['schemas']['LedgerSummary'] & {
  /** ADR-007 C14: the ledger's status on the overview and its code (ok, waiting, check_overdue, check_failed,
   * never_checked, ledger_broken, evidence_missing); a parallel server change adds them. */
  status?: string
  code?: string
}
export type AttentionGroup = components['schemas']['AttentionGroup']
export type CoverageGroup = components['schemas']['CoverageGroup']
export type ActivityRow = components['schemas']['ActivityRow']
export type ActivityView = components['schemas']['ActivityView']
export type QueueItem = components['schemas']['QueueItem']
export type TestQueueView = components['schemas']['TestQueueView']

const num = (c: string) => (c ? Number(c) : undefined)

/** Needs attention: one row per cause (code, scope). */
export function useAttentionPages() {
  return usePages<AttentionGroup>(['views', 'attention'], async (cursor, limit) => {
    const v = await unwrap(api.GET('/api/v1/views/attention', { params: { query: { after: num(cursor), limit } } }))
    return { rows: v.groups, total: v.total, next: v.next }
  })
}

/** Coverage by host, policy or tag; `all` also the groups whose VMs are all OK. */
export function useCoveragePages(by: string, all: boolean) {
  return usePages<CoverageGroup>(['views', 'coverage', by, all], async (cursor, limit) => {
    const v = await unwrap(api.GET('/api/v1/views/coverage', { params: { query: { by, all, after: num(cursor), limit } } }))
    return { rows: v.groups, total: all ? v.total : v.shown, next: v.next, healthy: v.healthy, groups: v.total } as Page<CoverageGroup> & CoverageCounts
  })
}
export type CoverageCounts = { healthy: number; groups: number }

/** The VM table's filter, as the URL holds it. */
export type VMFilter = {
  attention?: boolean
  status?: string
  host_id?: number
  policy_id?: number
  tag?: string
  code?: string
  scope_kind?: string
  scope_id?: number
  /** With a host-scoped code and its host: every VM on the host, whatever its deciding reason (ADR-007 C19.2). */
  reach?: boolean
  q?: string
}

export function useVMPages(f: VMFilter) {
  return usePages<VMListRow>(['views', 'vm-list', f], async (cursor, limit) => {
    const v = await unwrap(api.GET('/api/v1/views/vm-list', { params: { query: { ...f, after: cursor || undefined, limit } } }))
    return { rows: v.rows as VMListRow[], total: v.total, next: v.next }
  })
}

/** The Activity screen's filter, as the URL holds it. */
export type JobFilter = { kind?: string; state?: string; host_id?: number }

/** Jobs newest first, one page; `refresh` follows what the screen shows (faster while something runs or waits). */
export function useJobPages(f: JobFilter, refresh: number) {
  return usePages<ActivityRow>(['views', 'activity', f], async (cursor, limit) => {
    const v = await unwrap(api.GET('/api/v1/views/activity', { params: { query: { ...f, before: num(cursor), limit } } }))
    return { rows: v.rows, total: v.total, next: v.next }
  }, 25, refresh)
}

/** The jobs now and in the last hour, without the rows (the Activity header and its tabs' counts). */
export function useJobTotals(refresh: number) {
  return useQuery({
    queryKey: ['views', 'activity', 'totals'],
    queryFn: () => unwrap(api.GET('/api/v1/views/activity', { params: { query: { limit: 1 } } })),
    refetchInterval: refresh,
    refetchIntervalInBackground: true,
  })
}

/** The queued restore tests in the order they start, with position and estimated start (ADR-007 C4, C13). */
export function useTestQueuePages(refresh: number) {
  return usePages<QueueItem>(['views', 'test-queue'], async (cursor, limit) => {
    const v = await unwrap(api.GET('/api/v1/views/test-queue', { params: { query: { after: num(cursor), limit } } }))
    return { rows: v.items, total: v.total, next: v.next, head: v } as Page<QueueItem> & { head: TestQueueView }
  }, 25, refresh)
}
