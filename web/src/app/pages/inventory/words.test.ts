import { test } from 'node:test'
import assert from 'node:assert/strict'
import { okShare } from '../../../i18n/en-inventory.ts'
import { auditOn, auditResult, auditWhat, onlyWhenWeaker, stepUpName } from './words.ts'

test('step-up routes have names; the weakening note is read, not shown', () => {
  assert.equal(stepUpName('POST /api/v1/points/{id}/forget'), 'Forget a point')
  assert.equal(stepUpName('GET /api/v1/evidence-export'), 'Export evidence')
  assert.equal(stepUpName('PATCH /api/v1/policies/{id} (when it weakens protection)'), 'Change a policy')
  assert.ok(onlyWhenWeaker('PATCH /api/v1/vms/{id} (when it weakens protection)'))
  assert.ok(!onlyWhenWeaker('POST /api/v1/users'))
  assert.equal(stepUpName('POST /api/v1/somewhere/new'), null)
})

test('the server\'s step-up list today has a name for every route', () => {
  const listed = ['POST /api/v1/login/cli/approve', 'PUT /api/v1/me/password', 'POST /api/v1/me/factors', 'DELETE /api/v1/me/factors/{id}',
    'POST /api/v1/me/recovery-codes', 'PUT /api/v1/settings/signin', 'POST /api/v1/api-tokens', 'DELETE /api/v1/api-tokens/{id}',
    'POST /api/v1/users', 'POST /api/v1/users/{id}/unlock', 'POST /api/v1/users/{id}/factors/reset', 'PATCH /api/v1/users/{id}',
    'POST /api/v1/hosts', 'DELETE /api/v1/hosts/{id}', 'PATCH /api/v1/vms/{id} (when it weakens protection)', 'POST /api/v1/repositories',
    'PATCH /api/v1/policies/{id} (when it weakens protection)', 'POST /api/v1/points/{id}/forget', 'GET /api/v1/evidence-export',
    'GET /api/v1/evidence/{id}/export', 'GET /api/v1/migration-checks/{id}/report']
  for (const r of listed) assert.ok(stepUpName(r), r)
})

test('audit entries as words', () => {
  const e = (action: string, target = '', detail = '', outcome = 200) => ({ action, target, detail, outcome })
  assert.equal(auditWhat(e('POST /api/v1/login/factor', 'user:ui-c')), 'signed in with a second factor')
  assert.equal(auditOn(e('POST /api/v1/login/factor', 'user:ui-c')), 'user ui-c')
  assert.equal(auditWhat(e('POST /api/v1/hosts/{id}/scan')), 'read a host\'s SSH key')
  assert.equal(auditOn(e('POST /api/v1/hosts/{id}/scan', '/api/v1/hosts/5/scan')), 'host 5')
  assert.equal(auditOn(e('POST /api/v1/users', '/api/v1/users')), '')
  assert.equal(auditWhat(e('step_up', 'POST /api/v1/users')), 'confirmed an action with a second factor')
  assert.equal(auditOn(e('step_up', 'POST /api/v1/users')), 'Create a user')
  assert.equal(auditOn(e('POST /api/v1/step-up', 'DELETE /api/v1/hosts/7')), 'Remove a host')
  assert.equal(auditWhat(e('session_end', 'session:3', 'ended: idle (by system)')), 'session ended after the idle time')
  assert.equal(auditWhat(e('session_end', 'session:3', 'something else')), 'session ended')
  assert.equal(auditOn(e('session_end', 'session:3')), 'session 3')
  assert.equal(auditOn(e('POST /api/v1/migration-sources/{id}/checks', 'migration_source:2:web-01')), 'migration source 2 · web-01')
  assert.equal(auditOn(e('GET /api/v1/evidence-export', 'evidence:export')), 'all evidence')
  assert.equal(auditWhat(e('POST /api/v1/unheard-of')), 'an action of the API')
  assert.equal(auditResult(0), 'asked')
  assert.equal(auditResult(201), 'done')
  assert.equal(auditResult(403), 'refused')
  assert.equal(auditResult(500), 'failed')
})

test('C12: how much of the day a VM can be OK, never rounded up', () => {
  assert.equal(okShare(1), 'all day')
  assert.equal(okShare(0.1799), 'at most 17 % of the day')
  assert.equal(okShare(0.004), 'less than 1 % of the day')
  assert.equal(okShare(0), 'never')
})
