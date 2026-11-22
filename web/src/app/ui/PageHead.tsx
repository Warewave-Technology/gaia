import type { ReactNode } from 'react'
import s from './PageHead.module.css'

/** Every v2 screen opens with the question it answers, in small caps, and the answer as the headline (T20 language). */
export function PageHead({ question, answer, lede, aside, size = 'md' }: {
  question: string
  answer: ReactNode
  lede?: ReactNode
  /** Facts beside the answer (the overview's proof capacity, ledger, kit). */
  aside?: ReactNode
  size?: 'md' | 'hero'
}) {
  return (
    <section className={`${s.head} ${aside ? s.split : ''}`}>
      <div className={s.main}>
        <p className={s.q}>{question}</p>
        <h1 className={s[size]}>{answer}</h1>
        {lede && <div className={s.lede}>{lede}</div>}
      </div>
      {aside}
    </section>
  )
}

/** Side facts: a term and its sentence, one per row (the overview's proof capacity, ledger, kit, running now). */
export function Facts({ children, label }: { children: ReactNode; label?: string }) {
  return <dl className={s.facts} aria-label={label}>{children}</dl>
}

export function Fact({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div>
      <dt>{term}</dt>
      <dd>{children}</dd>
    </div>
  )
}

/** A section's heading with its one-line explanation and controls. */
export function SectionHead({ title, hint, children, id }: { title: string; hint?: ReactNode; children?: ReactNode; id?: string }) {
  return (
    <div className={s.sh}>
      <h2 id={id}>{title}</h2>
      {children}
      {hint && <span className={s.hint}>{hint}</span>}
    </div>
  )
}
