import { useQuery } from '@tanstack/react-query'
import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { useNavigate } from 'react-router'
import { useT } from '../../i18n'
import { query } from '../../lib/url'
import { serverTime } from '../../lib/time'
import { useSystem } from '../api/views'
import { api } from '../../api/client'
import { unwrap } from '../api/errors'
import s from './Search.module.css'

type Hit = { key: string; kind: 'vm' | 'host' | 'policy' | 'point'; name: string; sub: string; to: string }

/**
 * The global search (T27): VMs, hosts, policies by name and a point by id, from any screen; `/` focuses it. A host
 * or a policy opens the overview filtered to it, so the URL can be shared.
 */
export function Search() {
  const t = useT().search
  const t3 = useT().v3.search
  // a point is said with its time, in the server's zone like every time on screen (docs/design/v3 rule 4)
  const clock = useSystem().data?.clock
  const nav = useNavigate()
  const id = useId()
  const input = useRef<HTMLInputElement>(null)
  const [text, setText] = useState('')
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  const [at, setAt] = useState(0)

  // `/` anywhere but in a field focuses the search box.
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      const el = e.target as HTMLElement | null
      if (e.key !== '/' || e.metaKey || e.ctrlKey || e.altKey) return
      if (el && (el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName))) return
      e.preventDefault()
      input.current?.focus()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
  useEffect(() => {
    const h = setTimeout(() => setQ(text.trim()), 150)
    return () => clearTimeout(h)
  }, [text])

  const res = useQuery({
    queryKey: ['search', q],
    queryFn: () => unwrap(api.GET('/api/v1/search', { params: { query: { q, limit: 8 } } })),
    enabled: q.length > 0,
    staleTime: 10_000,
  })
  const hits = useMemo<Hit[]>(() => {
    const r = res.data
    if (!r) return []
    return [
      ...r.vms.map((v) => ({ key: `vm${v.id}`, kind: 'vm' as const, name: v.name, sub: t.vm, to: `/vms/${v.id}` })),
      ...r.hosts.map((h) => ({ key: `h${h.id}`, kind: 'host' as const, name: h.name, sub: t.host(h.count), to: `/${query({ tab: 'vms', host: h.id })}` })),
      ...r.policies.map((p) => ({ key: `p${p.id}`, kind: 'policy' as const, name: p.name, sub: t.policy(p.count), to: `/${query({ tab: 'vms', policy: p.id })}` })),
      ...r.points.map((p) => ({
        key: `pt${p.id}`, kind: 'point' as const, name: t3.pointName(p.id), sub: clock && p.taken_at ? t3.point(p.vm, serverTime(clock).at(p.taken_at)) : p.vm,
        to: `/vms/${p.vm_id}${query({ point: p.id })}`,
      })),
    ]
  }, [res.data, t, t3, clock])
  useEffect(() => setAt(0), [q])

  const go = (h: Hit | undefined) => {
    if (!h) return
    setOpen(false)
    setText('')
    input.current?.blur()
    nav(h.to)
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setAt((i) => Math.min(i + 1, hits.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setAt((i) => Math.max(i - 1, 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      go(hits[at])
    } else if (e.key === 'Escape') {
      setOpen(false)
      input.current?.blur()
    }
  }
  const shown = open && q.length > 0
  return (
    <div className={s.find}>
      <label className={s.box}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <circle cx="11" cy="11" r="7" stroke="currentColor" strokeWidth="2" />
          <path d="M20 20l-4-4" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
        </svg>
        <input
          ref={input}
          type="search"
          role="combobox"
          aria-label={t.label}
          aria-expanded={shown}
          aria-controls={`${id}-list`}
          aria-activedescendant={shown && hits[at] ? `${id}-${hits[at].key}` : undefined}
          placeholder={t.placeholder}
          value={text}
          onChange={(e) => {
            setText(e.target.value)
            setOpen(true)
          }}
          onFocus={() => setOpen(true)}
          onBlur={() => setTimeout(() => setOpen(false), 120)}
          onKeyDown={onKey}
        />
        <kbd className={s.kbd} aria-hidden="true">/</kbd>
      </label>
      {shown && (
        <ul className={s.list} id={`${id}-list`} role="listbox" aria-label={t.results}>
          {res.isPending && <li className={s.note}>{t.searching}</li>}
          {res.isError && <li className={s.note}>{t.failed}</li>}
          {res.isSuccess && hits.length === 0 && <li className={s.note}>{t.none(q)}</li>}
          {hits.map((h, i) => (
            <li
              key={h.key}
              id={`${id}-${h.key}`}
              role="option"
              aria-selected={i === at}
              className={i === at ? s.on : ''}
              onMouseDown={(e) => {
                e.preventDefault()
                go(h)
              }}
              onMouseEnter={() => setAt(i)}
            >
              <span className={s.kind}>{t.kinds[h.kind]}</span>
              <span className={s.name}>{h.name}</span>
              <span className={s.sub}>{h.sub}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
