import { test } from 'node:test'
import assert from 'node:assert/strict'
import { capacity, durationBasis, orderReason, osClassBasis } from './en-capacity.ts'

// docs/design/v3 rule 3: the queue's reasons and the durations' bases are sentences, never the server's raw text with
// an ISO time in it (internal/capacity reasonOf and Expected).
test('a queued test says why it is where it is, in words', () => {
  const at = (iso: string) => (iso === '2026-11-22T15:10:00Z' ? '18:10' : 'other')
  assert.equal(orderReason('retest of a CRITICAL VM', at), 'Retest of a CRITICAL VM: these go first')
  assert.equal(orderReason('never proven', at), 'Never proven: its first restore test')
  assert.equal(orderReason('proof expires 2026-11-22T15:10:00Z (policy gold-db)', at), 'Its proof expires 18:10')
  assert.doesNotMatch(orderReason('proof expires 2026-11-22T15:10:00Z (policy gold-db)', at), /\d{4}-\d{2}-\d{2}T/)
})

test('an expected duration says where it comes from', () => {
  assert.equal(durationBasis('median of its last 10 tests'), 'median of its last 10 tests')
  assert.equal(durationBasis('median of its last 1 tests'), 'its last test')
  assert.equal(durationBasis('median of the windows class'), 'no test of its own yet: median of Windows VMs')
  assert.equal(durationBasis('default for linux'), 'no test measured yet: the default for Linux')
  assert.equal(osClassBasis('os: tag'), 'from its os: tag')
  assert.equal(osClassBasis('libvirt guest info'), "from libvirt's guest information")
  assert.equal(osClassBasis('check template'), "guessed from its check template's name")
  assert.equal(osClassBasis(undefined), '')
})

// T29: the room for restored disks in sizes, and a VM that never fits said as a consequence, not a raw byte count.
test('the room for restored disks is said in sizes', () => {
  assert.equal(capacity.disksText(6442450944, 36406611968), '6.0 GiB held by running tests, of 33.9 GiB')
  assert.equal(capacity.tooLarge(1, 36406611968),
    "1 VM's disks do not fit the sandbox's 33.9 GiB for restored disks, even with no other test running: it is never tested.")
  assert.match(capacity.tooLarge(3, 36406611968), /^3 VMs' disks do not fit .* they are never tested\.$/)
  assert.equal(capacity.diskNeed(34359738368), 'needs 32.0 GiB for its disks; it starts when running tests give the room back')
})
