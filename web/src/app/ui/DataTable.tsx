import { Fragment, useMemo, useState, type MouseEvent, type ReactNode } from 'react'
import { useT } from '../../i18n'
import s from './DataTable.module.css'

/** What a cell gets besides its row: whether the row's detail is open, and how to open or close it. */
export type RowContext = { open: boolean; toggle: () => void; detailId: string }

export type Column<T> = {
  key: string
  header: string
  /** Numbers and times: right-aligned, tabular figures. */
  numeric?: boolean
  /** The cell may wrap and is cut to one line with an ellipsis (a reason, a long name). */
  clip?: boolean
  /** Orders two rows by this column, ascending. Without it the column cannot be sorted. */
  compare?: (a: T, b: T) => number
  cell: (row: T, ctx: RowContext) => ReactNode
}

export type Sort = { key: string; dir: 'asc' | 'desc' }

/**
 * A table of rows the screen has loaded (after heimdall's ui/DataTable): headers that sort when a column can be
 * ordered (ascending, descending, then the order the rows came in), a detail row under a row that has one (a click
 * on the row opens it), and pages of `pageSize` rows so a long record does not push the screen down.
 *
 * Sorting and paging act on the rows that are here; the caller says above the table what they are.
 */
export function DataTable<T>({
  caption,
  columns,
  rows,
  rowKey,
  detail,
  rowState,
  pageSize = 10,
  minWidth,
}: {
  /** The table's name (visually hidden). */
  caption: string
  columns: readonly Column<T>[]
  rows: readonly T[]
  rowKey: (row: T) => string
  /** The content of the detail row under `row`; null: this row has none. */
  detail?: (row: T) => ReactNode | null
  /** A word for the row's state, set as `data-state` (styled here: "bad", "mark"). */
  rowState?: (row: T) => string | undefined
  pageSize?: number
  /** The table scrolls sideways in its frame below this width (px). */
  minWidth?: number
}) {
  const t = useT().table
  const [sort, setSort] = useState<Sort | null>(null)
  const [page, setPage] = useState(0)
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set())
  const toggle = (k: string) =>
    setOpen((old) => {
      const next = new Set(old)
      if (!next.delete(k)) next.add(k)
      return next
    })

  const ordered = useMemo(() => {
    const col = sort ? columns.find((c) => c.key === sort.key) : undefined
    if (!sort || !col?.compare) return rows
    const sign = sort.dir === 'asc' ? 1 : -1
    // a stable sort: rows that compare equal keep the order they came in
    return [...rows].sort((a, b) => sign * col.compare!(a, b))
  }, [rows, columns, sort])

  const pages = Math.max(1, Math.ceil(ordered.length / pageSize))
  const at = Math.min(page, pages - 1) // the rows can shrink under a page (a refresh)
  const shown = ordered.slice(at * pageSize, (at + 1) * pageSize)

  const cycle = (key: string) => {
    setPage(0)
    if (sort?.key !== key) setSort({ key, dir: 'asc' })
    else if (sort.dir === 'asc') setSort({ key, dir: 'desc' })
    else setSort(null)
  }

  const onRowClick = (e: MouseEvent, k: string) => {
    // Links and buttons handle their own clicks; selecting text is not a click.
    if ((e.target as HTMLElement).closest('a, button')) return
    if (window.getSelection()?.toString()) return
    toggle(k)
  }

  return (
    <div className={s.frame}>
      <div className={s.scroll}>
        <table className={s.table} style={minWidth ? { minWidth } : undefined}>
          <caption className={s.hidden}>{caption}</caption>
          <thead>
            <tr>
              {columns.map((c) => {
                const dir = sort?.key === c.key ? sort.dir : null
                return (
                  <th key={c.key} scope="col" className={c.numeric ? s.num : undefined}
                    aria-sort={c.compare ? (dir === 'asc' ? 'ascending' : dir === 'desc' ? 'descending' : 'none') : undefined}>
                    {c.compare ? (
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
                      <td key={c.key} className={[c.numeric && s.num, c.clip && s.clip].filter(Boolean).join(' ') || undefined}>
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
      {pages > 1 && (
        <nav className={s.pager} aria-label={t.pages(caption)}>
          <span className={s.range}>{t.range(at * pageSize + 1, at * pageSize + shown.length, ordered.length)}</span>
          <button type="button" className={s.step} disabled={at === 0} onClick={() => setPage(at - 1)} aria-label={t.prev}>‹</button>
          <span className={s.pageno}>{t.page(at + 1, pages)}</span>
          <button type="button" className={s.step} disabled={at >= pages - 1} onClick={() => setPage(at + 1)} aria-label={t.next}>›</button>
        </nav>
      )}
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
