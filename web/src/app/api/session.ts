import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { components } from '../../api/schema'
import { ApiError, unwrap } from './errors'

export type User = components['schemas']['User']
export const ME_KEY = ['me'] as const

/** The logged-in user, or null when there is no session. */
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

export function useLogin() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: { username: string; password: string }) =>
      unwrap(api.POST('/api/v1/login', { body: { username: v.username, password: v.password, browser: true } })),
    onSuccess: (r) => qc.setQueryData(ME_KEY, r.user),
  })
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
