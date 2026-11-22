import { Fragment, useMemo, useState, type MouseEvent, type ReactNode } from 'react'
import { useT } from '../../i18n'
import { PAGE_SIZES, type Pages } from '../api/pages'
import s from './DataTable.module.css'

/** What a cell gets besides its row: whether the row's detail is open, and how to open or close it. */
export type RowContext = { open: boolean; toggle: () => void; detailId: string }

export type Column<T> = {
  key: string
  header: string
  /** Numbers and times: right-aligned, tabular figures. */
  numeric?: boolean
  /** The column's width (CSS); without one the column takes what is left. Rows are one line: text is cut with `…`. */
  width?: string
  /** The cell's whole text on hover, for a cell that may be cut. */
  title?: (row: T) => string | undefined
  /** Kept for the screens not yet on v3: every cell is cut to one line now. */
  clip?: boolean
  /** Orders two rows by this column, ascending (rows the screen holds only; a server-paged table is ordered by the
   * server). Without it the column cannot be sorted. */
  compare?: (a: T, b: T) => number
  cell: (row: T, ctx: RowContext) => ReactNode
}

export type Sort = { key: string; dir: 'asc' | 'desc' }

/**
 * Every list on a Gaia screen (docs/design/v3 rule 1, ADR-007 C15): a fixed height with a sticky header, one line
 * per row (≈ 40 px, long text cut with `…`, whole on hover or in the row's detail), and pages of 25, 50 or 100 rows
 * with "1–25 of 4,102" under it. No list grows the page.
 *
 * With `pages` the server pages it (usePages): the table shows that one page. Without, the rows are the screen's and
 * the table pages and sorts them itself. `data-table`, `data-page-size` and `data-rows` let the T27 gate check that
 * no table ever shows more than one page (lab/scale/ui-gate.mjs).
 */
export function DataTable<T>({
  caption,
  columns,
  rows,
  pages,
  rowKey,
  detail,
  rowState,
  pageSize = 25,
  minWidth,
  height,
  empty,
  sizes = true,
}: {
  /** The table's name (visually hidden). */
  caption: string
  columns: readonly Column<T>[]
  /** The rows when the screen holds them all (a short list). */
  rows?: readonly T[]
  /** The rows when the server pages them. */
  pages?: Pages<T>
  rowKey: (row: T) => string
  /** The content of the detail row under `row`; null: this row has none. */
  detail?: (row: T) => ReactNode | null
  /** A word for the row's state, set as `data-state` (styled here: "bad", "mark", "critical"). */
  rowState?: (row: T) => string | undefined
  pageSize?: number
  /** The table scrolls sideways in its frame below this width (px). */
  minWidth?: number
  /** The frame's fixed height (CSS); the default holds about 12 rows. */
  height?: string
  /** What an empty table says. */
  empty?: ReactNode
  /** Offer 25 / 50 / 100 rows. */
  sizes?: boolean
}) {
  const t = useT().table
  const [sort, setSort] = useState<Sort | null>(null)
  const [local, setLocal] = useState({ page: 0, size: pageSize })
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set())
  const toggle = (k: string) =>
    setOpen((old) => {
      const next = new Set(old)
      if (!next.delete(k)) next.add(k)
      return next
    })

  const all = rows ?? []
  const ordered = useMemo(() => {
    const col = sort ? columns.find((c) => c.key === sort.key) : undefined
    if (pages || !sort || !col?.compare) return all
    const sign = sort.dir === 'asc' ? 1 : -1
    // a stable sort: rows that compare equal keep the order they came in
    return [...all].sort((a, b) => sign * col.compare!(a, b))
  }, [all, columns, sort, pages])

  // One page, either the server's or a slice of the screen's rows.
  let shown: readonly T[], from: number, total: number | null, pageNo: number, pageCount: number | null, size: number
  let hasPrev: boolean, hasNext: boolean, go: { first: () => void; prev: () => void; next: () => void; size: (n: number) => void }
  if (pages) {
    shown = pages.rows.slice(0, pages.size)
    ;({ from, total, page: pageNo, pages: pageCount, size, hasPrev, hasNext } = pages)
    go = { first: pages.first, prev: pages.prev, next: pages.next, size: pages.setSize }
  } else {
    size = local.size
    pageCount = Math.max(1, Math.ceil(ordered.length / size))
    const at = Math.min(local.page, pageCount - 1) // the rows can shrink under a page (a refresh)
    shown = ordered.slice(at * size, (at + 1) * size)
    from = ordered.length ? at * size + 1 : 0
    total = ordered.length
    pageNo = at + 1
    hasPrev = at > 0
    hasNext = at < pageCount - 1
    go = {
      first: () => setLocal((o) => ({ ...o, page: 0 })),
      prev: () => setLocal((o) => ({ ...o, page: Math.max(0, at - 1) })),
      next: () => setLocal((o) => ({ ...o, page: at + 1 })),
      size: (n) => setLocal({ page: 0, size: n }),
    }
  }

  const cycle = (key: string) => {
    go.first()
    if (sort?.key !== key) setSort({ key, dir: 'asc' })
    else if (sort.dir === 'asc') setSort({ key, dir: 'desc' })
    else setSort(null)
  }

  const onRowClick = (e: MouseEvent, k: string) => {
    // Links and buttons handle their own clicks; selecting text is not a click.
    if ((e.target as HTMLElement).closest('a, button, input, select, label')) return
    if (window.getSelection()?.toString()) return
    toggle(k)
  }

  const loading = pages ? pages.isPending : false
  const to = from + shown.length - (shown.length ? 1 : 0)
  return (
    <div className={s.frame} data-table={caption} data-page-size={size} data-rows={shown.length}>
      <div className={s.scroll} style={height ? { maxHeight: height } : undefined}>
        <table className={s.table} style={minWidth ? { minWidth } : undefined}>
          <caption className={s.hidden}>{caption}</caption>
          <colgroup>{columns.map((c) => <col key={c.key} style={c.width ? { width: c.width } : undefined} />)}</colgroup>
          <thead>
            <tr>
              {columns.map((c) => {
                const dir = sort?.key === c.key ? sort.dir : null
                const sortable = !pages && !!c.compare
                return (
                  <th key={c.key} scope="col" className={c.numeric ? s.num : undefined}
                    aria-sort={sortable ? (dir === 'asc' ? 'ascending' : dir === 'desc' ? 'descending' : 'none') : undefined}>
                    {sortable ? (
                      <button type="button" className={s.sort} data-dir={dir ?? undefined} onClick={() => cycle(c.key)}>
                        {c.header}
                        <span className={s.arrow} aria-hidden="true">{dir === 'asc' ? '↑' : dir === 'desc' ? '↓' : '↕'}</span>
                      </button>
                    ) : c.header}
                  </th>
                )
              })}
            </tr>
          </thead>
          <tbody>
            {shown.length === 0 && (
              <tr className={s.emptyRow}><td colSpan={columns.length}>{loading ? t.loading : empty ?? t.empty}</td></tr>
            )}
            {shown.map((row, i) => {
              const k = rowKey(row)
              const content = detail?.(row) ?? null
              const isOpen = content !== null && open.has(k)
              const ctx: RowContext = { open: isOpen, toggle: () => toggle(k), detailId: `dt-${caption.replace(/\W+/g, '-')}-${i}` }
              return (
                <Fragment key={k}>
                  <tr className={content !== null ? s.expandable : undefined} data-state={rowState?.(row)} data-open={isOpen || undefined}
                    aria-expanded={content !== null ? isOpen : undefined} onClick={content !== null ? (e) => onRowClick(e, k) : undefined}>
                    {columns.map((c) => (
                      <td key={c.key} className={c.numeric ? s.num : undefined} title={c.title?.(row)}>
                        {c.cell(row, ctx)}
                      </td>
                    ))}
                  </tr>
                  {isOpen && (
                    <tr className={s.detailRow} data-state={rowState?.(row)}>
                      <td colSpan={columns.length}><div id={ctx.detailId} className={s.detail}>{content}</div></td>
                    </tr>
                  )}
                </Fragment>
              )
            })}
          </tbody>
        </table>
      </div>
      <nav className={s.foot} aria-label={t.pages(caption)}>
        {sizes && (total === null || total > PAGE_SIZES[0]) && (
          <label className={s.sizes}>
            {t.rows}
            <select value={size} onChange={(e) => go.size(Number(e.target.value))}>
              {PAGE_SIZES.map((n) => <option key={n} value={n}>{n}</option>)}
            </select>
          </label>
        )}
        <span className={s.range}>{total === null ? t.rangeOpen(from, to) : t.range(from, to, total)}</span>
        {(hasPrev || hasNext) && (
          <span className={s.pager}>
            <button type="button" className={s.step} disabled={!hasPrev} onClick={go.first} aria-label={t.first}>«</button>
            <button type="button" className={s.step} disabled={!hasPrev} onClick={go.prev} aria-label={t.prev}>‹</button>
            <span className={s.pageno}>{pageCount !== null ? t.page(pageNo, pageCount) : t.pageOpen(pageNo)}</span>
            <button type="button" className={s.step} disabled={!hasNext} onClick={go.next} aria-label={t.next}>›</button>
          </span>
        )}
        {pages?.isFetching && !pages.isPending && <span className={s.busy} aria-live="polite">{t.loading}</span>}
      </nav>
    </div>
  )
}

/** The caret that opens a row's detail, for the keyboard (a click anywhere on the row does the same). */
export function RowToggle({ ctx, label }: { ctx: RowContext; label: string }) {
  return (
    <button type="button" className={s.caret} data-open={ctx.open || undefined} aria-expanded={ctx.open} aria-controls={ctx.open ? ctx.detailId : undefined}
      aria-label={label} onClick={ctx.toggle}>›</button>
  )
}

/** Compares two values for a column: numbers, then text (times in RFC 3339 order as text). */
export const by = <T,>(f: (r: T) => number | string | null | undefined) => (a: T, b: T): number => {
  const x = f(a), y = f(b)
  if (x == null || y == null) return x == null ? (y == null ? 0 : 1) : -1 // empty last
  return typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y))
}
