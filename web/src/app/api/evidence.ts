import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { unwrap } from './errors'
import { usePages } from './pages'

// The Evidence screen's lists (docs/design/v3 rule 1): the ledger entries one page at a time (GET /api/v1/views/evidence,
// views_evidence.go), with a filter the URL keeps.

export type EvidenceRow = components['schemas']['EvidenceRow']
/** The ledger summary with ADR-007 C14's fields, which a newer server sends. */
export type LedgerSummary = components['schemas']['LedgerSummary'] & { status?: string; code?: string; oldest_unchecked_at?: string | null }

export type EvidenceFilter = { kind?: 'test' | 'migration'; verdict?: 'CRITICAL' }

export function useEvidencePages(f: EvidenceFilter) {
  return usePages(['evidence', 'view', f], async (before, limit) => {
    const p = await unwrap(api.GET('/api/v1/views/evidence', { params: { query: { ...f, before: before ? Number(before) : undefined, limit } } }))
    return { rows: p.rows, total: p.total, next: p.next }
  })
}

/** Whether any restore test passed: an empty ledger is then a fault (C14 evidence_missing), for a server that does not
 * say so itself. The fleet's "can return" counts VMs with a point a passing test proved. */
export function useAnyTestPassed(enabled: boolean) {
  const q = useQuery({
    queryKey: ['views', 'fleet'],
    queryFn: () => unwrap(api.GET('/api/v1/views/fleet')),
    enabled,
    staleTime: 15_000,
  })
  return q.data ? q.data.can_return > 0 : null
}
