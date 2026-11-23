import { keepPreviousData, useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { unwrap } from './errors'
import { usePages, type Page } from './pages'
import { withStepUp, type useStepUp } from '../layout/StepUp'

export type VMOverview = components['schemas']['VMOverview']
export type VMView = components['schemas']['VMView']
export type TestView = components['schemas']['TestView']
export type TimelineEvent = components['schemas']['TimelineEvent']
export type SystemInfo = components['schemas']['SystemInfo']
export type EvidenceBundle = components['schemas']['EvidenceBundle']

// The screens refresh on their own: the status changes with time alone (RPO and proof age grow), and faster while
// a job runs or waits, so its end shows within seconds. They refresh in a background tab too: a wall screen is one.
const REFRESH = 15_000
const BUSY = 5_000

/** One VM's page; `windowH` is the history strip's hours (24 or 168). The last answer stays while another range loads. */
export function useVMView(id: number, windowH: 24 | 168 = 24) {
  return useQuery({
    queryKey: ['views', 'vm', id, windowH],
    queryFn: () => unwrap(api.GET('/api/v1/views/vms/{id}', { params: { path: { id }, query: { window_h: windowH } } })),
    refetchInterval: (q) => ((q.state.data?.status.activity.length ?? 0) > 0 ? BUSY : REFRESH),
    refetchIntervalInBackground: true,
    placeholderData: keepPreviousData,
  })
}

/** The VM's row of the VM table (its tags, its deciding code and scope): the list narrowed to its host and name. */
export function useVMRow(id: number, hostID: number, name: string) {
  return useQuery({
    queryKey: ['views', 'vm-list', 'row', id, hostID, name],
    queryFn: async () => {
      const l = await unwrap(api.GET('/api/v1/views/vm-list', { params: { query: { host_id: hostID, q: name, limit: 50 } } }))
      return l.rows.find((r) => r.vm_id === id) ?? null
    },
    refetchInterval: REFRESH,
    refetchIntervalInBackground: true,
  })
}

/** The VMs whose status has the same deciding cause (code and scope): how many, and the first page of them. */
export function useSameCause(code: string, scope: { kind: string; id: number } | null, enabled: boolean) {
  return useQuery({
    queryKey: ['views', 'vm-list', 'cause', code, scope?.kind ?? '', scope?.id ?? 0],
    queryFn: () =>
      unwrap(api.GET('/api/v1/views/vm-list', { params: { query: { code, scope_kind: scope?.kind, scope_id: scope?.id, limit: 25 } } })),
    enabled: enabled && code !== '',
    refetchInterval: REFRESH,
    refetchIntervalInBackground: true,
  })
}

/** The closed list of reason codes (docs/reason-codes.md): the cause's name of a code. */
export function useReasonCodes() {
  return useQuery({ queryKey: ['reason-codes'], queryFn: () => unwrap(api.GET('/api/v1/reason-codes')), staleTime: Infinity })
}

/** Replace a VM's tags. */
export function useSetTags(id: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (tags: string[]) => unwrap(api.PUT('/api/v1/vms/{id}/tags', { params: { path: { id } }, body: { tags } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['views'] }),
  })
}

/** Forget a damaged point (ADR-007 C6): admins only, with a reason, behind a step-up (ADR-008 §4). */
export function useForgetPoint(ask: ReturnType<typeof useStepUp>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, reason }: { id: number; reason: string }) =>
      withStepUp(ask, 'POST', `/api/v1/points/${id}/forget`, (h) =>
        unwrap(api.POST('/api/v1/points/{id}/forget', { params: { path: { id } }, body: { reason }, headers: h }))),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['views'] }),
  })
}

export function useSystem() {
  return useQuery({ queryKey: ['system'], queryFn: () => unwrap(api.GET('/api/v1/system')), refetchInterval: REFRESH })
}

/** The newest evidence bundles, each with its ledger entry. */
export function useEvidence(limit: number) {
  return useQuery({
    queryKey: ['evidence', limit],
    queryFn: () => unwrap(api.GET('/api/v1/evidence', { params: { query: { limit } } })),
    refetchInterval: REFRESH,
    placeholderData: keepPreviousData,
  })
}

/** The evidence ledger's latest check. */
export function useLedger() {
  return useQuery({ queryKey: ['ledger'], queryFn: () => unwrap(api.GET('/api/v1/ledger')), refetchInterval: REFRESH })
}

/** "Check now" (admins): the ledger and its bundles are checked at once, not at the next daily report. */
export function useCheckLedger() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/ledger/verify')),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['ledger'] }),
  })
}

/** The names of some VMs, one request each (a page shows a few; the whole list is never loaded). */
export function useVMNames(ids: readonly number[]) {
  const qs = useQueries({
    queries: ids.map((id) => ({
      queryKey: ['vms', id],
      queryFn: () => unwrap(api.GET('/api/v1/vms/{id}', { params: { path: { id } } })),
      staleTime: 300_000,
    })),
  })
  return new Map(qs.flatMap((q) => (q.data ? [[q.data.id, q.data.name] as const] : [])))
}

/** "Back up now" and "Test a restore now": queue a job, then refresh the VM. */
export function useVMAction(id: number, what: 'backup' | 'test') {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () =>
      what === 'backup'
        ? unwrap(api.POST('/api/v1/vms/{id}/backups', { params: { path: { id } } }))
        : unwrap(api.POST('/api/v1/vms/{id}/tests', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['views'] }),
  })
}

/** The newest daily report: the last time the ledger's head was published outside Gaia. */
export function useDailyReports() {
  return useQuery({
    queryKey: ['daily-reports'],
    queryFn: () => unwrap(api.GET('/api/v1/daily-reports', { params: { query: { limit: 1 } } })),
    refetchInterval: 60_000,
  })
}

export type MigrationView = components['schemas']['MigrationView']
export type MigrationCheckView = components['schemas']['MigrationCheckView']
export type SourceVM = components['schemas']['SourceVM']
export type MigrationSource = components['schemas']['MigrationSource']

/** A check's result as the server filters it: its readiness, or `open` while it is queued or running. */
export type MigrationResult = 'OK' | 'WARNING' | 'CRITICAL' | 'UNKNOWN' | 'open'
/** One page of the migration screen: the checks, and the screen's head (converter, sources, counts) it came with. */
export type MigrationPageData = Page<MigrationCheckView> & { view: MigrationView }

/**
 * The migration screen's VMs, a page at a time from the server (ADR-007 C18.7): each VM's newest check (its result),
 * worst first, then by name, optionally only one result. Every page carries the converter, the sources and the
 * counts by result over the whole list (MigrationPageData.view).
 */
export function useMigrationPages(result: MigrationResult | '') {
  return usePages<MigrationCheckView>(['views', 'migration', 'latest', result], async (cursor, limit): Promise<MigrationPageData> => {
    const v = await unwrap(api.GET('/api/v1/views/migration', {
      params: { query: { latest: true, result: result || undefined, after: cursor ? Number(cursor) : undefined, limit } },
    }))
    return { rows: v.checks, total: v.total, next: v.next, view: v }
  }, 25, REFRESH)
}

/** One VM's checks, newest first (its result, then its history); faster while one is queued or running. */
export function useMigrationVMChecks(sourceID: number, vm: string, limit = 7) {
  return useQuery({
    queryKey: ['views', 'migration', 'vm', sourceID, vm, limit],
    queryFn: () => unwrap(api.GET('/api/v1/views/migration', { params: { query: { source_id: sourceID, vm, limit } } })),
    refetchInterval: (q) => (q.state.data?.checks.some((c) => c.job_state === 'queued' || c.job_state === 'running') ? BUSY : REFRESH),
    refetchIntervalInBackground: true,
    placeholderData: keepPreviousData,
  })
}

/** A source's VMs, read when asked (it logs in to vSphere: read-only calls only). */
export function useSourceInventory(id: number, enabled: boolean) {
  return useQuery({
    queryKey: ['inventory', id],
    queryFn: () => unwrap(api.GET('/api/v1/migration-sources/{id}/inventory', { params: { path: { id } } })),
    enabled,
    staleTime: 60_000,
  })
}

/** The inventories of the sources read so far (each read when asked, then kept for a minute), by source. */
export function useSourceInventories(ids: readonly number[]) {
  const qs = useQueries({
    queries: ids.map((id) => ({
      queryKey: ['inventory', id],
      queryFn: () => unwrap(api.GET('/api/v1/migration-sources/{id}/inventory', { params: { path: { id } } })),
      staleTime: 60_000,
    })),
  })
  return new Map(qs.flatMap((q, i) => (q.data ? [[ids[i], q.data] as const] : [])))
}

/** "Check": queue a migration check of one VM of a source. */
export function useQueueMigrationCheck(source: number) {
  const qc = useQueryClient()
  return useMutation({
    // exclude: virtio-win drivers the Windows guest is converted without (ADR-005 C); the result is at best WARNING
    mutationFn: ({ vm, exclude }: { vm: string; exclude?: string[] }) =>
      unwrap(api.POST('/api/v1/migration-sources/{id}/checks', {
        params: { path: { id: source } }, body: { vm, ...(exclude?.length ? { exclude_drivers: exclude } : {}) },
      })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['views', 'migration'] }),
  })
}
