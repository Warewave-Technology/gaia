import { keepPreviousData, useQuery, type QueryKey } from '@tanstack/react-query'
import { useState } from 'react'
import { LIST_REFRESH } from './paged'

/** One page of a list as a data-table shows it: its rows, the whole list's size when the API knows it, and the cursor
 * of the page after it ("" ends the list). */
export type Page<R> = { rows: readonly R[]; total: number | null; next: string }

export const PAGE_SIZES = [25, 50, 100] as const

/**
 * A list the server pages (ADR-007 C1, docs/design/v3 rule 1): one page of `size` rows at a time, never more.
 * The cursors of the pages already seen are kept, so ‹ goes back without asking twice; a new `key` (another filter)
 * starts at the first page again. The page refreshes like the screens do.
 */
export function usePages<R>(key: QueryKey, fetch: (cursor: string, limit: number) => Promise<Page<R>>, initialSize = 25,
  refetchInterval: number | false = LIST_REFRESH) {
  const id = JSON.stringify(key)
  const [st, setSt] = useState({ id, size: initialSize, cursors: [''] })
  // A new filter: back to the first page (set during render, React's way to reset state from props).
  if (st.id !== id) setSt({ id, size: st.size, cursors: [''] })
  const cursor = st.cursors[st.cursors.length - 1]
  const q = useQuery({
    queryKey: [...key, 'page', st.size, cursor],
    queryFn: () => fetch(cursor, st.size),
    placeholderData: keepPreviousData,
    refetchInterval,
    refetchIntervalInBackground: true,
  })
  const page = st.cursors.length
  const total = q.data?.total ?? null
  return {
    ...q,
    rows: q.data?.rows ?? [],
    total,
    page,
    pages: total !== null ? Math.max(1, Math.ceil(total / st.size)) : null,
    from: total === 0 ? 0 : (page - 1) * st.size + 1,
    size: st.size,
    hasPrev: page > 1,
    hasNext: !!q.data?.next && !q.isPlaceholderData,
    next: () => q.data?.next && setSt((o) => ({ ...o, cursors: [...o.cursors, q.data!.next] })),
    prev: () => setSt((o) => ({ ...o, cursors: o.cursors.length > 1 ? o.cursors.slice(0, -1) : o.cursors })),
    first: () => setSt((o) => ({ ...o, cursors: [''] })),
    setSize: (size: number) => setSt((o) => ({ ...o, size, cursors: [''] })),
  }
}

export type Pages<R> = ReturnType<typeof usePages<R>>
