import { useT } from '../../i18n'
import s from './PointProof.module.css'

export type Proof = 'PROVEN' | 'BACKED_UP' | 'FAILED' | 'DAMAGED' | 'UNKNOWN'

/**
 * What a backup point is worth (ADR-007 C16): ✓ PROVEN only when a passing restore test proved it; BACKED UP (neutral,
 * no ✓, no hatching) when it was backed up and checked but not tested; ◆ FAILED when its test failed; ◆ DAMAGED when
 * its check found it damaged; ? UNKNOWN only for what should have been checked and could not be. `note` is the few
 * words beside it ("not tested", "return here"). The status-coloured ones carry `data-status` like a StatusChip.
 */
export function PointProof({ proof, note }: { proof: Proof; note?: string }) {
  const t = useT().proof
  const st = proof === 'PROVEN' ? 'OK' : proof === 'FAILED' || proof === 'DAMAGED' ? 'CRITICAL' : proof === 'UNKNOWN' ? 'UNKNOWN' : undefined
  const glyph = { PROVEN: '✓', BACKED_UP: '', FAILED: '◆', DAMAGED: '◆', UNKNOWN: '?' }[proof]
  return (
    <span className={s.wrap}>
      <span className={`${s.chip} ${s[proof.toLowerCase()]}`} data-status={st} data-proof={proof}>
        {glyph && <span className={s.g} aria-hidden="true">{glyph}</span>}
        {t[proof]}
      </span>
      {note && <span className={s.note}>{note}</span>}
    </span>
  )
}
