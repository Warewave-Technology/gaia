import { useT } from '../../i18n'
import { ApiError } from '../api/errors'
import { Button, Notice } from '../ui'
import s from './States.module.css'

/** While a screen's data is on its way. */
export function Loading() {
  return <p className={s.loading} aria-busy="true">{useT().app.loading}</p>
}

/** A screen whose data could not be loaded: what happened, in words, and a way to try again. */
export function Failed({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const t = useT()
  let msg = t.errors.generic
  if (error instanceof ApiError) {
    msg = error.status === 0 ? t.errors.offline : error.status === 403 ? t.errors.forbidden : error.message
  }
  return (
    <div className={s.failed}>
      <Notice tone="critical" action={onRetry && <Button variant="quiet" onClick={onRetry}>{t.app.retry}</Button>}>{msg}</Notice>
    </div>
  )
}
