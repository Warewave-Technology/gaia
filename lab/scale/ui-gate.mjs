#!/usr/bin/env node
// fixture: the T27 gate's browser checks (CLAUDE.md T27 (c), (d)), against a running Gaia server and its embedded UI.
// Not part of the product. It drives a headless Chrome over the DevTools protocol (no dependency but Chrome and
// Node 22, whose WebSocket it uses) and, for every screen:
//   (d) walks the rendered DOM: every status mark (a StatusChip, [data-status]) carries its word, and every status bar
//       ([data-status-bar]) has the words of its statuses beside it;
//   lists: every table's rows are counted against the totals the page states ("Showing 50 of 5,000"), and every API
//       list request is checked for a page size (limit) and a total it did not load at once;
//   (c) on the overview: the time from navigation until the headline and the first rows of the VM table are drawn.
// The session is a bearer token sent with every request (Network.setExtraHTTPHeaders): a CLI session of the gate's
// own user, read from a file, never printed.
//
//   node lab/scale/ui-gate.mjs -base https://HOST:8443 -token-file F [-chrome PATH] [-runs 3] [-routes /,/capacity,...]
//        [-vm ID] [-shots DIR] [-width 1440] > ui-gate.json
//   -vm ID     adds that VM's page (/vms/ID) to the screens
//   -shots DIR writes a full-page PNG of every screen there (T27 step 1), and of the sign-in page without the session
//
// Exit 0 OK, 2 CRITICAL (a check failed), 3 UNKNOWN (could not run).
import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const args = Object.fromEntries(process.argv.slice(2).reduce((acc, a, i, all) => (a.startsWith('-') ? [...acc, [a.slice(1), all[i + 1]]] : acc), []))
const base = (args.base || '').replace(/\/$/, '')
if (!base || !args['token-file']) {
  console.error('usage: ui-gate.mjs -base URL -token-file F [-chrome PATH] [-runs N] [-routes a,b]')
  process.exit(3)
}
const token = readFileSync(args['token-file'], 'utf8').trim()
const chromePath = args.chrome || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const runs = Number(args.runs || 3)
const routes = (args.routes || '/,/capacity,/activity,/migration,/evidence,/inventory/hosts,/inventory/policies,/inventory/repositories,/inventory/users,/inventory/security,/inventory/audit').split(',')
if (args.vm) routes.push(`/vms/${args.vm}`)
const shots = args.shots || ''
const width = Number(args.width || 1440)
const READY_MS = 2000

function fail(msg) {
  console.error('UNKNOWN: ' + msg)
  process.exit(3)
}

// --- a minimal DevTools client ---
async function startChrome() {
  const dir = mkdtempSync(join(tmpdir(), 'gaia-ui-gate-'))
  const port = 9300 + Math.floor(Math.random() * 500)
  const p = spawn(chromePath, ['--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${dir}`, '--ignore-certificate-errors',
    `--window-size=${width},900`, '--no-first-run', '--no-default-browser-check', 'about:blank'], { stdio: 'ignore' })
  for (let i = 0; i < 100; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
      const pg = list.find((t) => t.type === 'page')
      if (pg) return { proc: p, dir, ws: pg.webSocketDebuggerUrl }
    } catch { /* not up yet */ }
    await new Promise((r) => setTimeout(r, 100))
  }
  p.kill()
  fail('Chrome did not start: ' + chromePath)
}

function cdp(url) {
  const ws = new WebSocket(url)
  let id = 0
  const pending = new Map()
  const listeners = []
  ws.onmessage = (ev) => {
    const m = JSON.parse(ev.data)
    if (m.id && pending.has(m.id)) {
      const { res, rej } = pending.get(m.id)
      pending.delete(m.id)
      m.error ? rej(new Error(m.error.message)) : res(m.result)
    } else if (m.method) listeners.forEach((l) => l(m))
  }
  const open = new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej })
  return {
    open,
    send: (method, params = {}) => new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })) }),
    on: (f) => listeners.push(f),
    close: () => ws.close(),
  }
}

// --- what runs in the page ---
const pageChecks = `(() => {
  const marks = [...document.querySelectorAll('[data-status]')]
  const bad = []
  for (const m of marks) {
    const text = m.textContent.replace(/[✓▲◆?●]/g, '').trim()
    if (text.length < 2) bad.push({ mark: m.outerHTML.slice(0, 160) })
  }
  const bars = [...document.querySelectorAll('[data-status-bar]')]
  for (const b of bars) {
    const cell = b.closest('td, li, dd, div') || b.parentElement
    const words = (b.getAttribute('aria-label') || '').split(', ').filter((w) => !w.startsWith('0 '))
    const text = cell.textContent
    for (const w of words) if (!text.includes(w)) bad.push({ bar: w, cell: text.slice(0, 120) })
  }
  const tables = [...document.querySelectorAll('table')].map((t) => ({ rows: t.querySelectorAll(':scope > tbody > tr').length }))
  const shown = [...document.body.innerText.matchAll(/Showing ([0-9,]+) of ([0-9,]+)/g)].map((m) => [Number(m[1].replace(/,/g, '')), Number(m[2].replace(/,/g, ''))])
  return { marks: marks.length, bars: bars.length, bad, tables, shown, title: document.querySelector('h1')?.textContent ?? '' }
})()`

// shot writes the page, its whole height, as DIR/<route>.png.
async function shot(c, route) {
  mkdirSync(shots, { recursive: true })
  const m = await c.send('Page.getLayoutMetrics')
  const h = Math.ceil(m.cssContentSize.height)
  await c.send('Emulation.setDeviceMetricsOverride', { width, height: Math.min(h, 16000), deviceScaleFactor: 1, mobile: false })
  const png = await c.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true })
  await c.send('Emulation.clearDeviceMetricsOverride')
  const name = route === '/' ? 'overview' : route.replace(/^\//, '').replace(/\//g, '-')
  writeFileSync(join(shots, `${name}.png`), Buffer.from(png.data, 'base64'))
}

async function waitFor(c, expr, ms) {
  const t0 = Date.now()
  while (Date.now() - t0 < ms) {
    const r = await c.send('Runtime.evaluate', { expression: expr, returnByValue: true })
    if (r.result.value) return r.result.value
    await new Promise((r) => setTimeout(r, 25))
  }
  return null
}

async function main() {
  const ch = await startChrome()
  const c = cdp(ch.ws)
  await c.open
  const requests = []
  c.on((m) => {
    if (m.method === 'Network.requestWillBeSent' && m.params.request.url.includes('/api/v1/')) requests.push(m.params.request.url)
  })
  await c.send('Network.enable')
  await c.send('Page.enable')
  await c.send('Runtime.enable')
  await c.send('Network.setExtraHTTPHeaders', { headers: { Authorization: `Bearer ${token}` } })
  await c.send('Network.setCacheDisabled', { cacheDisabled: false })

  const out = { base, at: new Date().toISOString(), chrome: (await c.send('Browser.getVersion')).product, ready_limit_ms: READY_MS, overview: [], screens: [] }

  // (c) the overview, cold and warm: navigation start until the headline and the VM table's rows are drawn.
  for (let i = 0; i < runs; i++) {
    await c.send('Page.navigate', { url: base + '/' })
    const ms = await waitFor(c, `(() => {
      const nav = performance.getEntriesByType('navigation')[0]
      const rows = document.querySelectorAll('#vms tbody tr').length
      const h1 = document.querySelector('h1')
      if (!nav || !h1 || rows === 0) return 0
      if (!window.__gaiaReady) {
        window.__gaiaReady = performance.now()
      }
      return { ready_ms: Math.round(window.__gaiaReady), rows }
    })()`, 20000)
    out.overview.push(ms ?? { ready_ms: null, error: 'the VM table did not draw within 20 s' })
    if (i === 0) await new Promise((r) => setTimeout(r, 500))
  }

  for (const r of routes) {
    requests.length = 0
    await c.send('Page.navigate', { url: base + r })
    // settled: no new API request for 1.5 s (the screens refresh every 5 to 15 s), at most 20 s
    let last = -1, still = 0
    for (let t = 0; t < 200 && still < 15; t++) {
      await new Promise((res) => setTimeout(res, 100))
      if (requests.length === last) still++
      else { last = requests.length; still = 0 }
    }
    const v = (await c.send('Runtime.evaluate', { expression: pageChecks, returnByValue: true })).result.value
    const lists = requests.map((u) => new URL(u)).filter((u) => u.pathname.startsWith('/api/v1/views/') || u.searchParams.has('limit'))
      .map((u) => ({ path: u.pathname, limit: u.searchParams.get('limit') }))
    if (shots) await shot(c, r)
    out.screens.push({ route: r, ...v, api: requests.map((u) => new URL(u).pathname + new URL(u).search) , unpaged_lists: lists.filter((l) => l.path.startsWith('/api/v1/views/') && l.limit === null && /list|attention|coverage|activity|hosts|repositories|audit|test-queue/.test(l.path)) })
  }
  if (shots) {
    // the sign-in page, as someone without a session sees it
    await c.send('Network.setExtraHTTPHeaders', { headers: {} })
    await c.send('Network.clearBrowserCookies')
    await c.send('Page.navigate', { url: base + '/login' })
    await waitFor(c, `!!document.querySelector('h1')`, 10000)
    await new Promise((res) => setTimeout(res, 800))
    await shot(c, '/login')
  }
  c.close()
  const exited = new Promise((r) => ch.proc.once('exit', r))
  ch.proc.kill()
  await exited
  rmSync(ch.dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 })

  const readys = out.overview.map((o) => o.ready_ms).filter((x) => x !== null)
  const median = readys.length ? [...readys].sort((a, b) => a - b)[Math.floor(readys.length / 2)] : null
  out.checks = {
    c_overview_ready: { median_ms: median, runs: readys, pass: median !== null && median <= READY_MS && readys.length === runs },
    d_status_words: { marks: out.screens.reduce((n, s) => n + s.marks + s.bars, 0), violations: out.screens.flatMap((s) => s.bad.map((b) => ({ route: s.route, ...b }))),
      pass: out.screens.every((s) => s.bad.length === 0) && out.screens.some((s) => s.marks > 0) },
    lists_paged: { screens: out.screens.map((s) => ({ route: s.route, rows: s.tables.map((t) => t.rows), shown: s.shown, unpaged: s.unpaged_lists })),
      pass: out.screens.every((s) => s.unpaged_lists.length === 0 && s.shown.every(([n, total]) => n <= 200 && n <= total)) },
  }
  out.verdict = Object.values(out.checks).every((x) => x.pass) ? 'OK' : 'CRITICAL'
  console.log(JSON.stringify(out, null, 2))
  process.exit(out.verdict === 'OK' ? 0 : 2)
}

main().catch((e) => fail(e.stack || String(e)))
