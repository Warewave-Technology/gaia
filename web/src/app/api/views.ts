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

// The screens refresh on their own: the status changes with time alone (RPO and proof age grow).
const REFRESH = 30_000

export function useOverview() {
  return useQuery({ queryKey: ['views', 'overview'], queryFn: () => unwrap(api.GET('/api/v1/views/overview')), refetchInterval: REFRESH })
}

export function useVMView(id: number) {
  return useQuery({
    queryKey: ['views', 'vm', id],
    queryFn: () => unwrap(api.GET('/api/v1/views/vms/{id}', { params: { path: { id } } })),
    refetchInterval: REFRESH,
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
