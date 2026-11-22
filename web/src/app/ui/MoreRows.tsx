import { useEffect, useRef } from 'react'
import { useT } from '../../i18n'
import { Button } from './Button'
import s from './MoreRows.module.css'

/**
 * The end of a paged list: how many rows are shown of how many, and the next page, loaded when this line scrolls
 * into view (or with the button, for a keyboard). A list in the middle of a screen sets `auto` false: it grows only
 * when asked, so the sections below it stay where they are.
 */
export function MoreRows({ shown, total, more, loading, onMore, auto = true }: {
  shown: number
  total: number
  more: boolean
  loading: boolean
  onMore: () => void
  auto?: boolean
}) {
  const t = useT().table
  const ref = useRef<HTMLDivElement>(null)
  const load = useRef(onMore)
  load.current = onMore
  useEffect(() => {
    const el = ref.current
    if (!auto || !more || !el || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && load.current(), { rootMargin: '600px 0px' })
    io.observe(el)
    return () => io.disconnect()
  }, [auto, more, shown])
  return (
    <div ref={ref} className={s.more}>
      <span>{t.shown(shown, total)}</span>
      {more && (
        <Button variant="quiet" onClick={onMore} disabled={loading}>
          {loading ? t.loadingMore : t.more}
        </Button>
      )}
    </div>
  )
}
