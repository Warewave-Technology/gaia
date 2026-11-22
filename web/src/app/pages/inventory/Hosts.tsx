import { useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { useT } from '../../../i18n'
import { dur } from '../../../lib/duration'
import { serverTime, type Fmt } from '../../../lib/time'
import { query, useFilters } from '../../../lib/url'
import { can } from '../../access'
import { change, useAllHosts, type HostCheck, type HostRow, type Scanned } from '../../api/inventory'
import type { User } from '../../api/session'
import { Failed, Loading } from '../../layout/States'
import { Button, DataTable, FilterChip, PageHead, StatusBar, StatusChip, StatusCounts, TechDetails, Toolbar, type Column } from '../../ui'
import s from '../Inventory.module.css'
import { Confirm, Field, message, useAction } from './common'

type T = ReturnType<typeof useT>['inventory']

/** A "Check now" answer, kept on this screen only (the API does not store it). */
type Checked = { at: string; ok?: HostCheck; error?: string }

/**
 * A host's state, worst first. VMs that cannot reach it make it unreachable; a key nobody confirmed means Gaia reads
 * nothing from it (ADR-007 C11 host_key_untrusted); a pinned key nothing has used yet (no VM, no backup) is not
 * checked yet. Only a host a backup has read is OK: an unchecked state is never green (CLAUDE.md rule 4).
 */
export type HostState = 'unreachable' | 'untrusted' | 'unchecked' | 'ok'
const STATES: readonly HostState[] = ['unreachable', 'untrusted', 'unchecked', 'ok']
const STATUS: Record<HostState, string> = { unreachable: 'CRITICAL', untrusted: 'UNKNOWN', unchecked: 'UNKNOWN', ok: 'OK' }

export function stateOf(r: HostRow): HostState {
  if (r.unreachable > 0) return 'unreachable'
  if (!r.ssh_host_key) return 'untrusted'
  if (r.vms === 0 || !r.last_point_at) return 'unchecked'
  return 'ok'
}

type Row =
  | { kind: 'host'; r: HostRow; state: HostState }
  | { kind: 'group'; hosts: HostRow[] }

const vmCounts = (rs: HostRow[]) => {
  const c = { CRITICAL: 0, UNKNOWN: 0, WARNING: 0, OK: 0 }
  for (const r of rs) {
    c.CRITICAL += r.critical
    c.UNKNOWN += r.unknown
    c.WARNING += r.warning
    c.OK += r.ok
  }
  return c
}

/**
 * The hosts (docs/design/v3: Hosts): the headline says every state with its count ("2 unreachable · 98 not trusted
 * yet · 0 OK", rule 7); chips above the data-table pick one state and stay in the URL; with no chip set, the hosts
 * whose key nobody confirmed are one group row, so the hosts that need something else are not buried under them.
 */
export function Hosts({ me }: { me: User }) {
  const t = useT().inventory
  const h = t.hosts
  const q = useAllHosts()
  const act = useAction()
  const writer = can(me, 'act')
  const [f, setF] = useFilters(['state'] as const)
  const [adding, setAdding] = useState(false)
  const [scanned, setScanned] = useState<Record<number, Scanned>>({})
  const [checked, setChecked] = useState<Record<number, Checked>>({})

  async function scan(id: number) {
    const k = await act.run(`scan/${id}`, 'POST', `/api/v1/hosts/${id}/scan`, (hd) => change.scanHost(id, hd))
    if (k) setScanned((m) => ({ ...m, [id]: k }))
  }
  async function check(id: number) {
    const at = new Date().toISOString()
    try {
      const ok = await change.checkHost(id)
      setChecked((m) => ({ ...m, [id]: { at, ok } }))
    } catch (e) {
      setChecked((m) => ({ ...m, [id]: { at, error: message(e) } }))
    }
  }
  async function trust(r: HostRow, k: Scanned) {
    const ok = await act.run(`trust/${r.id}`, 'POST', `/api/v1/hosts/${r.id}/trust`, (hd) => change.trustHost(r.id, k.fingerprint, hd), () => h.trusted(r.name))
    if (!ok) return
    setScanned((m) => { const n = { ...m }; delete n[r.id]; return n })
    await check(r.id)
  }

  if (q.isPending || (q.isSuccess && !q.complete)) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const clock = q.head?.clock ?? { now: new Date().toISOString(), zone: 'UTC', offset_s: 0 }
  const fmt = serverTime(clock)
  const all = q.rows
  const by: Record<HostState, HostRow[]> = { unreachable: [], untrusted: [], unchecked: [], ok: [] }
  for (const r of all) by[stateOf(r)].push(r)
  const pick = STATES.includes(f.state as HostState) ? (f.state as HostState) : null
  const hostRow = (r: HostRow): Row => ({ kind: 'host', r, state: stateOf(r) })
  let rows: Row[]
  if (pick) rows = by[pick].map(hostRow)
  else {
    const group: Row[] = by.untrusted.length > 1 ? [{ kind: 'group', hosts: by.untrusted }] : by.untrusted.map(hostRow)
    rows = [...by.unreachable.map(hostRow), ...group, ...by.unchecked.map(hostRow), ...by.ok.map(hostRow)]
  }
  const headline = all.length === 0 ? h.none : STATES
    .filter((x) => x !== 'unchecked' || by.unchecked.length > 0)
    .map((x) => `${t.count(by[x].length)} ${h.states[x]}`).join(' · ')

  const cols: Column<Row>[] = [
    {
      key: 'host', header: h.cols.host, width: '170px',
      title: (x) => (x.kind === 'host' ? `${x.r.name} ${h.ssh(x.r.ssh_user, x.r.address, x.r.ssh_port)}` : h.group.label(x.hosts.length)),
      cell: (x) => x.kind === 'group'
        ? <span className={s.strong}>{h.group.hosts(x.hosts.length)}</span>
        : <><span className={s.name}>{x.r.name}</span><span className={s.cellOf}>{h.ssh(x.r.ssh_user, x.r.address, x.r.ssh_port)}</span></>,
    },
    {
      key: 'connection', header: h.cols.connection,
      title: (x) => (x.kind === 'host' ? connection(x.r, checked[x.r.id], fmt, t).text : h.group.short),
      cell: (x) => {
        if (x.kind === 'group') return <><StatusChip status="UNKNOWN" word={h.word.untrusted} size="sm" /><span>{h.group.short}</span></>
        const st = connection(x.r, checked[x.r.id], fmt, t)
        return <><StatusChip status={st.status} word={st.word} size="sm" /><span>{st.short}</span></>
      },
    },
    {
      key: 'vms', header: h.cols.vms, width: '450px',
      title: (x) => (x.kind === 'host' ? h.vms(x.r.vms) : h.vms(x.hosts.reduce((a, r) => a + r.vms, 0))),
      cell: (x) => {
        const rs = x.kind === 'host' ? [x.r] : x.hosts
        const total = rs.reduce((a, r) => a + r.vms, 0)
        if (total === 0) return <span className={s.cellOf}>{h.noVMs}</span>
        const counts = vmCounts(rs)
        return <span className={s.vmsCell}><b className={s.fig}>{h.vms(total)}</b><StatusBar counts={counts} /><StatusCounts counts={counts} /></span>
      },
    },
    {
      key: 'last', header: h.cols.last, width: '120px',
      cell: (x) => (x.kind === 'host' ? (x.r.last_point_at ? fmt.at(x.r.last_point_at) : t.never) : t.dash),
    },
  ]
  if (writer) {
    cols.push({
      key: 'action', header: h.cols.action, width: '120px',
      cell: (x, ctx) => {
        if (x.kind === 'group') return <Button variant="quiet" className={s.cellBtn} onClick={() => setF({ state: 'untrusted' })}>{h.group.show}</Button>
        const r = x.r
        if (!r.ssh_host_key) {
          return (
            <Button variant="quiet" className={s.cellBtn} disabled={act.busy === `scan/${r.id}`}
              onClick={async () => { await scan(r.id); if (!ctx.open) ctx.toggle() }}>{h.scan}</Button>
          )
        }
        return <Button variant="quiet" className={s.cellBtn} onClick={async () => { await check(r.id); if (!ctx.open) ctx.toggle() }}>{h.check}</Button>
      },
    })
  }

  const untrustedVMs = by.untrusted.reduce((a, r) => a + r.vms, 0)
  return (
    <>
      <PageHead question={h.question} answer={headline} lede={<p>{h.hint}</p>} />
      <section className={s.sec}>
        <Toolbar end={writer && (
          <Button variant={adding ? 'quiet' : 'primary'} onClick={() => setAdding(!adding)}>{adding ? t.cancel : h.add}</Button>
        )}>
          <FilterChip set={!pick} onClick={() => setF({ state: '' })}>{h.chip(h.all, all.length)}</FilterChip>
          {STATES.map((x) => (
            <FilterChip key={x} set={pick === x} onClick={() => setF({ state: pick === x ? '' : x })}>{h.chip(h.chips[x], by[x].length)}</FilterChip>
          ))}
        </Toolbar>
        {adding && (
          <AddHost busy={act.busy === 'add'} onAdd={async (body) => {
            const host = await act.run('add', 'POST', '/api/v1/hosts', (hd) => change.addHost(body, hd))
            if (!host) return
            setAdding(false)
            setF({ state: '' })
            await scan(host.id)
          }} />
        )}
        {pick === 'untrusted' && by.untrusted.length > 0 && (
          <p className={s.hint}>{h.group.long(by.untrusted.length, untrustedVMs)} <b>{h.todo}</b> {h.group.todo}</p>
        )}
        <DataTable<Row>
          caption={h.caption}
          columns={cols}
          rows={rows}
          rowKey={(x) => (x.kind === 'group' ? 'group-untrusted' : String(x.r.id))}
          rowState={(x) => (x.kind === 'host' && x.state === 'unreachable' ? 'critical' : undefined)}
          minWidth={980}
          empty={all.length === 0 ? h.empty : h.emptyFilter}
          detail={(x) => (x.kind === 'group'
            ? <GroupDetail hosts={x.hosts} vms={untrustedVMs} />
            : <HostDetail r={x.r} c={checked[x.r.id]} k={scanned[x.r.id]} fmt={fmt} writer={writer} busy={act.busy}
              onTrust={() => scanned[x.r.id] && trust(x.r, scanned[x.r.id])}
              onRemove={() => act.run(`remove/${x.r.id}`, 'DELETE', `/api/v1/hosts/${x.r.id}`, (hd) => change.removeHost(x.r.id, hd))} />)}
        />
        {act.outcome}
        {writer && <p className={s.foot}>{h.foot}</p>}
      </section>
    </>
  )
}

/**
 * The connection: the state's chip word, a few words for the table's cell and the sentence for the row's detail
 * (and the cell's hover); a "Check now" answer on this screen wins over what the status table last knew.
 */
function connection(r: HostRow, c: Checked | undefined, fmt: Fmt, t: T) {
  const h = t.hosts
  const x = h.text
  const k = h.short
  const state = stateOf(r)
  if (state === 'unreachable') {
    const since = fmt.at(r.unreachable_since)
    return { status: 'CRITICAL', word: h.word.unreachable, short: k.since(since), text: x.unreachable(r.unreachable, since) }
  }
  if (state === 'untrusted') return { status: 'UNKNOWN', word: h.word.untrusted, short: k.untrusted, text: x.untrusted }
  if (c?.error) return { status: 'CRITICAL', word: h.word.failed, short: k.failed(fmt.at(c.at)), text: x.failed(fmt.at(c.at)) }
  if (c?.ok) return { status: 'OK', word: h.word.answered, short: fmt.at(c.at), text: x.answered(fmt.at(c.at)) }
  if (state === 'unchecked') {
    return r.vms === 0
      ? { status: 'UNKNOWN', word: h.word.unchecked, short: k.noVMs, text: x.noVMs }
      : { status: 'UNKNOWN', word: h.word.unchecked, short: k.neverRead, text: x.neverRead }
  }
  return { status: STATUS[state], word: undefined, short: k.ok, text: x.ok }
}

function took(seconds: number) {
  return seconds < 1 ? 'under a second' : dur(seconds, true)
}

function HostDetail({ r, c, k, fmt, writer, busy, onTrust, onRemove }: {
  r: HostRow
  c: Checked | undefined
  k: Scanned | undefined
  fmt: Fmt
  writer: boolean
  busy: string | null
  onTrust: () => void
  onRemove: () => void
}) {
  const t = useT().inventory
  const h = t.hosts
  const st = connection(r, c, fmt, t)
  return (
    <div className={s.facts}>
      <p>{st.text}.</p>
      <p className={s.cellOf}>{h.ssh(r.ssh_user, r.address, r.ssh_port)} · {h.added(fmt.at(r.created_at))}</p>
      {c?.ok && <p>{h.checked(c.ok.libvirt, c.ok.qemu, c.ok.domains.length, took(c.ok.seconds), fmt.at(c.at))}</p>}
      {c?.error && <><p>{h.checkFailed}</p><TechDetails>{c.error}</TechDetails></>}
      {k && (
        <div className={s.facts}>
          <p>{h.keyHead}</p>
          <p className={s.mono1}>{h.key(k.type, k.fingerprint)}</p>
          <p className={s.hint}>{h.keyHint}</p>
          <span><Button onClick={onTrust} disabled={busy === `trust/${r.id}`}>{busy === `trust/${r.id}` ? t.working : h.trust}</Button></span>
        </div>
      )}
      <span className={s.inline}>
        {r.vms > 0 && <Link to={`/${query({ show: 'all', host: r.id })}`}>{h.showVMs}</Link>}
        {writer && r.vms === 0 && <Confirm label={h.remove} question={h.removeConfirm(r.name)} busy={busy === `remove/${r.id}`} onYes={onRemove} />}
      </span>
    </div>
  )
}

function GroupDetail({ hosts, vms }: { hosts: HostRow[]; vms: number }) {
  const h = useT().inventory.hosts
  return (
    <div className={s.facts}>
      <p>{h.group.long(hosts.length, vms)}</p>
      <p><b>{h.todo}</b> {h.group.todo}</p>
    </div>
  )
}

function AddHost({ busy, onAdd }: { busy: boolean; onAdd: (b: { name: string; address: string; ssh_port: number; ssh_user: string }) => void }) {
  const t = useT().inventory
  const f = t.hosts.form
  const [name, setName] = useState('')
  const [address, setAddress] = useState('')
  const [port, setPort] = useState('22')
  const [user, setUser] = useState('gaia')
  function submit(e: FormEvent) {
    e.preventDefault()
    onAdd({ name: name.trim(), address: address.trim(), ssh_port: Number(port), ssh_user: user.trim() })
  }
  return (
    <form className={s.form} onSubmit={submit}>
      <h3>{f.title}</h3>
      <div className={s.fields}>
        <Field label={f.name}><input required value={name} onChange={(e) => setName(e.target.value)} autoFocus /></Field>
        <Field label={f.address}><input required value={address} onChange={(e) => setAddress(e.target.value)} /></Field>
        <Field label={f.port}><input required type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} /></Field>
        <Field label={f.user}><input required value={user} onChange={(e) => setUser(e.target.value)} /></Field>
      </div>
      <p className={s.hint}>{f.hint}</p>
      <div className={s.buttons}><Button type="submit" disabled={busy}>{busy ? t.working : f.save}</Button></div>
    </form>
  )
}

