import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { reasons, vmReason } from './en-reasons.ts'

// docs/reason-codes.md is generated from internal/status/codes.go: every code the server can send has a sentence here
// (an unmapped reason is a test failure, CLAUDE.md T24; the screen builds its sentences from codes, ADR-007 C15).
const doc = readFileSync(new URL('../../../docs/reason-codes.md', import.meta.url), 'utf8')
const codes = [...doc.matchAll(/^\| `([a-z_]+)` \|/gm)].map((m) => m[1])

test('every reason code has a sentence', () => {
  assert.ok(codes.length >= 19, `found ${codes.length} codes in docs/reason-codes.md`)
  for (const c of codes) assert.ok(reasons[c], `no sentence for reason code ${c}`)
})

test('sentences are said in words, not formulas', () => {
  const now = Date.parse('2026-11-22T14:32:00Z')
  const r = vmReason({ code: 'proof_too_old', params: { test: 7, test_at: '2026-11-22T06:22:00Z', target_s: 6 * 3600 } }, now)
  assert.equal(r, 'Proof 8 h 10 min old, max 6 h')
  assert.equal(vmReason({ code: 'rto_over_target', params: { rto_s: 460, target_s: 300 } }, now), 'Restore takes 7 min 40 s, target 5 min')
  for (const c of codes) {
    const s = vmReason({ code: c, params: { point_at: '2026-11-22T10:00:00Z', test_at: '2026-11-22T10:00:00Z', target_s: 3600, rto_s: 90 }, scope: { kind: 'host', id: 1, name: 'kvm-14' } }, now)
    assert.doesNotMatch(s, /\d+h\d+m|\dm\d+s|<=|\d{4}-\d\d-\d\dT/, `${c}: ${s}`)
  }
})
