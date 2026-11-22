// The step-up dialog's clock (ADR-008 §4 and amendment 4): the dialog shows the action and its target, asks for the
// factor, then the user presses the action's button. A step-up lasts 120 s on the server from the moment it was asked
// for (internal/server/stepup.go), and a factor is good for 120 s: if more than that passes before the button, the
// factor is asked again with a new step-up. Kept apart from the dialog so the rule can be tested on its own.

/** How long a step-up and its factor are good for (ADR-008 §4). */
export const STEP_UP_WINDOW_S = 120
/** Kept back from the window for the request's way to the server and the two clocks: the button turns before the
 * server would refuse. */
export const STEP_UP_MARGIN_S = 5

export type StepUpPhase =
  /** No factor yet: `left` seconds until this step-up is too old to use. */
  | { phase: 'factor'; left: number }
  /** A factor was given: the action's button works for `left` more seconds. */
  | { phase: 'ready'; left: number }
  /** Too late: nothing was sent, the factor must be given again for a new step-up. `afterFactor`: one had been given. */
  | { phase: 'expired'; afterFactor: boolean }

/**
 * Where a step-up stands at `now` (ms): asked for at `askedAt`, its factor given at `factorAt` (null: not yet).
 * The button may be pressed until 120 s after the step-up was asked for and after the factor was given, whichever
 * comes first, less the margin.
 */
export function stepUpPhase(askedAt: number, factorAt: number | null, now: number): StepUpPhase {
  const w = (STEP_UP_WINDOW_S - STEP_UP_MARGIN_S) * 1000
  const deadline = Math.min(askedAt + w, factorAt === null ? Infinity : factorAt + w)
  if (now >= deadline) return { phase: 'expired', afterFactor: factorAt !== null }
  const left = Math.ceil((deadline - now) / 1000)
  return factorAt === null ? { phase: 'factor', left } : { phase: 'ready', left }
}
