import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { access, can, roles } from './access.ts'

test('can() is what access.json says', () => {
  for (const [ability, allowed] of Object.entries(access.abilities)) {
    for (const role of roles) {
      assert.equal(can({ role }, ability as keyof typeof access.abilities), allowed.includes(role), `${role} ${ability}`)
    }
    assert.equal(can(null, ability as keyof typeof access.abilities), false)
  }
})

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    return statSync(p).isDirectory() ? files(p) : /\.tsx?$/.test(n) && !n.endsWith('.test.ts') ? [p] : []
  })
}

// Every call the screens make, and every link into the API, is in access.json: a page cannot start to need an
// endpoint without the role test (TestPageRoles) checking it for every role.
test('every API call in src/app is listed in access.json', () => {
  const listed = new Set([...access.session, ...access.pages.flatMap((p) => [...p.reads, ...Object.values(p.actions).flat()])])
  const used = new Set<string>()
  for (const f of files(new URL('.', import.meta.url).pathname)) {
    const src = readFileSync(f, 'utf8')
    for (const m of src.matchAll(/api\.(GET|POST|PUT|PATCH|DELETE)\('([^']+)'/g)) used.add(`${m[1]} ${m[2]}`)
    for (const m of src.matchAll(/(?:href|src)=\{?[`"](\/api\/v1\/[^`"?]*)/g)) {
      used.add('GET ' + m[1].replace(/\$\{[^}]+\}/g, (_, i: number) => (m[1].slice(i - 6, i).includes('files') ? '{name}' : '{id}')))
    }
  }
  assert.ok(used.size >= 10, `found only ${[...used]}`)
  for (const u of used) assert.ok(listed.has(u), `${u} is used by the UI but not in access.json`)
})
