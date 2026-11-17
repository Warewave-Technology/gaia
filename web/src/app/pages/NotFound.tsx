import { Link } from 'react-router'
import { useT } from '../../i18n'

export function NotFound() {
  const t = useT().notFound
  return (
    <main style={{ maxWidth: 900, margin: '60px auto', padding: '0 32px' }}>
      <h1>{t.title}</h1>
      <p><Link to="/">{t.back}</Link></p>
    </main>
  )
}
