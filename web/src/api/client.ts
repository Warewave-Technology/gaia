import createClient from 'openapi-fetch'
import type { paths } from './schema'

// Typed client generated from internal/server/openapi.json (`npm run gen-api`). The browser's session is an HttpOnly
// cookie: the scripts never see it.
export const api = createClient<paths>({ credentials: 'same-origin' })
