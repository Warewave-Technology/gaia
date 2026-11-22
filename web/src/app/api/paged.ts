import { useInfiniteQuery, type QueryKey } from '@tanstack/react-query'

/** A screen's lists refresh like the screens (views.ts): the status changes with time alone. */
export const LIST_REFRESH = 15_000

/**
 * A list the API answers in pages (ADR-007 C1: thousands of rows are never loaded at once): the first page, then
 * the next ones as the table scrolls (MoreRows). Each answer carries `next`, passed back as the cursor; "" ends it.
 */
export function usePaged<P extends { next: string }, R>(
  key: QueryKey,
  page: (cursor: string) => Promise<P>,
  rows: (p: P) => readonly R[],
  refetchInterval: number | false = LIST_REFRESH,
) {
  const q = useInfiniteQuery({
    queryKey: key,
    queryFn: ({ pageParam }) => page(pageParam),
    initialPageParam: '',
    getNextPageParam: (last) => last.next || undefined,
    refetchInterval,
    refetchIntervalInBackground: true,
  })
  return {
    ...q,
    /** The first page: totals and the clock come with it. */
    head: q.data?.pages[0],
    rows: q.data?.pages.flatMap(rows) ?? [],
  }
}
