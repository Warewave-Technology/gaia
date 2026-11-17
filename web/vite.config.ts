import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

// The web UI (T20). `npm run build` writes dist/, which the gaia binary embeds (web/embed.go) and which is committed:
// building Gaia needs no Node. In dev, /api goes to a gaia server: GAIA_API (default https://localhost:8443).
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '.', '')
  return {
    plugins: [react()],
    build: { outDir: 'dist', emptyOutDir: true, assetsInlineLimit: 0, sourcemap: false },
    server: {
      proxy: {
        // GAIA_API_TOKEN (dev only): a bearer token the proxy adds, for working against a lab server without typing a
        // password into the page.
        '/api': {
          target: env.GAIA_API || 'https://localhost:8443', changeOrigin: true, secure: false,
          headers: env.GAIA_API_TOKEN ? { Authorization: `Bearer ${env.GAIA_API_TOKEN}` } : undefined,
        },
      },
    },
  }
})
