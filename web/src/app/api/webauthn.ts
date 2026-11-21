// WebAuthn in the browser (ADR-008 §3, §4): the server's options carry base64url strings where the browser wants
// bytes, and the browser's answer goes back as JSON with base64url strings.

type Json = Record<string, unknown>

function fromB64url(s: string): ArrayBuffer {
  const b = atob(s.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((s.length + 3) % 4))
  const out = new Uint8Array(b.length)
  for (let i = 0; i < b.length; i++) out[i] = b.charCodeAt(i)
  return out.buffer
}

function toB64url(b: ArrayBuffer | null): string | null {
  if (!b) return null
  let s = ''
  for (const x of new Uint8Array(b)) s += String.fromCharCode(x)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

const creds = (l: unknown) => (Array.isArray(l) ? l.map((c: Json) => ({ ...c, id: fromB64url(c.id as string) })) : undefined)

/** Whether this browser can use a security key or the platform's authenticator here (a secure context). */
export const webauthnAvailable = () => typeof window !== 'undefined' && !!window.PublicKeyCredential && window.isSecureContext

/** Enrols a key: the server's CredentialCreation in, the PublicKeyCredential as JSON out. */
export async function createCredential(options: unknown): Promise<Json> {
  const pk = (options as { publicKey: Json }).publicKey
  const user = pk.user as Json
  const publicKey = { ...pk, challenge: fromB64url(pk.challenge as string), user: { ...user, id: fromB64url(user.id as string) },
    excludeCredentials: creds(pk.excludeCredentials) } as unknown as PublicKeyCredentialCreationOptions
  const c = (await navigator.credentials.create({ publicKey })) as PublicKeyCredential | null
  if (!c) throw new Error('no credential')
  const r = c.response as AuthenticatorAttestationResponse
  return { id: c.id, rawId: toB64url(c.rawId), type: c.type, clientExtensionResults: c.getClientExtensionResults(),
    response: { clientDataJSON: toB64url(r.clientDataJSON), attestationObject: toB64url(r.attestationObject),
      transports: typeof r.getTransports === 'function' ? r.getTransports() : [] } }
}

/** Answers an assertion: the server's CredentialAssertion in, the PublicKeyCredential as JSON out. */
export async function getAssertion(options: unknown): Promise<Json> {
  const pk = (options as { publicKey: Json }).publicKey
  const publicKey = { ...pk, challenge: fromB64url(pk.challenge as string), allowCredentials: creds(pk.allowCredentials) } as unknown as
    PublicKeyCredentialRequestOptions
  const c = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential | null
  if (!c) throw new Error('no credential')
  const r = c.response as AuthenticatorAssertionResponse
  return { id: c.id, rawId: toB64url(c.rawId), type: c.type, clientExtensionResults: c.getClientExtensionResults(),
    response: { clientDataJSON: toB64url(r.clientDataJSON), authenticatorData: toB64url(r.authenticatorData), signature: toB64url(r.signature),
      userHandle: toB64url(r.userHandle) } }
}

/** base64url of a JSON value (the step-up proof header). */
export function jsonB64url(v: unknown): string {
  return toB64url(new TextEncoder().encode(JSON.stringify(v)).buffer as ArrayBuffer) ?? ''
}
