import chip from '../../ui/FilterChip.module.css'
import s from './PickChip.module.css'

/**
 * A filter chip that picks one of many (a host, a policy, a tag): dashed "+ Host" while unset, solid "Host: kvm-07"
 * with a × once set. It is a native select, so the keyboard and screen readers know it as one.
 */
export function PickChip({ label, placeholder, value, options, display, onChange, clearLabel }: {
  /** The select's accessible name. */
  label: string
  /** The words while unset: "+ Host". */
  placeholder: string
  value: string
  options: readonly (readonly [string, string])[]
  /** The words once set: "Host: kvm-07". */
  display: (name: string) => string
  onChange: (v: string) => void
  clearLabel: string
}) {
  const set = value !== ''
  const name = options.find(([v]) => v === value)?.[1] ?? value
  const known = options.some(([v]) => v === value)
  return (
    <span className={s.wrap}>
      <select className={`${chip.chip} ${s.select}`} data-set={set || undefined} aria-label={label} value={value} onChange={(e) => onChange(e.target.value)}>
        {set ? <option value={value}>{display(name)}</option> : <option value="">{placeholder}</option>}
        {set && <option value="">{clearLabel}</option>}
        {options.filter(([v]) => !set || v !== value || !known).map(([v, n]) => <option key={v} value={v}>{n}</option>)}
      </select>
      {set && <button type="button" className={s.x} aria-label={clearLabel} onClick={() => onChange('')}>×</button>}
    </span>
  )
}
