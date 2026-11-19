import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { unwrap } from './errors'

export type OverviewView = components['schemas']['OverviewView']
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

export function useOverview() {
  return useQuery({
    queryKey: ['views', 'overview'],
    queryFn: () => unwrap(api.GET('/api/v1/views/overview')),
    refetchInterval: (q) => (q.state.data?.vms.some((v) => v.activity.length > 0) ? BUSY : REFRESH),
    refetchIntervalInBackground: true,
  })
}

export function useVMView(id: number) {
  return useQuery({
    queryKey: ['views', 'vm', id],
    queryFn: () => unwrap(api.GET('/api/v1/views/vms/{id}', { params: { path: { id } } })),
    refetchInterval: (q) => ((q.state.data?.status.activity.length ?? 0) > 0 ? BUSY : REFRESH),
    refetchIntervalInBackground: true,
  })
}

export function useSystem() {
  return useQuery({ queryKey: ['system'], queryFn: () => unwrap(api.GET('/api/v1/system')), refetchInterval: REFRESH })
}

export function useEvidence() {
  return useQuery({ queryKey: ['evidence'], queryFn: () => unwrap(api.GET('/api/v1/evidence', { params: { query: { limit: 1000 } } })) })
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

export function useVMs() {
  return useQuery({ queryKey: ['vms'], queryFn: () => unwrap(api.GET('/api/v1/vms')) })
}

export function useDailyReports() {
  return useQuery({ queryKey: ['daily-reports'], queryFn: () => unwrap(api.GET('/api/v1/daily-reports', { params: { query: { limit: 1 } } })) })
}

export type MigrationView = components['schemas']['MigrationView']
export type MigrationCheckView = components['schemas']['MigrationCheckView']
export type SourceVM = components['schemas']['SourceVM']
export type MigrationSource = components['schemas']['MigrationSource']

/** The migration screen; it refreshes faster while a check is queued or running. */
export function useMigrationView() {
  return useQuery({
    queryKey: ['views', 'migration'],
    queryFn: () => unwrap(api.GET('/api/v1/views/migration')),
    refetchInterval: (q) => (q.state.data?.checks.some((c) => c.job_state === 'queued' || c.job_state === 'running') ? 5_000 : REFRESH),
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

/** "Check": queue a migration check of one VM of a source. */
export function useQueueMigrationCheck(source: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (vm: string) => unwrap(api.POST('/api/v1/migration-sources/{id}/checks', { params: { path: { id: source } }, body: { vm } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['views', 'migration'] }),
  })
}
