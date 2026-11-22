import s from './Seg.module.css'

/** A row of buttons of which one is chosen (group by host / policy / tag; 24 h / 7 days). */
export function Seg<V extends string>({ label, value, options, onChange }: {
  label: string
  value: V
  options: readonly (readonly [V, string])[]
  onChange: (v: V) => void
}) {
  return (
    <div className={s.seg} role="group" aria-label={label}>
      {options.map(([v, text]) => (
        <button key={v} type="button" aria-pressed={v === value} onClick={() => onChange(v)}>{text}</button>
      ))}
    </div>
  )
}

/** A filter as a button-like select: "Host: any". The first option is the empty value. */
export function Pick({ label, value, options, onChange }: {
  label: string
  value: string
  options: readonly (readonly [string, string])[]
  onChange: (v: string) => void
}) {
  return (
    <label className={`${s.pick} ${value ? s.set : ''}`}>
      <span>{label}</span>
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        {options.map(([v, text]) => <option key={v} value={v}>{text}</option>)}
      </select>
    </label>
  )
}
