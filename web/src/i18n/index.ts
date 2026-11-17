import { en, type Messages } from './en'

export type { Messages }

/** The UI's strings. One language today; the hook keeps components independent of where the strings come from. */
export function useT(): Messages {
  return en
}
