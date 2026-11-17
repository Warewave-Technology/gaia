import access from './access.json' with { type: 'json' }

export type Ability = keyof typeof access.abilities
export const roles = ['auditor', 'operator', 'admin'] as const

/** What a role may do on the screens, from access.json (the API checks it again). */
export function can(u: { role: string } | null | undefined, what: Ability): boolean {
  return !!u && (access.abilities[what] as string[]).includes(u.role)
}

export { access }
