import { useState } from 'react'
import { useT } from '../../i18n'
import { dur } from '../../lib/duration'
import { serverTime, type Fmt } from '../../lib/time'
import { can, useMe } from '../api/session'
import {
  useMigrationView,
  useQueueMigrationCheck,
  useSourceInventory,
  type MigrationCheckView,
  type MigrationSource,
  type MigrationView,
  type SourceVM,
} from '../api/views'
import { Failed, Loading } from '../layout/States'
import { download, useStepUp } from '../layout/StepUp'
import { Button, DataTable, Notice, RowToggle, StatusChip, by, type Column } from '../ui'
import s from './MigrationPage.module.css'

/** Migration checks (ADR-003, T21): is a VM on vSphere ready to run on KVM, proven by booting its conversion. */
export function MigrationPage() {
  const q = useMigrationView()
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  return <MigrationBody v={q.data} />
}

function MigrationBody({ v }: { v: MigrationView }) {
  const t = useT().migration
  const fmt = serverTime(v.clock)
  return (
    <main className={s.page}>
      <section className={s.head}>
        <h1>{t.title}</h1>
        <Converter v={v} />
      </section>
      <section>
        <h2>{t.checks}<span className={s.h2s}>{t.checksSub}</span></h2>
        {v.checks.length === 0 ? <p className={s.muted}>{t.noChecks}</p> : <ChecksTable checks={v.checks} fmt={fmt} />}
      </section>
      <section>
        <h2>{t.sources}<span className={s.h2s}>{t.sourcesSub}</span></h2>
        {v.sources.length === 0 ? <p className={s.muted}>{t.noSources}</p> : v.sources.map((src) => <Source key={src.id} src={src} />)}
      </section>
    </main>
  )
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
  if (status === 'OK' && !vwOK) status = 'WARNING'
  return (
    <dl className={s.conv}>
      <dt>{t.converter}</dt>
      <dd><StatusChip status={status} size="xs" /><span>{line}</span></dd>
      {vw && <dd className={vwOK ? s.muted : s.warn}>{vwOK ? t.virtioOk(vw.version) : t.virtioMissing(vw.version, vw.detail)}</dd>}
    </dl>
  )
}

const shortID = (id: string) => id.replace(/^sha256:/, '').slice(0, 12)

function ChecksTable({ checks, fmt }: { checks: MigrationCheckView[]; fmt: Fmt }) {
  const t = useT().migration
  const tt = useT()
  const cols: Column<MigrationCheckView>[] = [
    { key: 'id', header: t.col.id, compare: by((c) => c.id), cell: (c, ctx) => <><RowToggle ctx={ctx} label={tt.table.details} /><span className={s.num}>{c.id}</span></> },
    { key: 'vm', header: t.col.vm, compare: by((c) => c.vm), cell: (c) => <b className={s.vm}>{c.vm}</b> },
    { key: 'source', header: t.col.source, compare: by((c) => c.source), cell: (c) => c.source },
    { key: 'readiness', header: t.col.readiness, compare: by((c) => c.readiness || c.job_state), cell: (c) => <Readiness c={c} /> },
    { key: 'firmware', header: t.col.firmware, compare: by((c) => c.firmware), cell: (c) => t.firmware(c.firmware) },
    { key: 'guest', header: t.col.guest, compare: by((c) => c.guest_id), cell: (c) => <span className={s.muted}>{c.guest_id || '—'}</span> },
    {
      key: 'checked', header: t.col.checked, numeric: true, compare: by((c) => c.finished_at || c.created_at),
      cell: (c) => (c.finished_at ? `${fmt.date(c.finished_at)} ${fmt.clock(c.finished_at)}` : <span className={s.muted}>{c.started_at ? fmt.clock(c.started_at) : '—'}</span>),
    },
    { key: 'why', header: t.col.why, clip: true, cell: (c) => <span title={c.reason}>{c.reason}</span> },
    {
      key: 'report', header: '', cell: (c) => (c.bundle_id
        ? <ReportLink id={c.id} label={t.report} />
        : null),
    },
  ]
  return <DataTable caption={t.checksCaption} columns={cols} rows={checks} rowKey={(c) => String(c.id)}
    rowState={(c) => (c.readiness === 'CRITICAL' ? 'critical' : c.readiness && c.readiness !== 'OK' ? 'bad' : undefined)}
    detail={(c) => (c.result ? <CheckDetail c={c} /> : null)} />
}

function Readiness({ c }: { c: MigrationCheckView }) {
  const t = useT().migration
  if (c.readiness) return <StatusChip status={c.readiness} size="xs" />
  return <span className={s.pending} data-state={c.job_state}>{c.job_state === 'running' ? t.state.running : t.state.queued}</span>
}

/** What a check's row opens to: every step and check, the converter, the vSphere calls, the console. */
function CheckDetail({ c }: { c: MigrationCheckView }) {
  const t = useT().migration
  const r = c.result!
  const conv = r.converter
  const tm = r.timings_s ?? {}
  const boot = r.checks.find((k) => k.name === 'boot')
  return (
    <div className={s.detail}>
      <ul className={s.steps}>
        {r.checks.map((k) => (
          <li key={k.name}>
            <StatusChip status={k.verdict} size="xs" />
            <span className={s.sn}>{k.name.replace(/_/g, ' ')}</span>
            <span className={s.st}>{k.seconds ? dur(k.seconds, true) : ''}</span>
            <span className={s.sd}>{k.detail}</span>
          </li>
        ))}
      </ul>
      <dl className={s.facts}>
        <div><dt>vSphere</dt><dd className={r.vsphere_blocked_calls ? s.bad : ''}>{t.calls(r.vsphere_calls, r.vsphere_blocked_calls)}</dd></div>
        {conv && <div><dt>{t.image}</dt><dd><code>{conv.image}</code></dd></div>}
        {conv?.versions && <div><dt>{t.versions}</dt><dd className={s.pre}>{conv.versions}</dd></div>}
        {conv && <div><dt>{t.virtioWin}</dt><dd>{conv.virtio_win ? `${conv.virtio_win.version}, sha256 ${conv.virtio_win.sha256}` : t.noVirtioWin}</dd></div>}
        {(tm.download || tm.conversion) && (
          <div><dt>Time</dt><dd>{t.timings(dur(tm.download ?? null, true), dur(tm.conversion ?? null, true), dur(boot?.seconds ?? null, true))}</dd></div>
        )}
      </dl>
      {c.screenshots.length > 0 && c.evidence_id > 0 && (
        <div className={s.shots}>
          {c.screenshots.map((n) => (
            <figure key={n}>
              <img src={`/api/v1/evidence/${c.evidence_id}/files/${encodeURIComponent(n)}`} width={720} height={400} alt={`Console of ${c.vm} (${n})`} loading="lazy" />
              <figcaption>{n.replace(/^screenshot-|\.png$/g, '')}</figcaption>
            </figure>
          ))}
        </div>
      )}
    </div>
  )
}

/** One source; operators open its inventory and check a VM from it. */
function Source({ src }: { src: MigrationSource }) {
  const t = useT().migration
  const me = useMe().data
  const act = can(me, 'act')
  const [open, setOpen] = useState(false)
  const host = (() => {
    try {
      return new URL(src.url).host
    } catch {
      return src.url
    }
  })()
  return (
    <div className={s.source}>
      <div className={s.srcHead}>
        <b>{src.name}</b>
        <span className={s.muted}>{host}, {src.username}, {src.insecure ? t.tlsNot : t.tlsVerified}</span>
        {act && <Button variant="quiet" onClick={() => setOpen(!open)} aria-expanded={open}>{open ? t.hideVMs : t.showVMs}</Button>}
      </div>
      {act && open && <Inventory src={src} />}
    </div>
  )
}

function Inventory({ src }: { src: MigrationSource }) {
  const t = useT().migration
  const q = useSourceInventory(src.id, true)
  const queue = useQueueMigrationCheck(src.id)
  if (q.isPending) return <p className={s.muted}>{t.reading}</p>
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const inv = q.data
  const cols: Column<SourceVM>[] = [
    { key: 'name', header: t.col.name, compare: by((v) => v.name), cell: (v) => <b className={s.vm}>{v.name}</b> },
    { key: 'power', header: t.col.power, compare: by((v) => v.power_state), cell: (v) => v.power_state.replace(/^powered/, '').toLowerCase() },
    { key: 'guest', header: t.col.guest, compare: by((v) => v.guest_id), cell: (v) => <span className={s.muted}>{v.guest_id}</span> },
    { key: 'firmware', header: t.col.firmware, compare: by((v) => v.firmware), cell: (v) => <>{t.firmware(v.firmware)}{v.secure_boot && <span className={s.tag}>{t.secureBoot}</span>}</> },
    { key: 'disks', header: t.col.disks, numeric: true, compare: by((v) => v.disk_bytes), cell: (v) => t.disks(v.disks, Math.round(v.disk_bytes / 2 ** 30)) },
    { key: 'state', header: t.col.state, clip: true, cell: (v) => <span className={v.checkable ? s.muted : s.ok} title={v.checkable}>{v.checkable || t.checkable}</span> },
    {
      key: 'act', header: '', cell: (v) => (v.checkable ? null
        : <Button variant="quiet" disabled={queue.isPending} onClick={() => queue.mutate(v.name)}>{t.check}</Button>),
    },
  ]
  return (
    <div className={s.inv}>
      <p className={s.muted}>{inv.product}, {t.invCalls(inv.calls, inv.blocked_calls)}</p>
      <DataTable caption={t.inventoryCaption(src.name)} columns={cols} rows={inv.vms} rowKey={(v) => v.name} />
      {queue.data && <p className={s.muted}>{t.queued(queue.data.vm, queue.data.id)}</p>}
      {queue.error && <Notice tone="critical">{queue.error.message}</Notice>}
    </div>
  )
}

/** A readiness report is an evidence export: it asks for a step-up (ADR-008 §4). */
function ReportLink({ id, label }: { id: number; label: string }) {
  const ask = useStepUp()
  const [err, setErr] = useState<string | null>(null)
  const path = `/api/v1/migration-checks/${id}/report`
  return (
    <>
      <button type="button" className={s.dl} onClick={() => download(ask, path, `gaia-readiness-${id}.tar.gz`).catch((e) => setErr(String(e.message ?? e)))}>{label}</button>
      {err && <span role="alert">{err}</span>}
    </>
  )
}
