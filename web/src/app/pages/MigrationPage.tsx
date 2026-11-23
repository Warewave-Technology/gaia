import { useState } from 'react'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { serverTime, type Fmt } from '../../lib/time'
import { useFilters } from '../../lib/url'
import type { Pages } from '../api/pages'
import { can, useMe } from '../api/session'
import {
  useMigrationPages, useMigrationVMChecks, useQueueMigrationCheck, useSourceInventories, useSourceInventory,
  type MigrationCheckView, type MigrationPageData, type MigrationResult, type MigrationSource, type MigrationView, type SourceVM,
} from '../api/views'
import { Failed, Loading } from '../layout/States'
import { download, useStepUp } from '../layout/StepUp'
import { Button, DataTable, Fact, Facts, FilterChip, Notice, PageHead, SectionHead, StatusChip, TechDetails, Toolbar, by, type Column } from '../ui'
import s from './MigrationPage.module.css'

type T = ReturnType<typeof useT>['migration']

/** The selected VM: its newest check (if any), its earlier ones, and what the source says of it (once read). */
type Row = { key: string; source: MigrationSource | undefined; sourceID: number; vm: string; check?: MigrationCheckView; earlier: MigrationCheckView[]; inv?: SourceVM }

/** A VM of a source as the source listed it (read with read-only calls). */
type SourceRow = { key: string; sourceID: number; source: MigrationSource | undefined; vm: SourceVM }

type Rank = 'notReady' | 'warnings' | 'undecided' | 'inProgress' | 'ready' | 'notChecked'
type Checked = Exclude<Rank, 'notChecked'>
const ORDER: Checked[] = ['notReady', 'warnings', 'undecided', 'inProgress', 'ready']
/** The server's result for each filter chip: the list is filtered and counted on the server (ADR-007 C18.7). */
const RESULT: Record<Checked, MigrationResult> = { notReady: 'CRITICAL', warnings: 'WARNING', undecided: 'UNKNOWN', inProgress: 'open', ready: 'OK' }

/** A check's result: status, the word for it, and its group (worst first). */
function resultOf(c: MigrationCheckView | undefined, t: T): { status: string; word: string; rank: Rank } {
  if (!c) return { status: 'UNKNOWN', word: t.word.notChecked, rank: 'notChecked' }
  if (!c.readiness) return { status: 'UNKNOWN', word: c.job_state === 'running' ? t.word.running : t.word.queued, rank: 'inProgress' }
  if (c.readiness === 'CRITICAL') return { status: 'CRITICAL', word: t.word.notReady, rank: 'notReady' }
  if (c.readiness === 'WARNING') return { status: 'WARNING', word: t.word.warnings, rank: 'warnings' }
  if (c.readiness === 'OK') return { status: 'OK', word: t.word.ready, rank: 'ready' }
  return { status: 'UNKNOWN', word: t.word.undecided, rank: 'undecided' }
}

const KEYS = ['source', 'vm', 'result'] as const

/**
 * The migration check (docs/design/v2/Migration.dc.html with the v3 rules; ADR-003, ADR-005): will a VM on vSphere run
 * on KVM before it is moved, proven by converting a copy and booting it in the sandbox. The source is only ever read.
 * The checked VMs are a data-table the server pages (each VM's newest check, worst first; ADR-007 C18.7) with the
 * results as filter chips counted over the whole list; a source's VMs, once read, are a second table; the selected
 * VM's findings are sentences, their raw output under "Technical details".
 */
export function MigrationPage() {
  const [f] = useFilters(KEYS)
  const rank = (ORDER as readonly string[]).includes(f.result) ? (f.result as Checked) : null
  const pages = useMigrationPages(rank ? RESULT[rank] : '')
  if (pages.isPending) return <Loading />
  if (pages.isError) return <Failed error={pages.error} onRetry={() => pages.refetch()} />
  return <MigrationBody v={(pages.data as MigrationPageData).view} pages={pages} rank={rank} />
}

function MigrationBody({ v, pages, rank }: { v: MigrationView; pages: Pages<MigrationCheckView>; rank: Checked | null }) {
  const t = useT().migration
  const me = useMe().data
  const act = can(me, 'act')
  const fmt = serverTime(v.clock)
  const [f, setF] = useFilters(KEYS)
  const [read, setRead] = useState<readonly number[]>([])
  const invs = useSourceInventories(read)
  const byID = new Map(v.sources.map((x) => [x.id, x]))
  const counts: Record<string, number> = v.by_result ?? {}
  const all = Object.values(counts).reduce((a, b) => a + b, 0)
  const count = (k: Checked) => counts[RESULT[k]] ?? 0
  const listed: SourceRow[] = [...invs].flatMap(([sid, inv]) =>
    inv.vms.filter((x) => !x.template).map((vm) => ({ key: `${sid}/${vm.name}`, sourceID: sid, source: byID.get(sid), vm })))
  // The selected VM: the one in the address, else the first of the page.
  const first = pages.rows[0]
  const selSource = f.source && f.vm ? Number(f.source) : first?.source_id
  const selVM = f.source && f.vm ? f.vm : first?.vm
  const selKey = selSource !== undefined && selVM ? `${selSource}/${selVM}` : ''
  const pick = (sourceID: number, vm: string) => setF({ source: String(sourceID), vm })
  return (
    <main className={s.page}>
      <PageHead
        question={t.question}
        answer={t.title}
        lede={<Sources v={v} />}
        aside={<Facts label={t.facts}><Converter v={v} /></Facts>}
      />
      {act && v.sources.length > 0 && (
        <div className={s.read}>
          {v.sources.map((src) => (
            <ReadButton key={src.id} src={src} on={read.includes(src.id)} onRead={() => setRead([...read, src.id])} />
          ))}
          <span className={s.of}>{t.readHint}</span>
        </div>
      )}
      {all === 0 && listed.length === 0 ? (
        v.sources.length > 0 && <p className={s.muted}>{act ? t.noChecksAct : t.noChecks}</p>
      ) : (
        <>
          {all > 0 && (
            <Toolbar end={t.order}>
              <FilterChip set={!rank} onClick={() => setF({ result: '' })}>{t.f.all(all)}</FilterChip>
              {ORDER.filter((k) => count(k) > 0 || k === 'ready' || k === 'notReady' || k === 'warnings').map((k) => (
                <FilterChip key={k} set={rank === k} onClick={() => setF({ result: rank === k ? '' : k })}>{t.f.rank(t.word[k], count(k))}</FilterChip>
              ))}
            </Toolbar>
          )}
          <section className={s.two}>
            <div className={s.lists}>
              {all > 0 && <VMTable pages={pages} sel={selKey} fmt={fmt} onPick={(c) => pick(c.source_id, c.vm)} />}
              {listed.length > 0 && <SourceTable rows={listed} sel={selKey} onPick={(r) => pick(r.sourceID, r.vm.name)} />}
            </div>
            {selSource !== undefined && selVM && (
              <SelectedVM key={selKey} sourceID={selSource} vm={selVM} source={byID.get(selSource)}
                inv={invs.get(selSource)?.vms.find((x) => x.name === selVM)} fmt={fmt} act={act} />
            )}
          </section>
        </>
      )}
    </main>
  )
}

function ReadButton({ src, on, onRead }: { src: MigrationSource; on: boolean; onRead: () => void }) {
  const t = useT().migration
  const q = useSourceInventory(src.id, on)
  if (!on) return <Button variant="quiet" onClick={onRead}>{t.readVMs(src.name)}</Button>
  if (q.isPending) return <span className={s.muted}>{t.reading(src.name)}</span>
  if (q.isError) return <span className={s.err}><Notice tone="critical">{t.readFailed(src.name)}</Notice><TechDetails>{q.error.message}</TechDetails></span>
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
  let raw = ''
  if (!c.reachable || !info) {
    status = 'CRITICAL'
    line = t.convDown
    raw = c.detail
  } else if (!info.image_present) {
    status = 'CRITICAL'
    line = t.convNoImage
    raw = info.image_detail
  } else {
    const v2v = info.versions.split('\n').find((l) => l.startsWith('virt-v2v')) ?? 'virt-v2v'
    line = t.convReady(v2v, shortID(info.image))
  }
  const vw = info?.virtio_win
  const vwOK = vw?.state === 'ok'
  return (
    <>
      <Fact term={t.converter}><StatusChip status={status} size="xs" /> {line}<TechDetails>{raw}</TechDetails></Fact>
      {vw && (
        <Fact term={t.virtioWin}>
          <StatusChip status={vwOK ? 'OK' : 'WARNING'} size="xs" /> {vwOK ? t.virtioOk(vw.version) : t.virtioMissing(vw.version)}
          {!vwOK && <TechDetails>{vw.detail}</TechDetails>}
        </Fact>
      )}
    </>
  )
}

const shortID = (id: string) => id.replace(/^sha256:/, '').slice(0, 12)

function checkedAt(c: MigrationCheckView | undefined, fmt: Fmt, t: T) {
  if (!c) return t.never
  if (c.finished_at) return fmt.at(c.finished_at)
  return c.started_at ? t.runningSince(fmt.at(c.started_at)) : t.queuedAt(fmt.at(c.created_at))
}

function disks(inv: SourceVM, t: T) {
  return t.disks(inv.disks, inv.disk_bytes / 2 ** 30)
}

/** The checked VMs, worst first then by name: each VM's newest check, a page at a time from the server (ADR-007
 * C18.7; the server orders them, so the columns do not sort). */
function VMTable({ pages, sel, fmt, onPick }: { pages: Pages<MigrationCheckView>; sel: string; fmt: Fmt; onPick: (c: MigrationCheckView) => void }) {
  const t = useT().migration
  const key = (c: MigrationCheckView) => `${c.source_id}/${c.vm}`
  const cols: Column<MigrationCheckView>[] = [
    { key: 'vm', header: t.col.vm, width: '36%', title: (c) => c.vm, cell: (c) => (
      <button type="button" className={s.pick} aria-pressed={key(c) === sel} onClick={() => onPick(c)}>{c.vm}</button>
    ) },
    { key: 'guest', header: t.col.guest, title: (c) => c.guest_id || undefined, cell: (c) => c.guest_id || '—' },
    { key: 'result', header: t.col.result, width: '180px', cell: (c) => {
      const res = resultOf(c, t)
      return <StatusChip status={res.status} size="xs" word={res.word} />
    } },
    { key: 'checked', header: t.col.checked, width: '140px', cell: (c) => checkedAt(c, fmt, t) },
  ]
  return (
    <DataTable caption={t.vmsCaption} columns={cols} pages={pages} rowKey={key} empty={t.noneMatch}
      rowState={(c) => (key(c) === sel ? 'mark' : resultOf(c, t).status === 'CRITICAL' ? 'critical' : undefined)} />
  )
}

/** The VMs of the sources read just now (one read-only listing each; the table pages them). Picking one shows its
 * checks, or what the source says of it when it was never checked. */
function SourceTable({ rows, sel, onPick }: { rows: SourceRow[]; sel: string; onPick: (r: SourceRow) => void }) {
  const t = useT().migration
  const cols: Column<SourceRow>[] = [
    { key: 'vm', header: t.col.vm, width: '36%', title: (r) => r.vm.name, compare: by((r) => r.vm.name), cell: (r) => (
      <button type="button" className={s.pick} aria-pressed={r.key === sel} onClick={() => onPick(r)}>{r.vm.name}</button>
    ) },
    { key: 'guest', header: t.col.guest, title: (r) => r.vm.guest_full_name || undefined, compare: by((r) => r.vm.guest_full_name), cell: (r) => r.vm.guest_full_name || '—' },
    { key: 'power', header: t.col.power, width: '90px', compare: by((r) => r.vm.power_state), cell: (r) => r.vm.power_state.replace(/^powered/, '').toLowerCase() },
    { key: 'size', header: t.col.size, width: '150px', compare: by((r) => r.vm.disk_bytes), cell: (r) => disks(r.vm, t) },
  ]
  return (
    <DataTable caption={t.sourceCaption} columns={cols} rows={rows} rowKey={(r) => r.key} empty={t.noneMatch}
      rowState={(r) => (r.key === sel ? 'mark' : undefined)} />
  )
}

/** The selected VM with its checks, newest first: the first is its result, the others its history. */
function SelectedVM({ sourceID, vm, source, inv, fmt, act }: {
  sourceID: number; vm: string; source: MigrationSource | undefined; inv: SourceVM | undefined; fmt: Fmt; act: boolean
}) {
  const q = useMigrationVMChecks(sourceID, vm)
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const [check, ...earlier] = q.data.checks
  if (!check && !inv) return null
  return <Selected r={{ key: `${sourceID}/${vm}`, source, sourceID, vm, check, earlier, inv }} fmt={fmt} act={act} />
}

/** The steps a check takes; each is a check of its own in the result (migrate.go). */
const STEPS = ['download', 'conversion', 'boot'] as const
const PRE = new Set(['source_vm', 'cold', 'layout', 'download', 'conversion', 'firmware', 'boot'])
const RANK: Record<string, number> = { CRITICAL: 0, UNKNOWN: 1, WARNING: 2, OK: 3 }

/** The selected VM: the verdict in a sentence, the four steps, the findings, and what can be done with it. */
function Selected({ r, fmt, act }: { r: Row; fmt: Fmt; act: boolean }) {
  const t = useT().migration
  const res = resultOf(r.check, t)
  const c = r.check
  const d = c?.result ?? null
  const checks = d?.checks ?? []
  const step = (name: string) => checks.find((k) => k.name === name)
  const after = checks.filter((k) => !PRE.has(k.name))
  const verdict = !c ? t.verdict.none : !c.readiness ? (c.job_state === 'running' ? t.verdict.running : t.verdict.queued)
    : c.readiness === 'OK' ? t.verdict.ok : c.readiness === 'WARNING' ? t.verdict.warning(checks.filter((k) => k.verdict === 'WARNING').length)
      : c.readiness === 'CRITICAL' ? t.verdict.critical : t.verdict.unknown
  const findings = [...checks].sort((a, b) => (RANK[a.verdict] ?? 1) - (RANK[b.verdict] ?? 1))
  const raw = [c?.reason, ...(d?.errors ?? [])].filter(Boolean).join('\n')
  return (
    <section className={s.sel} aria-labelledby="sel-vm">
      <SectionHead id="sel-vm" title={r.vm}>
        <StatusChip status={res.status} size="sm" word={res.word} />
        <span className={s.of}>{c?.finished_at ? t.checkedAt(fmt.at(c.finished_at), c.id) : r.source?.name}</span>
      </SectionHead>
      <p className={s.verdict}>{verdict}</p>
      {d && d.errors.length > 0 && <Notice tone="critical">{t.errors(d.errors.length)}</Notice>}
      {raw && <TechDetails summary={t.why}>{raw}</TechDetails>}
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
            <span className={s.of}>{after.length > 0 ? t.afterBoot : t.notRun}</span>
          </li>
        </ol>
      )}
      {findings.length > 0 && (
        <>
          <Findings list={findings.filter((k) => k.verdict !== 'OK')} label={t.findings} />
          {findings.some((k) => k.verdict === 'OK') && (
            <details className={s.passed}>
              <summary>{t.passedChecks(findings.filter((k) => k.verdict === 'OK').length)}</summary>
              <Findings list={findings.filter((k) => k.verdict === 'OK')} label={t.passedLabel} />
            </details>
          )}
        </>
      )}
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
        <TechDetails summary={t.converterUsed}>
          {[
            `${t.image}: ${d.converter.image}`,
            d.converter.versions ? `${t.versions}:\n${d.converter.versions}` : '',
            `${t.virtioWin}: ${d.converter.virtio_win ? `${d.converter.virtio_win.version}, sha256 ${d.converter.virtio_win.sha256}` : t.noVirtioWin}`,
          ].filter(Boolean).join('\n')}
        </TechDetails>
      )}
      <Actions r={r} act={act} />
      {r.earlier.length > 0 && (
        <p className={s.of}>
          {t.earlier}{' '}
          {r.earlier.slice(0, 6).map((e) => (
            <span key={e.id} className={s.earlierItem}>
              {t.checkN(e.id)} <StatusChip status={e.readiness || 'UNKNOWN'} size="xs" word={e.readiness ? undefined : t.word.unfinished} />
              {e.finished_at ? ` ${fmt.at(e.finished_at)}` : ''}
            </span>
          ))}
        </p>
      )}
    </section>
  )
}

type Check = NonNullable<MigrationCheckView['result']>['checks'][number]

/** Findings as sentences, each with its check's raw output folded under it (docs/design/v3 rule 3). */
function Findings({ list, label }: { list: Check[]; label: string }) {
  const t = useT().migration
  if (list.length === 0) return null
  return (
    <ul className={s.findings} aria-label={label}>
      {list.map((k) => (
        <li key={k.name}>
          <StatusChip status={k.verdict} size="xs" />
          <span className={s.finding}>
            <span>{t.finding(k.name, k.verdict)}</span>
            <TechDetails summary={t.output}>{k.detail}</TechDetails>
          </span>
          <span className={s.secs}>{k.seconds ? dur(k.seconds, true) : ''}</span>
        </li>
      ))}
    </ul>
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
        <dd>
          {inv.checkable ? (
            <span className={s.finding}>
              <span><StatusChip status="UNKNOWN" size="xs" word={t.word.cannotCheck} />{/powered ?on/i.test(inv.checkable) && <> {t.finding('cold', 'UNKNOWN')}</>}</span>
              <TechDetails summary={t.why}>{inv.checkable}</TechDetails>
            </span>
          ) : t.checkable}
        </dd>
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
  // A driver left out stays left out when the VM is checked again; one that loops at first boot is offered (ADR-005 C)
  const kept = c?.result?.converter?.excluded_drivers ?? []
  const looping = c?.result?.code === 'driver_install_loop' ? (c.result.drivers ?? []).filter((d) => !kept.includes(d)) : []
  return (
    <div className={s.acts}>
      {c?.bundle_id && (
        <Button variant="quiet" onClick={() => {
          setErr(null)
          download(ask, `/api/v1/migration-checks/${c.id}/report`, `gaia-readiness-${c.id}.tar.gz`,
            { facts: [{ label: t.reportOf, value: t.reportTarget(r.vm, c.id) }] }).catch((e: Error) => setErr(e.message))
        }}>
          {t.report}
        </Button>
      )}
      {act && !busy && checkable && (
        <Button onClick={() => queue.mutate({ vm: r.vm, exclude: kept })} disabled={queue.isPending}>
          {c ? (kept.length ? t.checkAgainWithout(kept) : t.checkAgain) : t.check}
        </Button>
      )}
      {act && !busy && checkable && looping.length > 0 && (
        <Button variant="quiet" onClick={() => queue.mutate({ vm: r.vm, exclude: [...kept, ...looping] })} disabled={queue.isPending}>
          {t.checkAgainWithout([...kept, ...looping])}
        </Button>
      )}
      {queue.data && <span role="status" className={s.muted}>{t.queued(queue.data.vm, queue.data.id)}</span>}
      {queue.error && <Notice tone="critical">{queue.error.message}</Notice>}
      {err && <Notice tone="critical">{err}</Notice>}
    </div>
  )
}
