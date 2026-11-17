import type { ButtonHTMLAttributes, AnchorHTMLAttributes } from 'react'
import s from './Button.module.css'

type Variant = 'primary' | 'quiet'

/** A button with words (never an icon alone); not coloured: colour is for status. */
export function Button({ variant = 'primary', className = '', ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant }) {
  return <button {...rest} className={`${s.btn} ${s[variant]} ${className}`} />
}

/** A link that looks like a button (a download, a navigation). */
export function ButtonLink({ variant = 'primary', className = '', ...rest }: AnchorHTMLAttributes<HTMLAnchorElement> & { variant?: Variant }) {
  return <a {...rest} className={`${s.btn} ${s[variant]} ${className}`} />
}
