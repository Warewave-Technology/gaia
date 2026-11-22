// The overview's small decisions, kept apart from React so they are tested (logic.test.ts).

type NameCount = { id: number; name: string; count: number }
type Group = { count: number; scope: { kind: string; id: number; name: string } | null; hosts: NameCount[]; policies: NameCount[] }
type WhereWords = {
  scope: (kind: string, name: string) => string
  times: (name: string, n: number) => string
  noPolicy: string
  moreHosts: string
  morePolicies: string
}

const sum = (xs: readonly NameCount[]) => xs.reduce((n, x) => n + x.count, 0)

/**
 * Where a cause's VMs are, in a few words (Main3's "Where"): its scope when it has one ("host kvm-14"); otherwise the
 * hosts or policies that hold them, whichever says it in fewer names ("gold-db ×9 · std-linux ×5"), and "more hosts"
 * when the server's short list does not hold them all.
 */
export function where(g: Group, w: WhereWords): string {
  if (g.scope) return w.scope(g.scope.kind, g.scope.name)
  const list = (xs: readonly NameCount[], more: string) => {
    const parts = xs.slice(0, 3).map((x) => {
      const nm = x.name || w.noPolicy
      return xs.length === 1 && x.count === g.count ? nm : w.times(nm, x.count)
    })
    if (sum(xs.slice(0, 3)) < g.count) parts.push(more)
    return parts.join(' · ')
  }
  const pol = g.policies.filter((p) => p.name)
  const polAll = pol.length > 0 && pol.length <= 3 && sum(pol) === g.count
  const hostAll = g.hosts.length > 0 && g.hosts.length <= 3 && sum(g.hosts) === g.count
  if (polAll && (!hostAll || pol.length < g.hosts.length)) return list(pol, w.morePolicies)
  if (g.hosts.length) return list(g.hosts, w.moreHosts)
  return pol.length ? list(pol, w.morePolicies) : ''
}

type Ledger = { checked_at: string; verdict: string; entries: number; unchecked: number; status?: string; code?: string }

/** Unchecked entries may wait this long before the ledger is not OK any more (ADR-007 C14). */
export const LEDGER_OVERDUE_S = 2 * 3600

/**
 * The ledger's status on the overview (ADR-007 C14). The server says it (`status`, `code`); until every server does,
 * the same rule from what the summary has: never checked, a failed or overdue check is UNKNOWN (rule 7: never fine
 * without a check), a broken ledger CRITICAL, no entries although restore tests passed CRITICAL.
 */
export function ledgerState(l: Ledger, testsPassed: boolean, nowMs: number): { status: string; code: string } {
  if (l.status && l.code) return { status: l.status, code: l.code }
  if (!l.checked_at) return { status: 'UNKNOWN', code: 'never_checked' }
  if (l.verdict === 'CRITICAL') return { status: 'CRITICAL', code: 'ledger_broken' }
  if (l.verdict !== 'OK') return { status: 'UNKNOWN', code: 'check_failed' }
  if (l.entries === 0 && l.unchecked === 0 && testsPassed) return { status: 'CRITICAL', code: 'evidence_missing' }
  const age = (nowMs - Date.parse(l.checked_at)) / 1000
  if (!(age <= LEDGER_OVERDUE_S)) return { status: 'UNKNOWN', code: 'check_overdue' }
  return l.unchecked > 0 ? { status: 'OK', code: 'waiting' } : { status: 'OK', code: 'ok' }
}

/** A Go zero time ("0001-01-01T00:00:00Z") or no time at all: the server does not know it. */
export const known = (iso: string | null | undefined): iso is string => !!iso && Date.parse(iso) > Date.parse('2000-01-01T00:00:00Z')

/** The why of a job, from the scheduler's words ("schedule: policy std-linux, every 43200 s"); null when it is not that. */
export function scheduled(reason: string): { policy: string; every_s: number } | null {
  const m = /^schedule: policy (.+), every (\d+) s$/.exec(reason)
  return m ? { policy: m[1], every_s: Number(m[2]) } : null
}

/** Why a restore test is where it is in the queue, from internal/capacity's words. */
export function queueOrder(reason: string): { kind: 'critical' | 'never' | 'expires' | 'other'; at?: string; policy?: string } {
  if (reason === 'retest of a CRITICAL VM') return { kind: 'critical' }
  if (reason === 'never proven') return { kind: 'never' }
  const m = /^proof expires (\S+) \(policy (.*)\)$/.exec(reason)
  return m ? { kind: 'expires', at: m[1], policy: m[2] } : { kind: 'other' }
}
