import { useState } from 'react'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { unwrap } from './errors'
import { usePages } from './pages'

// The Proof capacity screen's server-paged lists (docs/design/v3 rule 1): the test queue in the order it starts, and
// the capacity's inputs per VM (expected test durations and their basis). One page at a time, never the whole list.

export type TestQueueView = components['schemas']['TestQueueView']
export type CapacityInput = components['schemas']['CapacityInput'] & {
  /** Where the VM's OS class comes from (ADR-007 C13: its `os:` tag, libvirt's guest information, the check template's
   * name); a newer server sends it, an older one does not. */
  os_class_basis?: string
}

/** The queued tests, one page at a time; `head` is the newest page's answer (what runs now, what waits for memory). */
export function useTestQueuePages() {
  const [head, setHead] = useState<TestQueueView | null>(null)
  const pages = usePages(['views', 'test-queue'], async (after, limit) => {
    const p = await unwrap(api.GET('/api/v1/views/test-queue', { params: { query: { after: after ? Number(after) : undefined, limit } } }))
    setHead(p)
    return { rows: p.items, total: p.total, next: p.next }
  })
  return { pages, head }
}

/** The capacity's inputs per VM, one page at a time (the server answers at most 200). */
export function useCapacityInputPages() {
  const [medians, setMedians] = useState<Record<string, number>>({})
  const pages = usePages(['capacity', 'inputs'], async (after, limit) => {
    const p = await unwrap(api.GET('/api/v1/capacity/inputs', { params: { query: { after: after ? Number(after) : undefined, limit } } }))
    setMedians(p.class_medians_s)
    return { rows: p.items as CapacityInput[], total: p.total, next: p.next }
  }, 25, 60_000)
  return { pages, medians }
}
