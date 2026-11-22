import { test } from 'node:test'
import assert from 'node:assert/strict'
import { vm3 as t } from '../../../i18n/en-vm.ts'
import { groups, marksOf, proofOf, stepOf, testSays, whyOf } from './logic.ts'

const point = (over: object) => ({
  id: 7, job_id: 1, kind: 'incremental', taken_at: '2026-11-22T04:51:00Z', bytes: 1, check_state: 'ok', kind_reason: 'incremental from the previous point',
  proven: null, latest: null, running_test_id: 0, ...over,
})
const test1 = (over: object) => ({ id: 9, verdict: 'OK', finished_at: '2026-11-22T05:02:00Z', proven_rto_s: 52, failed_check: '', sandbox_down: false, ...over })

test('marks closer than 6 px merge into one, with the worst look', () => {
  const start = Date.parse('2026-11-22T00:00:00Z')
  const at = (min: number) => new Date(start + min * 60_000).toISOString()
  const events = [
    { kind: 'backup', id: 1, at: at(0) }, { kind: 'backup', id: 2, at: at(2) }, { kind: 'damaged', id: 3, at: at(4) },
    { kind: 'backup', id: 4, at: at(60) },
    { kind: 'test_ok', id: 5, at: at(3) }, { kind: 'test_failed', id: 6, at: at(4) },
    { kind: 'backup', id: 8, at: at(-5) }, // before the window
  ]
  // 1440 px for 24 h: one minute is one pixel.
  const gs = groups(marksOf(events, start, 86_400_000, 1440))
  const backups = gs.filter((g) => g.lane === 'backup')
  assert.deepEqual(backups.map((g) => g.marks.map((m) => m.e.id)), [[1, 2, 3], [4]])
  assert.equal(backups[0].look, 'd')
  const tests = gs.filter((g) => g.lane === 'test')
  assert.equal(tests.length, 1)
  assert.equal(tests[0].look, 'f')
  assert.equal(tests[0].marks.length, 2)
  // 6 px apart is two marks
  assert.equal(groups(marksOf([{ kind: 'backup', id: 1, at: at(10) }, { kind: 'backup', id: 2, at: at(16) }], start, 86_400_000, 1440)).length, 2)
})

test('a point is proven only by a passing test; untested is neutral, never ✓ (ADR-007 C16)', () => {
  assert.deepEqual(proofOf(point({}), null, t), { proof: 'BACKED_UP', note: 'not tested', detail: '' })
  assert.equal(proofOf(point({ kind: 'full', kind_reason: 'a disk was resized' }), null, t).detail, 'a disk was resized')
  assert.deepEqual(proofOf(point({ proven: test1({}) }), 7, t), { proof: 'PROVEN', note: 'return here', detail: 'test 9 · 52 s' })
  assert.equal(proofOf(point({ proven: test1({}), check_state: 'failed' }), 7, t).proof, 'DAMAGED')
  assert.deepEqual(proofOf(point({ latest: test1({ verdict: 'CRITICAL', failed_check: 'boot', proven_rto_s: null }) }), null, t),
    { proof: 'FAILED', note: '', detail: 'test 9 · copy did not boot' })
  assert.equal(proofOf(point({ latest: test1({ verdict: 'UNKNOWN', sandbox_down: true }) }), null, t).proof, 'UNKNOWN')
  assert.equal(proofOf(point({ latest: test1({ verdict: 'WARNING' }) }), null, t).proof, 'BACKED_UP')
  assert.equal(proofOf(point({ running_test_id: 11 }), null, t).note, 'being tested')
  assert.equal(proofOf(point({ check_state: 'unchecked' }), null, t).note, 'check pending')
})

test('test steps are sentences, not probe output (docs/design/v3 rule 3)', () => {
  const boot = stepOf({ name: 'boot', type: 'boot_serial', verdict: 'CRITICAL', seconds: 300, detail: 'no match for "(?m)login: *$" on the serial console within 300 s' }, t)
  assert.deepEqual(boot, { name: 'Boot', text: 'No login prompt on the serial console within 5 min', notRun: false })
  assert.equal(stepOf({ name: 'boot', verdict: 'CRITICAL' }, t).text, 'The copy did not boot')
  assert.equal(stepOf({ name: 'boot', type: 'winrm', verdict: 'OK', detail: 'POST http://10.254.0.10:5985/wsman: Identify refused (401)' }, t).text, 'Windows answered on WinRM')
  assert.deepEqual(stepOf({ name: 'postgres', type: 'tcp', verdict: 'UNKNOWN', detail: 'not run: the guest did not boot' }, t),
    { name: 'Postgres', text: 'Skipped: the copy did not boot.', notRun: true })
  assert.equal(stepOf({ name: 'image_unchanged_vda', type: 'image_hash', verdict: 'OK', detail: 'sha256 before and after are equal' }, t).name, 'Unchanged (vda)')
  assert.equal(stepOf({ name: 'stable', verdict: 'OK' }, t).name, 'Stays up')
  for (const s of [boot]) assert.doesNotMatch(s.text, /\(\?m\)|\d+ s\b|login: \*/)
})

test('a test, a status change: what they say comes from verdicts and codes', () => {
  assert.equal(testSays({ verdict: 'CRITICAL', proven_rto_s: null, detail: { checks: [{ name: 'boot', verdict: 'CRITICAL' }] } }, t), 'Restored, but the copy did not boot.')
  assert.equal(testSays({ verdict: 'OK', proven_rto_s: 294, detail: {} }, t), 'Restored, booted and every check passed; running again in 4 min 54 s.')
  assert.equal(testSays({ verdict: 'UNKNOWN', proven_rto_s: null, detail: { sandbox_down: true } }, t), 'The test could not run: the sandbox did not answer. It proves nothing.')
  assert.equal(whyOf({ code: 'test_failed_boot' }, t), 'A restore test failed: the copy did not boot.')
  assert.equal(whyOf({ code: 'test_not_run' }, t), 'Restore test could not run.')
  assert.equal(whyOf({ code: '' }, t), t.whyNoCode)
})
