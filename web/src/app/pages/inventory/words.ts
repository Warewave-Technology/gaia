// Routes and audit records in words (docs/design/v3 rule 3): the step-up list and the audit log show what was done,
// never a raw route; the route, target and detail as the server recorded them stay under the row's details.
import { auditDid, inventory, sessionEnd, stepUpNames, targetKind } from '../../../i18n/en-inventory.ts'

/** "PATCH /api/v1/policies/{id}" matches "PATCH /api/v1/policies/12"; the pattern's words, else null. */
export function byRoute(table: Record<string, string>, route: string): string | null {
  const r = route.replace(/ \(.*\)$/, '').replace(/\?.*$/, '').trim() // "… (when it weakens protection)", a query
  if (table[r] !== undefined) return table[r]
  for (const [k, v] of Object.entries(table)) {
    if (!k.includes('{')) continue
    const re = new RegExp(`^${k.replace(/[.*+?^$()|[\]\\]/g, '\\$&').replace(/\{[^}]+\}/g, '[^/ ]+')}$`)
    if (re.test(r)) return v
  }
  return null
}

/** A step-up action's name, from the route the server lists it under. Null for a route this screen has no name for. */
export function stepUpName(route: string): string | null {
  return byRoute(stepUpNames, route)
}

/** The server's step-up routes, the ones that touch data first, then protection, accounts and one's own account
 * (the order of stepUpNames); routes this screen has no name for come last, as the server listed them. */
export function stepUpOrder(routes: readonly string[]): string[] {
  const keys = Object.keys(stepUpNames)
  const rank = (r: string) => {
    const name = stepUpName(r)
    const i = name === null ? -1 : keys.findIndex((k) => stepUpNames[k] === name)
    return i < 0 ? keys.length : i
  }
  return routes.map((r, i) => ({ r, i, k: rank(r) })).sort((a, b) => a.k - b.k || a.i - b.i).map((x) => x.r)
}

/** Whether the server lists the route as a step-up only when it weakens protection. */
export function onlyWhenWeaker(route: string): boolean {
  return /\(when it weakens protection\)$/.test(route)
}

export type AuditIn = { action: string; target: string; detail: string; outcome: number }

/** What an audit entry did, in the past tense: "created a user", "confirmed an action with a second factor". */
export function auditWhat(e: AuditIn): string {
  if (e.action === 'session_end') {
    const m = /^ended: ([a-z_]+)/.exec(e.detail)
    return (m && sessionEnd[m[1]]) ? `session ${sessionEnd[m[1]]}` : auditDid.session_end
  }
  return byRoute(auditDid, e.action) ?? inventory.audit.other
}

/**
 * What an audit entry acted on, in words: "user ui-c", "host 5", "session 13"; for a step-up the action it confirms
 * ("Create a user"). Empty when the target says no more than the action (a list route).
 */
export function auditOn(e: AuditIn): string {
  const t = e.target.trim()
  if (!t) return ''
  const route = /^(GET|POST|PUT|PATCH|DELETE) (\/\S+)$/.exec(t)
  if (route) return stepUpName(t) ?? byRoute(auditDid, t) ?? ''
  if (t.startsWith('/')) {
    // a path: the last "<kind>/<id>" in it, e.g. /api/v1/hosts/5/scan → host 5
    const parts = t.split('/').filter(Boolean)
    for (let i = parts.length - 1; i > 0; i--) {
      if (/^\d+$/.test(parts[i]) && targetKind[parts[i - 1]]) return `${targetKind[parts[i - 1]]} ${parts[i]}`
    }
    return ''
  }
  if (t === 'evidence:export') return 'all evidence'
  const kv = /^([a-z_-]+):(.+)$/.exec(t)
  if (kv) {
    const kind = targetKind[kv[1]] ?? kv[1].replace(/[_-]/g, ' ')
    return `${kind} ${kv[2].replace(/:/g, ' · ')}`
  }
  return t
}

export type AuditResult = 'done' | 'refused' | 'failed' | 'asked'

/** An entry's outcome as a word: an HTTP status as the server recorded it (0: a step that was only asked for). */
export function auditResult(outcome: number): AuditResult {
  if (outcome === 0) return 'asked'
  if (outcome >= 500) return 'failed'
  if (outcome >= 400) return 'refused'
  return 'done'
}
