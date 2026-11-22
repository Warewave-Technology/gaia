import { useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import { useT } from '../../../i18n'
import type { Fmt } from '../../../lib/time'
import type { TestView, TimelineEvent, VMView } from '../../api/views'
import { JobState, PointProof, Seg, StatusChip } from '../../ui'
import { pointsFromView } from '../../api/vm'
import { groups, marksOf, proofOf, testSays, type Group, type Mark } from './logic'
import s from './Strip.module.css'

const LABEL_W = 84

/**
 * The history strip (docs/design/v3 rule 6): two labelled lanes, restore tests (dots) and backups (ticks); the proven
 * point as a labelled flag; the span from it to now shaded, red when it is longer than the RPO target, which is a dashed
 * line labelled where it is drawn. A mark is chosen with a ring and said in words below, never with a box.
 */
export function Strip({ v, fmt, windowH, point, onRange, onPoint, onShowPoint }: {
  v: VMView
  fmt: Fmt
  windowH: 24 | 168
  point: number | null
  onRange: (h: 24 | 168) => void
  onPoint: (id: number | null) => void
  onShowPoint: (id: number) => void
}) {
  const t = useT().vm3
  const ref = useRef<HTMLDivElement>(null)
  const [w, setW] = useState(1200)
  const [sel, setSel] = useState<string | null>(null)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const ro = new ResizeObserver(() => setW(Math.max(100, el.clientWidth - LABEL_W)))
    ro.observe(el)
    setW(Math.max(100, el.clientWidth - LABEL_W))
    return () => ro.disconnect()
  }, [])

  const end = Date.parse(v.clock.now)
  const span = v.window_s * 1000
  const start = end - span
  const frac = (iso: string) => (Date.parse(iso) - start) / span
  const left = (f: number): CSSProperties => ({ left: `calc(${LABEL_W}px + (100% - ${LABEL_W}px) * ${Math.min(1, Math.max(0, f)).toFixed(5)})` })

  const gs = groups(marksOf(v.status.events, start, span, w))
  // The chosen mark: one clicked here, else the point in the URL (the global search opens the page on a point).
  const chosen = sel ?? (point !== null ? `backup:${point}` : null)
  const selGroup = chosen ? gs.find((g) => g.marks.some((m) => m.key === chosen || (chosen === `backup:${point}` && m.key === `damaged:${point}`))) ?? null : null
  // A point from the URL outside this window is still said below, though it has no mark to ring.
  const urlPoint = !selGroup && sel === null && point !== null ? v.points.find((p) => p.id === point) : undefined
  const capGroup: Group | null = selGroup ?? (urlPoint ? {
    lane: 'backup', px: 0, look: 'b',
    marks: [{ e: { kind: urlPoint.check_state === 'failed' ? 'damaged' : 'backup', id: urlPoint.id, at: urlPoint.taken_at }, key: '', lane: 'backup', look: 'b', px: 0 }],
  } : null)

  const pr = v.status.proven
  const rpo = v.policy?.rpo_target_s ?? null
  const retF = pr ? frac(pr.point_taken_at) : 0
  const over = !pr || (rpo !== null && (v.status.proven_rpo_s ?? Infinity) > rpo)
  const tgtF = rpo !== null ? 1 - (rpo * 1000) / span : -1
  const flagRight = pr && retF > 0.7
  const retAt = pr ? t.day(fmt.at(pr.point_taken_at)) : ''
  const flag = !pr ? t.nothingProven : retF < 0 ? t.returnBefore(retAt) : flagRight ? t.returnHereEnd(retAt) : t.returnHere(retAt)

  const pick = (g: Group) => {
    const k = g.marks[g.marks.length - 1].key
    if (chosen && g.marks.some((m) => m.key === chosen)) {
      setSel(null)
      onPoint(null)
      return
    }
    setSel(k)
    const m = g.marks.length === 1 ? g.marks[0] : null
    onPoint(m && (m.e.kind === 'backup' || m.e.kind === 'damaged') ? m.e.id : null)
  }

  const ticks = [0, 0.25, 0.5, 0.75].map((f) => {
    const iso = new Date(start + f * span).toISOString()
    return f === 0 || windowH === 24 ? fmt.at(iso) : fmt.date(iso)
  })
  return (
    <section className={s.sec} aria-labelledby="strip-title">
      <div className={s.top}>
        <h2 id="strip-title" className={s.h2}>{t.last(windowH)}</h2>
        <Seg label={t.range} value={windowH === 168 ? '7d' : '24h'} options={[['24h', t.h24], ['7d', t.d7]]} onChange={(r) => onRange(r === '7d' ? 168 : 24)} />
      </div>
      <div className={s.tl} ref={ref} role="group" aria-label={t.strip(windowH)}>
        <span className={`${s.band} ${over ? s.over : ''} ${!pr || retF < 0 ? s.open : ''}`} style={{ ...left(retF), right: 0 }} aria-hidden="true" />
        <span className={`${s.flag} ${flagRight ? s.flagRight : ''}`} style={left(pr ? retF : 0)}>{flag}</span>
        {tgtF > 0 && <>
          <span className={s.tgt} style={left(tgtF)} aria-hidden="true" />
          <span className={`${s.tgtl} ${tgtF > 0.94 ? s.tgtlEnd : tgtF < 0.06 ? s.tgtlStart : ''}`} style={left(tgtF)}>{t.rpoTarget}</span>
        </>}
        <span className={s.now} aria-hidden="true" />
        <span className={s.lab} style={{ top: 38 }}>{t.laneTests}</span>
        <span className={s.lane} style={{ top: 46 }} aria-hidden="true" />
        <span className={s.lab} style={{ top: 70 }}>{t.laneBackups}</span>
        <span className={s.lane} style={{ top: 78 }} aria-hidden="true" />
        {gs.map((g) => {
          const on = g === selGroup
          const f = g.px / w
          const label = groupLabel(g, fmt, t)
          return (
            <button key={`${g.lane}${g.marks[0].key}`} type="button" title={label} aria-label={label} aria-pressed={on}
              className={`${g.lane === 'test' ? s.dot : s.tk} ${s[g.look]} ${g.marks.length > 1 ? s.many : ''} ${on ? s.sel : ''}`}
              style={{ ...left(f), top: g.lane === 'test' ? 46 : 72 }} onClick={() => pick(g)}>
              {g.marks.length > 1 && <span className={s.n}>{g.marks.length}</span>}
            </button>
          )
        })}
      </div>
      <div className={s.axis} aria-hidden="true">
        {ticks.map((x, i) => <span key={i} style={{ left: `${i * 25}%` }} className={i === 0 ? s.first : undefined}>{x}</span>)}
        <span className={s.last}>{t.now}</span>
      </div>
      <Caption g={capGroup} v={v} fmt={fmt} onShowPoint={onShowPoint} />
      <ul className={s.legend}>
        <li><span className={`${s.ldot} ${s.p}`} />{t.legend.passed}</li>
        <li><span className={`${s.ldot} ${s.f}`} />{t.legend.failed}</li>
        <li><span className={`${s.ldot} ${s.u}`} />{t.legend.notRun}</li>
        <li><span className={`${s.lband} ${over ? s.over : ''}`} />{t.legend.lose}</li>
      </ul>
    </section>
  )
}

type T = ReturnType<typeof useT>['vm3']

/** What a test mark says, from the test the page knows (its failed check), else from the mark's kind. */
function testOf(e: TimelineEvent, v: VMView): TestView | undefined {
  return v.tests.find((x) => x.id === e.id)
}

function testWord(kind: string, t: T) {
  return kind === 'test_ok' ? t.word.passed : kind === 'test_warning' ? t.word.warned : kind === 'test_failed' ? t.word.failed : t.word.notRun
}

function markLabel(m: Mark, fmt: Fmt, t: T) {
  const at = fmt.at(m.e.at)
  switch (m.e.kind) {
    case 'backup': return t.mark.backup(m.e.id, at)
    case 'damaged': return t.mark.damaged(m.e.id, at)
    case 'backup_failed': return t.mark.backupFailed(m.e.id, at)
    default: return t.mark.test(testWord(m.e.kind, t).toLowerCase(), m.e.id, at)
  }
}

function groupLabel(g: Group, fmt: Fmt, t: T) {
  if (g.marks.length === 1) return markLabel(g.marks[0], fmt, t)
  const ms = [...g.marks].sort((a, b) => a.e.at.localeCompare(b.e.at))
  return t.mark.group(g.marks.length, g.lane === 'test' ? t.mark.tests : t.mark.backups, fmt.at(ms[0].e.at), fmt.at(ms[ms.length - 1].e.at))
}

/** The caption under the strip: the chosen mark in words, or, with none chosen, the point you would return to. */
function Caption({ g, v, fmt, onShowPoint }: { g: Group | null; v: VMView; fmt: Fmt; onShowPoint: (id: number) => void }) {
  const t = useT().vm3
  const pr = v.status.proven
  if (!g) {
    if (!pr) {
      return <div className={s.cap}><StatusChip status="UNKNOWN" size="sm" word={t.word.unknown} /><b>{t.sel.nothingTitle}</b><span>{t.sel.nothingText}</span></div>
    }
    const newest = v.points.reduce<string | null>((a, p) => (a === null || p.taken_at > a ? p.taken_at : a), null)
    return (
      <div className={s.cap}>
        <span className={s.ret}>▼ {t.word.returnHere}</span>
        <b>{t.sel.returnTitle(t.day(fmt.at(pr.point_taken_at)))}</b>
        <span>{newest !== null && Date.parse(newest) > Date.parse(pr.point_taken_at) ? t.sel.returnText : t.sel.returnNewest}</span>
      </div>
    )
  }
  if (g.marks.length > 1) {
    const ms = [...g.marks].sort((a, b) => a.e.at.localeCompare(b.e.at))
    const counts = new Map<string, number>()
    for (const m of ms) {
      const w = m.lane === 'test' ? testWord(m.e.kind, t) : m.e.kind === 'backup' ? t.word.backedUp : m.e.kind === 'damaged' ? t.word.damaged : t.word.backupFailed
      counts.set(w, (counts.get(w) ?? 0) + 1)
    }
    const parts = [...counts].map(([w, k]) => `${k} ${w.toLowerCase()}`).join(', ')
    return (
      <div className={s.cap}>
        <b>{t.sel.groupTitle(ms.length, g.lane === 'test' ? t.mark.tests : t.mark.backups, fmt.at(ms[0].e.at), fmt.at(ms[ms.length - 1].e.at))}</b>
        <span>{t.cap(t.sel.groupText(parts))}</span>
      </div>
    )
  }
  const m = g.marks[0]
  const at = fmt.at(m.e.at)
  if (m.lane === 'test') {
    const test = testOf(m.e, v)
    const st = m.e.kind === 'test_ok' ? 'OK' : m.e.kind === 'test_warning' ? 'WARNING' : m.e.kind === 'test_failed' ? 'CRITICAL' : 'UNKNOWN'
    const text = test ? testSays(test, t) : m.e.kind === 'test_ok' ? t.testSays.passed : m.e.kind === 'test_failed' ? t.testSays.failed
      : m.e.kind === 'test_warning' ? t.testSays.warned : t.testSays.notRun
    return (
      <div className={s.cap}>
        <StatusChip status={st} size="sm" word={testWord(m.e.kind, t)} />
        <b>{t.sel.testTitle(at)}</b>
        <span>{text}</span>
        {test && <button type="button" className={s.link} onClick={() => onShowPoint(test.point_id)}>{t.sel.showPoint(test.point_id)}</button>}
      </div>
    )
  }
  if (m.e.kind === 'backup_failed') {
    return <div className={s.cap}><JobState state="failed" /><b>{t.sel.failedTitle(at)}</b><span>{t.sel.backupFailed(m.e.id)}</span></div>
  }
  const isRet = pr?.point_id === m.e.id
  const p = v.points.find((x) => x.id === m.e.id)
  // The point's proof as the Points tab says it (its own tests), and the sentence about where it stands.
  const row = p ? pointsFromView(v).find((x) => x.id === p.id) : undefined
  const pf = row ? proofOf(row, pr?.point_id ?? null, t) : null
  const proof = m.e.kind === 'damaged' ? 'DAMAGED' : pf?.proof ?? (isRet ? 'PROVEN' : 'BACKED_UP')
  const text = proof === 'DAMAGED' ? t.sel.damaged : isRet ? t.sel.proven : p?.check_state === 'unchecked' ? t.sel.unchecked
    : proof === 'FAILED' || proof === 'UNKNOWN' ? t.sel.tested(pf!.detail)
      : pr && Date.parse(m.e.at) < Date.parse(pr.point_taken_at) ? t.sel.older : t.sel.newer
  return (
    <div className={s.cap}>
      <PointProof proof={proof} />
      <b>{t.sel.backupTitle(at)}</b>
      <span>{text}{p && p.bytes > 0 ? ` ${t.size(p.bytes)}, ${p.kind}.` : ''}</span>
      {p && <button type="button" className={s.link} onClick={() => onShowPoint(p.id)}>{t.sel.showPoint(p.id)}</button>}
    </div>
  )
}

