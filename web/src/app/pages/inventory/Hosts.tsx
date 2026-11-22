import { useState, type FormEvent } from 'react'
import { useT } from '../../../i18n'
import { serverTime, type Fmt, type ServerClock } from '../../../lib/time'
import { can } from '../../access'
import { change, useHostRows, type HostCheck, type HostRow, type Scanned } from '../../api/inventory'
import type { User } from '../../api/session'
import { Failed, Loading } from '../../layout/States'
import { Button, MoreRows, PageHead, StatusBar, StatusChip, StatusCounts } from '../../ui'
import s from '../Inventory.module.css'
import { Confirm, Field, message, useAction, when } from './common'

type T = ReturnType<typeof useT>['inventory']
/** A "Check now" answer, kept on this screen only (the API does not store it). */
type Checked = { at: string; ok?: HostCheck; error?: string }

/**
 * A host's connection, from the status table and the host key: VMs that cannot reach it make it CRITICAL; a key not
 * pinned, or a host nothing has read yet, is UNKNOWN (an unchecked state is never green, CLAUDE.md rule 4).
 */
function connection(r: HostRow, c: Checked | undefined, fmt: Fmt, clock: ServerClock, t: T) {
  const h = t.hosts
  if (r.unreachable > 0) return { status: 'CRITICAL', word: h.word.unreachable, text: h.unreachableText(r.unreachable, when(fmt, clock, r.unreachable_since, t)) }
  if (!r.ssh_host_key) return { status: 'UNKNOWN', word: h.word.notPinned, text: h.notPinnedText }
  if (c?.error) return { status: 'CRITICAL', word: h.word.unreachable, text: c.error }
  if (c?.ok) return { status: 'OK', word: h.word.answered, text: '' }
  if (r.vms === 0) return { status: 'UNKNOWN', word: h.word.notChecked, text: h.noVMs }
  if (!r.last_point_at) return { status: 'UNKNOWN', word: h.word.notChecked, text: h.neverRead }
  return { status: 'OK', word: undefined, text: h.readOk }
}

export function Hosts({ me }: { me: User }) {
  const t = useT().inventory
  const q = useHostRows()
  const act = useAction()
  const writer = can(me, 'act')
  const [adding, setAdding] = useState(false)
  const [scanned, setScanned] = useState<Record<number, Scanned>>({})
  const [checked, setChecked] = useState<Record<number, Checked>>({})

  async function scan(id: number) {
    const k = await act.run(`scan/${id}`, 'POST', `/api/v1/hosts/${id}/scan`, (h) => change.scanHost(id, h))
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
  async function trust(r: { id: number; name: string }, k: Scanned) {
    const ok = await act.run(`trust/${r.id}`, 'POST', `/api/v1/hosts/${r.id}/trust`, (h) => change.trustHost(r.id, k.fingerprint, h), () => t.hosts.trusted(r.name))
    if (!ok) return
    setScanned((m) => { const n = { ...m }; delete n[r.id]; return n })
    await check(r.id)
  }

  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const head = q.head
  const total = head?.total ?? 0
  const clock = head?.clock ?? { now: new Date().toISOString(), zone: 'UTC', offset_s: 0 }
  const fmt = serverTime(clock)
  const down = q.rows.filter((r) => r.unreachable > 0).length
  return (
    <>
      <PageHead
        question={t.hosts.question}
        answer={down > 0 && !q.hasNextPage ? t.hosts.unreachable(down, total) : t.hosts.answer(total)}
        lede={<p>{t.hosts.hint}</p>}
      />
      <section className={s.sec}>
        {writer && (
          <div className={s.bar}>
            <Button className={s.end} variant={adding ? 'quiet' : 'primary'} onClick={() => setAdding(!adding)}>{adding ? t.cancel : t.hosts.add}</Button>
          </div>
        )}
        {adding && (
          <AddHost busy={act.busy === 'add'} onAdd={async (body) => {
            const host = await act.run('add', 'POST', '/api/v1/hosts', (h) => change.addHost(body, h))
            if (!host) return
            setAdding(false)
            await scan(host.id)
          }} />
        )}
        {q.rows.length === 0 ? <p className={s.empty}>{t.hosts.empty}</p> : (
          <div className={s.scroll}>
            <table className={s.tbl}>
              <thead>
                <tr>
                  <th scope="col">{t.hosts.cols.host}</th>
                  <th scope="col">{t.hosts.cols.connection}</th>
                  <th scope="col">{t.hosts.cols.vms}</th>
                  <th scope="col">{t.hosts.cols.last}</th>
                  {writer && <th scope="col"><span className={s.vh}>{t.action}</span></th>}
                </tr>
              </thead>
              <tbody>
                {q.rows.map((r) => {
                  const c = checked[r.id]
                  const st = connection(r, c, fmt, clock, t)
                  const k = scanned[r.id]
                  return (
                    <HostRows key={r.id} r={r} st={st} c={c} k={k} fmt={fmt} clock={clock} t={t} writer={writer} busy={act.busy}
                      onScan={() => scan(r.id)} onCheck={() => check(r.id)} onTrust={() => k && trust(r, k)}
                      onRemove={() => act.run(`remove/${r.id}`, 'DELETE', `/api/v1/hosts/${r.id}`, (h) => change.removeHost(r.id, h))} />
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
        {act.outcome}
        <MoreRows shown={q.rows.length} total={total} more={!!q.hasNextPage} loading={q.isFetchingNextPage} onMore={() => q.fetchNextPage()} />
        {writer && <p className={s.foot}>{t.hosts.foot}</p>}
      </section>
    </>
  )
}

function HostRows({ r, st, c, k, fmt, clock, t, writer, busy, onScan, onCheck, onTrust, onRemove }: {
  r: HostRow
  st: { status: string; word?: string; text: string }
  c: Checked | undefined
  k: Scanned | undefined
  fmt: Fmt
  clock: ServerClock
  t: T
  writer: boolean
  busy: string | null
  onScan: () => void
  onCheck: () => void
  onTrust: () => void
  onRemove: () => void
}) {
  const h = t.hosts
  const counts = { CRITICAL: r.critical, UNKNOWN: r.unknown, WARNING: r.warning, OK: r.ok }
  return (
    <>
      <tr className={st.status === 'CRITICAL' ? s.rCRITICAL : st.status === 'UNKNOWN' ? s.rUNKNOWN : ''}>
        <th scope="row">
          <span className={s.name}>{r.name}</span>
          <span className={s.of}>{h.ssh(r.ssh_user, r.address, r.ssh_port)}</span>
        </th>
        <td>
          <span className={s.state}>
            <StatusChip status={st.status} word={st.word} size="sm" />
            {st.text && <span>{st.text}</span>}
          </span>
          {c?.ok && <span className={s.of}>{h.checked(c.ok.libvirt, c.ok.qemu, c.ok.domains.length, c.ok.seconds.toFixed(1), when(fmt, clock, c.at, t))}</span>}
        </td>
        <td className={s.mid}>
          {r.vms === 0 ? <span className={s.of}>{h.vms(0)}</span> : (
            <>
              <b className={s.fig}>{h.vms(r.vms)}</b>
              <StatusBar counts={counts} />
              <StatusCounts counts={counts} />
            </>
          )}
        </td>
        <td className={s.fg2}>{r.last_point_at ? when(fmt, clock, r.last_point_at, t) : t.never}</td>
        {writer && (
          <td>
            <span className={s.acts}>
              {!r.ssh_host_key && !k && <Button variant="quiet" onClick={onScan} disabled={busy === `scan/${r.id}`}>{h.scan}</Button>}
              {r.ssh_host_key && <Button variant="quiet" onClick={onCheck}>{h.check}</Button>}
              {r.vms === 0 && <Confirm label={h.remove} question={h.removeConfirm(r.name)} busy={busy === `remove/${r.id}`} onYes={onRemove} />}
            </span>
          </td>
        )}
      </tr>
      {k && (
        <tr className={s.detail}>
          <td colSpan={writer ? 5 : 4}>
            <div className={s.form}>
              <p className={s.mono}>{h.key(k.type, k.fingerprint)}</p>
              <p className={s.hint}>{h.keyHint}</p>
              <div className={s.buttons}>
                <Button onClick={onTrust} disabled={busy === `trust/${r.id}`}>{busy === `trust/${r.id}` ? t.working : h.trust}</Button>
              </div>
            </div>
          </td>
        </tr>
      )}
    </>
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
