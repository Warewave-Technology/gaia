import { useState } from 'react'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { serverTime, type Fmt, type ServerClock } from '../../lib/time'
import { useFilters } from '../../lib/url'
import { can, useMe } from '../api/session'
import {
  useMigrationView, useQueueMigrationCheck, useSourceInventories, useSourceInventory,
  type MigrationCheckView, type MigrationSource, type MigrationView, type SourceVM,
} from '../api/views'
import { Failed, Loading } from '../layout/States'
import { download, useStepUp } from '../layout/StepUp'
import { Button, Fact, Facts, Notice, PageHead, SectionHead, StatusChip } from '../ui'
import s from './MigrationPage.module.css'

type T = ReturnType<typeof useT>['migration']

/** One VM of a source: its newest check (if any), its earlier ones, and what the source says of it (once read). */
type Row = { key: string; source: MigrationSource | undefined; sourceID: number; vm: string; check?: MigrationCheckView; earlier: MigrationCheckView[]; inv?: SourceVM }

/** A row's result: status, the word for it, and its place in the worst-first order. */
function resultOf(r: Row, t: T): { status: string; word: string; rank: number } {
  const c = r.check
  if (!c) return { status: 'UNKNOWN', word: t.word.notChecked, rank: 5 }
  if (!c.readiness) return { status: 'UNKNOWN', word: c.job_state === 'running' ? t.word.running : t.word.queued, rank: 3 }
  if (c.readiness === 'CRITICAL') return { status: 'CRITICAL', word: t.word.notReady, rank: 0 }
  if (c.readiness === 'WARNING') return { status: 'WARNING', word: t.word.warnings, rank: 1 }
  if (c.readiness === 'OK') return { status: 'OK', word: t.word.ready, rank: 4 }
  return { status: 'UNKNOWN', word: t.word.undecided, rank: 2 }
}

/**
 * The migration check (docs/design/v2/Migration.dc.html; ADR-003, ADR-005): will a VM on vSphere run on KVM before it
 * is moved, proven by converting a copy and booting it in the sandbox. The source is only ever read.
 */
export function MigrationPage() {
  const q = useMigrationView()
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  return <MigrationBody v={q.data} />
}

const KEYS = ['source', 'vm'] as const

function MigrationBody({ v }: { v: MigrationView }) {
  const t = useT().migration
  const me = useMe().data
  const act = can(me, 'act')
  const fmt = serverTime(v.clock)
  const [f, setF] = useFilters(KEYS)
  const [read, setRead] = useState<readonly number[]>([])
  const invs = useSourceInventories(read)
  const byID = new Map(v.sources.map((x) => [x.id, x]))
  const rows = new Map<string, Row>()
  // checks come newest first: the first of a VM is its result, the others its history
  for (const c of v.checks) {
    const key = `${c.source_id}/${c.vm}`
    const r = rows.get(key)
    if (r) r.earlier.push(c)
    else rows.set(key, { key, source: byID.get(c.source_id), sourceID: c.source_id, vm: c.vm, check: c, earlier: [] })
  }
  for (const [sid, inv] of invs) {
    for (const vm of inv.vms) {
      if (vm.template) continue
      const key = `${sid}/${vm.name}`
      const r = rows.get(key)
      if (r) r.inv = vm
      else rows.set(key, { key, source: byID.get(sid), sourceID: sid, vm: vm.name, earlier: [], inv: vm })
    }
  }
  const list = [...rows.values()].sort((a, b) => resultOf(a, t).rank - resultOf(b, t).rank || a.vm.localeCompare(b.vm))
  const sel = list.find((r) => String(r.sourceID) === f.source && r.vm === f.vm) ?? list[0]
  return (
    <main className={s.page}>
      <PageHead
        question={t.question}
        answer={t.title}
        lede={<Sources v={v} />}
        aside={<Facts label={t.facts}><Converter v={v} /></Facts>}
      />
      {list.length > 0 && <Tally rows={list} read={invs.size > 0} />}
      {act && v.sources.length > 0 && (
        <div className={s.read}>
          {v.sources.map((src) => (
            <ReadButton key={src.id} src={src} on={read.includes(src.id)} onRead={() => setRead([...read, src.id])} />
          ))}
          <span className={s.of}>{t.readHint}</span>
        </div>
      )}
      {list.length === 0 ? (
        v.sources.length > 0 && <p className={s.muted}>{act ? t.noChecksAct : t.noChecks}</p>
      ) : (
        <section className={s.two}>
          <VMTable rows={list} sel={sel} fmt={fmt} clock={v.clock} onPick={(r) => setF({ source: String(r.sourceID), vm: r.vm })} />
          {sel && <Selected key={sel.key} r={sel} fmt={fmt} clock={v.clock} act={act} />}
        </section>
      )}
    </main>
  )
}

function ReadButton({ src, on, onRead }: { src: MigrationSource; on: boolean; onRead: () => void }) {
  const t = useT().migration
  const q = useSourceInventory(src.id, on)
  if (!on) return <Button variant="quiet" onClick={onRead}>{t.readVMs(src.name)}</Button>
  if (q.isPending) return <span className={s.muted}>{t.reading(src.name)}</span>
  if (q.isError) return <Notice tone="critical">{t.readFailed(src.name, q.error.message)}</Notice>
  return <span className={s.of}>{t.read(src.name, q.data.vms.length, q.data.calls, q.data.blocked_calls)}</span>
}

/** The sources, and what Gaia does with them: it reads, it never writes. */
function Sources({ v }: { v: MigrationView }) {
  const t = useT().migration
  if (v.sources.length === 0) return <p>{t.noSources}</p>
  return (
    <>
      {v.sources.map((src) => (
        <p key={src.id}>
          {t.source} <span className={s.mono}>{src.name}</span> ({hostOf(src.url)}, {t.user} <span className={s.mono}>{src.username}</span>,{' '}
          {src.insecure ? t.tlsNot : t.tlsVerified}).
        </p>
      ))}
      <p>{t.readOnly}</p>
    </>
  )
}

function hostOf(url: string) {
  try {
    return new URL(url).host
  } catch {
    return url
  }
}

/** The converter's state: Linux guests need the image, Windows guests the image and virtio-win. */
function Converter({ v }: { v: MigrationView }) {
  const t = useT().migration
  const c = v.converter
  const info = c.info
  let status = 'OK'
  let line: string
  if (!c.reachable || !info) {
    status = 'CRITICAL'
    line = t.convDown(c.detail)
  } else if (!info.image_present) {
    status = 'CRITICAL'
    line = t.convNoImage(info.image_detail)
  } else {
    const v2v = info.versions.split('\n').find((l) => l.startsWith('virt-v2v')) ?? 'virt-v2v'
    line = t.convReady(v2v, shortID(info.image))
  }
  const vw = info?.virtio_win
  const vwOK = vw?.state === 'ok'
  return (
    <>
      <Fact term={t.converter}><StatusChip status={status} size="xs" />{line}</Fact>
      {vw && (
        <Fact term={t.virtioWin}>
          <StatusChip status={vwOK ? 'OK' : 'WARNING'} size="xs" />
          {vwOK ? t.virtioOk(vw.version) : t.virtioMissing(vw.version, vw.detail)}
        </Fact>
      )}
    </>
  )
}

const shortID = (id: string) => id.replace(/^sha256:/, '').slice(0, 12)

/** Ready, ready with warnings, not ready, not checked: each with its count. */
function Tally({ rows, read }: { rows: Row[]; read: boolean }) {
  const t = useT().migration
  const n = (rank: number) => rows.filter((r) => resultOf(r, t).rank === rank).length
  const items: [string, string, number][] = [
    ['OK', t.word.ready, n(4)], ['WARNING', t.word.warnings, n(1)], ['CRITICAL', t.word.notReady, n(0)],
  ]
  if (n(2) > 0) items.push(['UNKNOWN', t.word.undecided, n(2)])
  if (n(3) > 0) items.push(['UNKNOWN', t.word.inProgress, n(3)])
  return (
    <ul className={s.tally} aria-label={t.tally}>
      {items.map(([st, word, c]) => <li key={word}><StatusChip status={st} size="sm" word={word} /><b>{c.toLocaleString('en-US')}</b></li>)}
      {read && <li><StatusChip status="UNKNOWN" size="sm" word={t.word.notChecked} /><b>{n(5).toLocaleString('en-US')}</b><span className={s.of}>{t.ofVMs(rows.length)}</span></li>}
    </ul>
  )
}

/** "Today 12:20" or "Nov 17 12:20". */
function when(fmt: Fmt, clock: ServerClock, iso: string, t: T) {
  return fmt.date(iso) === fmt.date(clock.now) ? t.today(fmt.hhmm(iso)) : `${fmt.date(iso)} ${fmt.hhmm(iso)}`
}

function checkedAt(r: Row, fmt: Fmt, clock: ServerClock, t: T) {
  const c = r.check
  if (!c) return t.never
  if (c.finished_at) return when(fmt, clock, c.finished_at, t)
  return c.started_at ? t.runningSince(fmt.hhmm(c.started_at)) : t.queuedAt(fmt.hhmm(c.created_at))
}

function disks(inv: SourceVM, t: T) {
  return t.disks(inv.disks, inv.disk_bytes / 2 ** 30)
}

function VMTable({ rows, sel, fmt, clock, onPick }: { rows: Row[]; sel: Row | undefined; fmt: Fmt; clock: ServerClock; onPick: (r: Row) => void }) {
  const t = useT().migration
  return (
    <div className={s.scroll}>
      <table className={s.tbl}>
        <caption className={s.vh}>{t.vmsCaption}</caption>
        <thead>
          <tr>
            <th scope="col">{t.col.vm}</th>
            <th scope="col">{t.col.guest}</th>
            <th scope="col">{t.col.result}</th>
            <th scope="col">{t.col.checked}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => {
            const res = resultOf(r, t)
            return (
              <tr key={r.key} className={r === sel ? s.rsel : ''}>
                <th scope="row">
                  <button type="button" className={s.pick} aria-pressed={r === sel} onClick={() => onPick(r)}>{r.vm}</button>
                  <span className={s.of}>{r.inv ? disks(r.inv, t) : r.source?.name ?? r.check?.source}</span>
                </th>
                <td className={s.fg2}>{r.inv?.guest_full_name || r.check?.guest_id || '—'}</td>
                <td><StatusChip status={res.status} size="xs" word={res.word} /></td>
                <td className={s.muted}>{checkedAt(r, fmt, clock, t)}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

/** The steps a check takes; each is a check of its own in the result (migrate.go). */
const STEPS = ['download', 'conversion', 'boot'] as const
const PRE = new Set(['source_vm', 'cold', 'layout', 'download', 'conversion', 'firmware', 'boot'])
const RANK: Record<string, number> = { CRITICAL: 0, UNKNOWN: 1, WARNING: 2, OK: 3 }

/** The selected VM: the verdict in a sentence, the four steps, the findings, and what can be done with it. */
function Selected({ r, fmt, clock, act }: { r: Row; fmt: Fmt; clock: ServerClock; act: boolean }) {
  const t = useT().migration
  const res = resultOf(r, t)
  const c = r.check
  const d = c?.result ?? null
  const checks = d?.checks ?? []
  const step = (name: string) => checks.find((k) => k.name === name)
  const after = checks.filter((k) => !PRE.has(k.name))
  const verdict = !c ? t.verdict.none : !c.readiness ? (c.job_state === 'running' ? t.verdict.running : t.verdict.queued)
    : c.readiness === 'OK' ? t.verdict.ok : c.readiness === 'WARNING' ? t.verdict.warning(checks.filter((k) => k.verdict === 'WARNING').length)
      : c.readiness === 'CRITICAL' ? t.verdict.critical : t.verdict.unknown
  const findings = [...checks].sort((a, b) => (RANK[a.verdict] ?? 1) - (RANK[b.verdict] ?? 1))
  return (
    <section className={s.sel} aria-labelledby="sel-vm">
      <SectionHead id="sel-vm" title={r.vm}>
        <StatusChip status={res.status} size="sm" word={res.word} />
        <span className={s.of}>{c?.finished_at ? t.checkedAt(when(fmt, clock, c.finished_at, t), c.id) : r.source?.name}</span>
      </SectionHead>
      <p className={s.verdict}>{verdict}</p>
      {c?.reason && <p className={s.of}>{c.reason}</p>}
      {d && (
        <ol className={s.flow} aria-label={t.steps}>
          {STEPS.map((name, i) => {
            const k = step(name)
            return (
              <li key={name}>
                <span className={s.of}>{i + 1} · {t.step[name]}</span>
                <span className={s.fv}>{k ? <StatusChip status={k.verdict} size="xs" /> : null}<b>{k ? dur(k.seconds, true) : '—'}</b></span>
                <span className={s.of}>{t.stepSub[name]}</span>
              </li>
            )
          })}
          <li>
            <span className={s.of}>4 · {t.step.checks}</span>
            <span className={s.fv}>
              {after.length > 0 && <StatusChip status={after.every((k) => k.verdict === 'OK') ? 'OK' : after.some((k) => k.verdict === 'CRITICAL') ? 'CRITICAL' : 'WARNING'} size="xs" />}
              <b>{after.length > 0 ? t.passedOf(after.filter((k) => k.verdict === 'OK').length, after.length) : '—'}</b>
            </span>
            <span className={s.of}>{after.length > 0 ? after.map((k) => k.name.replace(/_/g, ' ')).join(', ') : t.notRun}</span>
          </li>
        </ol>
      )}
      {findings.length > 0 && (
        <ul className={s.findings} aria-label={t.findings}>
          {findings.map((k) => (
            <li key={k.name}>
              <StatusChip status={k.verdict} size="xs" />
              <span><b>{k.name.replace(/_/g, ' ')}</b><span className={s.of}>{k.detail}</span></span>
              <span className={s.secs}>{k.seconds ? dur(k.seconds, true) : ''}</span>
            </li>
          ))}
        </ul>
      )}
      {d && d.errors.length > 0 && d.errors.map((e, i) => <Notice key={i} tone="critical">{e}</Notice>)}
      {d && <p className={d.vsphere_blocked_calls ? s.bad : s.of}>{t.calls(d.vsphere_calls, d.vsphere_blocked_calls)}</p>}
      {!c && r.inv && <SourceFacts inv={r.inv} />}
      {c && c.screenshots.length > 0 && c.evidence_id > 0 && (
        <div className={s.shots}>
          {c.screenshots.map((n) => (
            <figure key={n}>
              <img src={`/api/v1/evidence/${c.evidence_id}/files/${encodeURIComponent(n)}`} width={720} height={450} alt={t.shot(r.vm, n)} loading="lazy" />
              <figcaption>{n.replace(/^screenshot-|\.png$/g, '')}</figcaption>
            </figure>
          ))}
        </div>
      )}
      {d?.converter && (
        <details className={s.conv}>
          <summary>{t.converterUsed}</summary>
          <dl>
            <div><dt>{t.image}</dt><dd className={s.mono}>{d.converter.image}</dd></div>
            {d.converter.versions && <div><dt>{t.versions}</dt><dd className={s.pre}>{d.converter.versions}</dd></div>}
            <div><dt>{t.virtioWin}</dt><dd>{d.converter.virtio_win ? `${d.converter.virtio_win.version}, sha256 ${d.converter.virtio_win.sha256}` : t.noVirtioWin}</dd></div>
          </dl>
        </details>
      )}
      <Actions r={r} act={act} />
      {r.earlier.length > 0 && (
        <p className={s.of}>
          {t.earlier}{' '}
          {r.earlier.slice(0, 6).map((e) => (
            <span key={e.id} className={s.earlier}>
              {t.checkN(e.id)} <StatusChip status={e.readiness || 'UNKNOWN'} size="xs" word={e.readiness ? undefined : t.word.unfinished} />
              {e.finished_at ? ` ${when(fmt, clock, e.finished_at, t)}` : ''}
            </span>
          ))}
        </p>
      )}
    </section>
  )
}

/** What the source says of a VM not checked yet (read with read-only calls). */
function SourceFacts({ inv }: { inv: SourceVM }) {
  const t = useT().migration
  return (
    <dl className={s.facts}>
      <div><dt>{t.col.power}</dt><dd>{inv.power_state.replace(/^powered/, '').toLowerCase()}</dd></div>
      <div><dt>{t.col.firmware}</dt><dd>{t.firmware(inv.firmware)}{inv.secure_boot && `, ${t.secureBoot}`}</dd></div>
      <div><dt>{t.col.size}</dt><dd>{disks(inv, t)}, {t.cpus(inv.cpus, inv.memory_mib)}</dd></div>
      <div>
        <dt>{t.col.state}</dt>
        <dd>{inv.checkable ? <><StatusChip status="UNKNOWN" size="xs" word={t.word.cannotCheck} />{inv.checkable}</> : t.checkable}</dd>
      </div>
    </dl>
  )
}

/** The readiness report (a step-up download), and checking the VM (again). */
function Actions({ r, act }: { r: Row; act: boolean }) {
  const t = useT().migration
  const ask = useStepUp()
  const queue = useQueueMigrationCheck(r.sourceID)
  const [err, setErr] = useState<string | null>(null)
  const c = r.check
  const busy = c !== undefined && !c.readiness
  const checkable = !r.inv || r.inv.checkable === ''
  return (
    <div className={s.acts}>
      {c?.bundle_id && (
        <Button variant="quiet" onClick={() => { setErr(null); download(ask, `/api/v1/migration-checks/${c.id}/report`, `gaia-readiness-${c.id}.tar.gz`).catch((e: Error) => setErr(e.message)) }}>
          {t.report}
        </Button>
      )}
      {act && !busy && checkable && (
        <Button onClick={() => queue.mutate(r.vm)} disabled={queue.isPending}>{c ? t.checkAgain : t.check}</Button>
      )}
      {queue.data && <span role="status" className={s.muted}>{t.queued(queue.data.vm, queue.data.id)}</span>}
      {queue.error && <Notice tone="critical">{queue.error.message}</Notice>}
      {err && <Notice tone="critical">{err}</Notice>}
    </div>
  )
}
