import { test } from 'node:test'
import assert from 'node:assert/strict'
import { known, ledgerState, queueOrder, scheduled, where } from './logic.ts'

const w = {
  scope: (kind: string, name: string) => `${kind} ${name}`,
  times: (name: string, n: number) => `${name} ×${n}`,
  noPolicy: 'no policy', moreHosts: 'more hosts', morePolicies: 'more policies',
}
const nc = (name: string, count: number, id = 1) => ({ id, name, count })

test('where: the scope, else the fewest names that hold every VM', () => {
  assert.equal(where({ count: 2, scope: { kind: 'host', id: 14, name: 'kvm-14' }, hosts: [nc('kvm-14', 2)], policies: [] }, w), 'host kvm-14')
  assert.equal(where({ count: 14, scope: null, hosts: [nc('a', 5), nc('b', 5), nc('c', 4)], policies: [nc('gold-db', 9), nc('std-linux', 5)] }, w),
    'gold-db ×9 · std-linux ×5')
  assert.equal(where({ count: 4, scope: null, hosts: [nc('kvm-07', 3), nc('kvm-11', 1)], policies: [nc('p', 2), nc('q', 1), nc('r', 1)] }, w), 'kvm-07 ×3 · kvm-11 ×1')
  assert.equal(where({ count: 5, scope: null, hosts: [nc('kvm-07', 5)], policies: [nc('dev', 5)] }, w), 'kvm-07')
  assert.equal(where({ count: 2750, scope: null, hosts: [nc('a', 39), nc('b', 37), nc('c', 36)], policies: [nc('x', 1514), nc('y', 900), nc('z', 300)] }, w),
    'a ×39 · b ×37 · c ×36 · more hosts')
  assert.equal(where({ count: 5, scope: null, hosts: [nc('a', 1), nc('b', 1), nc('c', 1)], policies: [nc('', 5, 0)] }, w), 'a ×1 · b ×1 · c ×1 · more hosts')
})

test('ledger: the server decides; without its code, never fine unchecked (ADR-007 C14)', () => {
  const now = Date.parse('2026-11-22T14:32:00Z')
  const base = { checked_at: '2026-11-22T14:30:00Z', verdict: 'OK', entries: 10, unchecked: 0 }
  assert.deepEqual(ledgerState({ ...base, status: 'UNKNOWN', code: 'check_failed' }, true, now), { status: 'UNKNOWN', code: 'check_failed' })
  assert.deepEqual(ledgerState(base, true, now), { status: 'OK', code: 'ok' })
  assert.deepEqual(ledgerState({ ...base, unchecked: 3 }, true, now), { status: 'OK', code: 'waiting' })
  assert.deepEqual(ledgerState({ ...base, checked_at: '2026-11-22T12:00:00Z' }, true, now), { status: 'UNKNOWN', code: 'check_overdue' })
  assert.deepEqual(ledgerState({ ...base, checked_at: '' }, true, now), { status: 'UNKNOWN', code: 'never_checked' })
  assert.deepEqual(ledgerState({ ...base, verdict: 'CRITICAL' }, true, now), { status: 'CRITICAL', code: 'ledger_broken' })
  assert.deepEqual(ledgerState({ ...base, verdict: 'UNKNOWN' }, true, now), { status: 'UNKNOWN', code: 'check_failed' })
  assert.deepEqual(ledgerState({ ...base, entries: 0 }, true, now), { status: 'CRITICAL', code: 'evidence_missing' })
  assert.deepEqual(ledgerState({ ...base, entries: 0 }, false, now), { status: 'OK', code: 'ok' })
})

test('the server words a job and a queue place is said from', () => {
  assert.deepEqual(scheduled('schedule: policy std-linux, every 43200 s'), { policy: 'std-linux', every_s: 43200 })
  assert.equal(scheduled('manual'), null)
  assert.deepEqual(queueOrder('proof expires 2026-11-23T01:00:00Z (policy gold-db)'), { kind: 'expires', at: '2026-11-23T01:00:00Z', policy: 'gold-db' })
  assert.equal(queueOrder('retest of a CRITICAL VM').kind, 'critical')
  assert.equal(known('0001-01-01T00:00:00Z'), false)
  assert.equal(known('2026-11-22T10:00:00Z'), true)
})
