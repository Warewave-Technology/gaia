import { useT } from '../../i18n'

// T27: built from docs/design/v2 (a placeholder until the screen is built).
export function Capacity() {
  return <main style={{ padding: '30px 28px' }}><h1>{useT().capacity.title}</h1></main>
}
