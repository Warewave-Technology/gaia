import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { unwrap } from './errors'
import { usePaged } from './paged'

// The v2 overview's regions (T24 views): one request per region, each list in pages.

export type FleetView = components['schemas']['FleetView']
export type AttentionGroup = components['schemas']['AttentionGroup']
export type CoverageGroup = components['schemas']['CoverageGroup']
export type VMListRow = components['schemas']['VMListRow']
export type VMListView = components['schemas']['VMListView']
export type CapacitySummary = components['schemas']['CapacityView']

const REFRESH = 15_000

export function useFleet() {
  return useQuery({
    queryKey: ['views', 'fleet'],
    queryFn: () => unwrap(api.GET('/api/v1/views/fleet')),
    refetchInterval: REFRESH,
    refetchIntervalInBackground: true,
  })
}

/** The proof capacity's headline numbers (the lists are on the capacity screen). */
export function useCapacitySummary() {
  return useQuery({
    queryKey: ['views', 'capacity', 'summary'],
    queryFn: () => unwrap(api.GET('/api/v1/views/capacity', { params: { query: { limit: 1 } } })),
    refetchInterval: 60_000,
    refetchIntervalInBackground: true,
  })
}

export function useAttention(limit = 10) {
  return usePaged(
    ['views', 'attention', limit],
    (after) => unwrap(api.GET('/api/v1/views/attention', { params: { query: { after: after ? Number(after) : undefined, limit } } })),
    (p) => p.groups,
  )
}

export function useCoverage(by: string, all: boolean, limit = 10) {
  return usePaged(
    ['views', 'coverage', by, all, limit],
    (after) => unwrap(api.GET('/api/v1/views/coverage', { params: { query: { by, all, after: after ? Number(after) : undefined, limit } } })),
    (p) => p.groups,
  )
}

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
  q?: string
}

export function useVMList(f: VMFilter, limit = 50) {
  return usePaged(
    ['views', 'vm-list', f, limit],
    (after) => unwrap(api.GET('/api/v1/views/vm-list', { params: { query: { ...f, after: after || undefined, limit } } })),
    (p: VMListView) => p.rows,
  )
}

/** The names a filter offers: every host, every policy, and the tags in use. */
export function useFilterNames() {
  const hosts = useQuery({ queryKey: ['hosts'], queryFn: () => unwrap(api.GET('/api/v1/hosts')), staleTime: 60_000 })
  const policies = useQuery({ queryKey: ['policies'], queryFn: () => unwrap(api.GET('/api/v1/policies')), staleTime: 60_000 })
  const tags = useQuery({
    queryKey: ['views', 'coverage', 'tag', 'names'],
    queryFn: () => unwrap(api.GET('/api/v1/views/coverage', { params: { query: { by: 'tag', all: true, limit: 200 } } })),
    staleTime: 60_000,
  })
  return { hosts: hosts.data ?? [], policies: policies.data ?? [], tags: tags.data?.groups.map((g) => g.name) ?? [] }
}
