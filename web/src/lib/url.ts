import { useCallback, useMemo } from 'react'
import { useSearchParams } from 'react-router'

/**
 * A screen's filters live in its URL (T27), so a filtered view can be shared and the back button undoes a filter:
 * the values of `keys` ("" when absent), and a setter that changes some and drops the empty ones.
 */
export function useFilters<K extends string>(keys: readonly K[]) {
  const [sp, setSp] = useSearchParams()
  const values = useMemo(() => Object.fromEntries(keys.map((k) => [k, sp.get(k) ?? ''])) as Record<K, string>, [sp, keys])
  const set = useCallback(
    (patch: Partial<Record<K, string>>) =>
      setSp((old) => {
        const next = new URLSearchParams(old)
        for (const [k, v] of Object.entries(patch) as [string, string | undefined][]) {
          if (v) next.set(k, v)
          else next.delete(k)
        }
        return next
      }),
    [setSp],
  )
  return [values, set] as const
}

/** A link's search part from filters, the empty ones left out. */
export function query(params: Record<string, string | number | null | undefined>): string {
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== null && v !== undefined && v !== '') sp.set(k, String(v))
  }
  const s = sp.toString()
  return s ? `?${s}` : ''
}
