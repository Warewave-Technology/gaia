import type { components } from '../../api/schema'

export type ErrorBody = components['schemas']['apiError']

/** Every failed API call ends as one ApiError: HTTP status (0: no answer), the API's error code, its message, and the
 * Retry-After seconds of a 429. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly retryAfter: number | null
  readonly reason: string | undefined

  constructor(status: number, code: string, message: string, retryAfter: number | null = null, reason?: string) {
    super(message || `${status} ${code}`)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.retryAfter = retryAfter
    this.reason = reason
  }
}

/** Turns an openapi-fetch result into its data, or throws ApiError. */
export async function unwrap<T>(p: Promise<{ data?: T; error?: unknown; response: Response }>): Promise<T> {
  let r: { data?: T; error?: unknown; response: Response }
  try {
    r = await p
  } catch (e) {
    throw new ApiError(0, 'offline', e instanceof Error ? e.message : String(e))
  }
  if (r.response.ok) return r.data as T
  const body = (r.error ?? {}) as Partial<ErrorBody>
  const ra = r.response.headers.get('Retry-After')
  throw new ApiError(r.response.status, body.code ?? 'internal', body.error ?? r.response.statusText, ra ? Number(ra) : null, body.reason)
}
