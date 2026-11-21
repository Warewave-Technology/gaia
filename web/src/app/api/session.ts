import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { ApiError, unwrap } from './errors'

export type User = components['schemas']['Me']
export type SigninStep = components['schemas']['SigninStep']
export type EnrolChallenge = components['schemas']['EnrolChallenge']
export const ME_KEY = ['me'] as const

/** Why the last session ended (idle, absolute, ...), for the sign-in page; set when a request finds it ended. */
export let endedReason: string | null = null
export function sessionEnded(reason: string | null) {
  endedReason = reason
}

/** The signed-in user and the session, or null when there is no session. */
export function useMe() {
  return useQuery({
    queryKey: ME_KEY,
    queryFn: async (): Promise<User | null> => {
      try {
        return await unwrap(api.GET('/api/v1/me'))
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return null
        throw e
      }
    },
    staleTime: 60_000,
  })
}

/** How one may sign in here (single sign-on on or off). */
export function useSigninOptions() {
  return useQuery({ queryKey: ['signin-options'], queryFn: () => unwrap(api.GET('/api/v1/signin-options')), staleTime: 60_000 })
}

/** The steps of a local sign-in (ADR-008): each answer says what comes next; the last sets the session cookie. */
export const signin = {
  password: (username: string, password: string) => unwrap(api.POST('/api/v1/login', { body: { username, password, browser: true } })),
  factor: (signin_token: string, method: string, answer: { code?: string; webauthn?: unknown }) =>
    unwrap(api.POST('/api/v1/login/factor', { body: { signin_token, method, code: answer.code, webauthn: answer.webauthn as never } })),
  enrol: (signin_token: string, kind: string) => unwrap(api.POST('/api/v1/login/enrol', { body: { signin_token, kind } })),
  enrolFinish: (signin_token: string, kind: string, answer: { code?: string; webauthn?: unknown; name?: string }) =>
    unwrap(api.POST('/api/v1/login/enrol/finish', { body: { signin_token, kind, name: answer.name, code: answer.code, webauthn: answer.webauthn as never } })),
  newPassword: (signin_token: string, new_password: string) => unwrap(api.POST('/api/v1/login/password', { body: { signin_token, new_password } })),
}

export function useLogout() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/logout')),
    onSettled: () => {
      qc.clear()
      qc.setQueryData(ME_KEY, null)
    },
  })
}

export { can } from '../access'
