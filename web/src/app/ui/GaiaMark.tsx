import s from './GaiaMark.module.css'

/**
 * Gaia's mark: the G's stroke comes round to the point in its middle, the one point you can return to. The stroke
 * takes the text colour, the point the accent; public/favicon.svg is the same drawing on a tile.
 */
export function GaiaMark({ size = 18, className = '' }: { size?: number; className?: string }) {
  const w = size <= 20 ? 2.3 : 2 // a little heavier when small
  return (
    <svg className={`${s.mark} ${className}`} viewBox="0 0 24 24" width={size} height={size} aria-hidden="true">
      <path d="M18.13 6.86A8 8 0 1 0 20 12h-4" fill="none" stroke="currentColor" strokeWidth={w} strokeLinecap="round" strokeLinejoin="round" />
      <circle className={s.point} cx="12" cy="12" r={size <= 20 ? 2.6 : 2.4} />
    </svg>
  )
}
