import createClient from 'openapi-fetch'
import type { paths } from './schema'

// Typed client generated from internal/server/openapi.json (`npm run gen-api`). The browser's session is an HttpOnly
// cookie: the scripts never see it.
//
// ADR-008 §5: only what the user does keeps a session alive. The first request after a click or a key press is the
// user's; every other one (auto-refresh, polling, a refetch nobody asked for) carries X-Gaia-Background: 1, which the
// server does not count as activity.
let lastInteraction = 0
let lastActive = 0
if (typeof window !== 'undefined') {
  for (const ev of ['pointerdown', 'keydown']) {
    window.addEventListener(ev, () => { lastInteraction = Date.now() }, { capture: true, passive: true })
  }
}

/** Marks a request as the user's or as the UI's own. */
export function markActivity(headers: Headers) {
  if (lastInteraction > lastActive) lastActive = Date.now()
  else headers.set('X-Gaia-Background', '1')
}

function gaiaFetch(req: Request): Promise<Response> {
  markActivity(req.headers)
  return fetch(req)
}

export const api = createClient<paths>({ credentials: 'same-origin', fetch: gaiaFetch })
