import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { ApiError, unwrap } from './errors'
import { usePages } from './pages'
import { withStepUp, type StepUpAbout, type useStepUp } from '../layout/StepUp'
import type { VMView } from './views'

// The VM page's two long lists, paged on the server (T27 step 1b; docs/design/v3 rule 1): its kept points with what
// proves each one, and its status changes. A server older than these endpoints answers 404: the page then pages the
// lists GET /views/vms/{id} carries (every kept point, at most 1,000 changes), so it still works, only less far back.

export type VMPoint = components['schemas']['VMPoint']
export type PointTest = components['schemas']['PointTest']
export type VMStatusChange = components['schemas']['VMStatusChange']

/** A list endpoint this server does not have yet (an unknown API path is a 404 `not_found`). */
const missing = (e: unknown) => e instanceof ApiError && e.status === 404

export function useVMPoints(id: number) {
  const pages = usePages<VMPoint>(['views', 'vm', id, 'points'], async (cursor, limit) => {
    const r = await unwrap(api.GET('/api/v1/views/vms/{id}/points', { params: { path: { id }, query: { before: cursor ? Number(cursor) : undefined, limit } } }))
    return { rows: r.rows, total: r.total, next: r.next, pruned: r.pruned }
  })
  const pruned = (pages.data as { pruned?: number } | undefined)?.pruned ?? null
  return { pages, pruned, missing: missing(pages.error) }
}

export function useVMStatusHistory(id: number) {
  const pages = usePages<VMStatusChange>(['views', 'vm', id, 'status-history'], async (cursor, limit) => {
    const r = await unwrap(api.GET('/api/v1/views/vms/{id}/status-history', { params: { path: { id }, query: { before: cursor ? Number(cursor) : undefined, limit } } }))
    return { rows: r.rows, total: r.total, next: r.next }
  })
  return { pages, missing: missing(pages.error) }
}

/** A test's facts about its point, from the VM view's tests (the fallback for a server without the points list). */
function pointTestOf(t: VMView['tests'][number]): PointTest {
  const d = (t.detail && typeof t.detail === 'object' ? t.detail : {}) as { checks?: { name: string; verdict: string }[]; sandbox_down?: boolean }
  return {
    id: t.id, verdict: t.verdict, finished_at: t.finished_at, proven_rto_s: t.proven_rto_s,
    failed_check: d.checks?.find((c) => c.verdict === 'CRITICAL')?.name ?? '', sandbox_down: !!d.sandbox_down,
  }
}

/** The VM view's kept points in the points list's shape (newest first). Its tests are the newest 12 and the older
 * ones that did not pass, so an old point proven long ago may show as not tested: only until the server has the list. */
export function pointsFromView(v: VMView): VMPoint[] {
  return [...v.points].sort((a, b) => b.id - a.id).map((p) => {
    const tests = v.tests.filter((t) => t.point_id === p.id) // newest first
    const done = tests.filter((t) => t.finished_at)
    const passed = done.find((t) => t.verdict === 'OK')
    return {
      id: p.id, job_id: p.job_id, kind: p.kind, taken_at: p.taken_at, bytes: p.bytes, check_state: p.check_state, kind_reason: p.reason,
      proven: passed ? pointTestOf(passed) : null,
      latest: done[0] ? pointTestOf(done[0]) : null,
      running_test_id: tests.find((t) => !t.finished_at)?.id ?? 0,
    }
  })
}

/** The VM view's status changes in the list's shape: newest first, each until the next one. */
export function historyFromView(v: VMView): VMStatusChange[] {
  const h = v.status_history
  return h.map((c, i) => ({ ...c, until: i + 1 < h.length ? h[i + 1].at : '' })).reverse()
}

/** Assign a policy (0: none). A looser one asks for the second factor again (ADR-008 §4, the server decides). */
export function useAssignPolicy(id: number, ask: ReturnType<typeof useStepUp>, about: StepUpAbout) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (policy: number) =>
      withStepUp(ask, 'PATCH', `/api/v1/vms/${id}`, (h) =>
        unwrap(api.PATCH('/api/v1/vms/{id}', { params: { path: { id } }, body: { policy_id: policy }, headers: h })), about),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['views'] }),
  })
}

/** Every policy, for the VM's "Change policy" form. */
export function usePolicyNames(enabled: boolean) {
  return useQuery({ queryKey: ['policies'], queryFn: () => unwrap(api.GET('/api/v1/policies')), staleTime: 60_000, enabled })
}
