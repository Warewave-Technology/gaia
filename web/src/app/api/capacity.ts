import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { unwrap } from './errors'
import { usePaged } from './paged'

// The Proof capacity and Activity screens (T25 views, T27): the capacity and its what-if, the test queue in pages,
// and the jobs newest first in pages.

export type CapacityView = components['schemas']['CapacityView']
export type WhatIf = components['schemas']['WhatIf']
export type CapacityGroup = components['schemas']['Group']
export type Expiring = components['schemas']['Expiring']
export type QueueItem = components['schemas']['QueueItem']
export type TestQueueView = components['schemas']['TestQueueView']
export type ActivityView = components['schemas']['ActivityView']
export type ActivityRow = components['schemas']['ActivityRow']
export type JobCount = components['schemas']['JobCount']

const REFRESH = 30_000

/** The capacity now, with the first `limit` entries of its lists (the expiring proofs grow by asking for more). */
export function useCapacity(limit: number) {
  return useQuery({
    queryKey: ['views', 'capacity', limit],
    queryFn: () => unwrap(api.GET('/api/v1/views/capacity', { params: { query: { limit } } })),
    refetchInterval: REFRESH,
    refetchIntervalInBackground: true,
    placeholderData: keepPreviousData,
  })
}

/** The capacity with other slots or policy intervals. Nothing is saved; `null` asks nothing. */
export function useWhatIf(w: WhatIf | null) {
  return useQuery({
    queryKey: ['capacity', 'what-if', w],
    queryFn: () => unwrap(api.POST('/api/v1/capacity/what-if', { body: w ?? {} })),
    enabled: w !== null,
    refetchInterval: REFRESH,
    refetchIntervalInBackground: true,
    placeholderData: keepPreviousData,
  })
}

/** Every policy: the what-if's choices and the policies' test intervals. */
export function usePolicies() {
  return useQuery({ queryKey: ['policies'], queryFn: () => unwrap(api.GET('/api/v1/policies')), staleTime: 60_000 })
}

export function useHosts() {
  return useQuery({ queryKey: ['hosts'], queryFn: () => unwrap(api.GET('/api/v1/hosts')), staleTime: 60_000 })
}

/** The queued restore tests in the order they start; each page also says what runs and what waits for memory. */
export function useTestQueue(limit = 25) {
  return usePaged(
    ['views', 'test-queue', limit],
    (after) => unwrap(api.GET('/api/v1/views/test-queue', { params: { query: { after: after ? Number(after) : undefined, limit } } })),
    (p: TestQueueView) => p.items,
  )
}

/** The Activity screen's filter, as the URL holds it. */
export type ActivityFilter = { kind?: string; state?: string; host_id?: number }

/** Jobs newest first; `refresh` follows what the screen shows (faster while something runs or waits). */
export function useActivity(f: ActivityFilter, refresh: number, limit = 50) {
  return usePaged(
    ['views', 'activity', f, limit],
    (before) => unwrap(api.GET('/api/v1/views/activity', { params: { query: { ...f, before: before ? Number(before) : undefined, limit } } })),
    (p: ActivityView) => p.rows,
    refresh,
  )
}
