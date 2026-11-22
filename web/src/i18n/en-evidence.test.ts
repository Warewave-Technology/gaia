import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ledger, ledgerState, type LedgerIn } from './en-evidence.ts'

const now = Date.parse('2026-11-22T14:30:00Z')
const base: LedgerIn = { checked_at: '2026-11-22T14:00:00Z', verdict: 'OK', entries: 184220, unchecked: 0, problems: [], reason: 'hourly' }

test('the server\'s code decides, and never reads calmer than it is', () => {
  assert.deepEqual(ledgerState({ ...base, status: 'OK', code: 'waiting', unchecked: 12 }, now), { status: 'OK', code: 'waiting' })
  assert.deepEqual(ledgerState({ ...base, status: 'CRITICAL', code: 'evidence_missing', entries: 0 }, now), { status: 'CRITICAL', code: 'evidence_missing' })
  // a code that is not OK with a status that says OK: the code wins
  assert.deepEqual(ledgerState({ ...base, status: 'OK', code: 'check_overdue' }, now), { status: 'UNKNOWN', code: 'check_overdue' })
})

test('an older server: the state is worked out from the latest check', () => {
  assert.equal(ledgerState({ ...base, checked_at: '' }, now).code, 'never_checked')
  assert.deepEqual(ledgerState({ ...base, verdict: 'CRITICAL', problems: ['entry 7: bad signature'] }, now), { status: 'CRITICAL', code: 'ledger_broken' })
  assert.deepEqual(ledgerState({ ...base, verdict: 'UNKNOWN' }, now), { status: 'UNKNOWN', code: 'check_failed' })
  // unchecked entries sealed after a check 30 min ago: they wait for the next hourly check
  assert.deepEqual(ledgerState({ ...base, unchecked: 12 }, now), { status: 'OK', code: 'waiting' })
  // after a check 3 h ago: overdue, not known
  assert.deepEqual(ledgerState({ ...base, checked_at: '2026-11-22T11:30:00Z', unchecked: 12 }, now), { status: 'UNKNOWN', code: 'check_overdue' })
  // an empty ledger while tests passed is a fault; with no test passed it is only empty
  assert.deepEqual(ledgerState({ ...base, entries: 0 }, now, true), { status: 'CRITICAL', code: 'evidence_missing' })
  assert.deepEqual(ledgerState({ ...base, entries: 0 }, now, false), { status: 'OK', code: 'ok' })
})

test('each state is said in a sentence, with no raw text in it', () => {
  const w = { ...base, unchecked: 12 }
  assert.equal(ledger.answer(ledgerState(w, now), w, 1800), 'Yes; 12 newer entries wait for the next hourly check.')
  const o = { ...base, checked_at: '2026-11-22T11:30:00Z', unchecked: 3 }
  assert.equal(ledger.answer(ledgerState(o, now), o, 3 * 3600), 'Not known: the ledger has not been checked for 3 h.')
  const b = { ...base, verdict: 'CRITICAL', problems: ['entry 7: signature does not verify'] }
  assert.doesNotMatch(ledger.lede(ledgerState(b, now), b, '14:00'), /signature does not verify/)
  const e = { ...base, entries: 0 }
  assert.match(ledger.answer(ledgerState(e, now, true), e, 0), /passed but left no evidence/)
})
