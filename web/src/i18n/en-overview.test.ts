import { test } from 'node:test'
import assert from 'node:assert/strict'
import { v3 } from './en-overview.ts'
import { reachOf } from '../app/pages/overview/logic.ts'

const t = v3.overview

// ADR-007 C19.2: a host-scoped cause states every VM on its host, not only the VMs it decides.
test('a host-scoped cause says its full reach', () => {
  const g = (count: number, host_vms: number, elsewhere: number, ok = 0) => ({ count, reach: { host_vms, elsewhere, ok } })
  assert.equal(t.reach(reachOf(g(38, 41, 3))!), 'affects 41 VMs · 38 here, 3 under other causes')
  assert.equal(t.reach(reachOf(g(40, 41, 1))!), 'affects 41 VMs · 40 here, 1 under another cause')
  assert.equal(t.reach(reachOf(g(36, 41, 3, 2))!), 'affects 41 VMs · 36 here, 3 under other causes, 2 still OK')
  assert.equal(t.reach(reachOf(g(1200, 1250, 50))!), 'affects 1,250 VMs · 1,200 here, 50 under other causes')
  assert.equal(t.reachOf('host kvm-07', reachOf(g(38, 41, 3))!), 'Host kvm-07: this cause affects 41 VMs · 38 here, 3 under other causes.')
  assert.equal(t.ofHost(41), 'of 41')
  // the whole host is the group: the count says it all; any other group has no reach
  assert.equal(reachOf(g(41, 41, 0)), null)
  assert.equal(reachOf({ count: 5, reach: null }), null)
  assert.equal(reachOf({ count: 5 }), null)
  assert.equal(t.showVMs(41), 'Show these 41 VMs →')
})
