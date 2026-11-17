import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from 'react-router/dom'
import './styles/fonts.css'
import './styles/tokens.css'
import './styles/global.css'
import { ApiError } from './app/api/errors'
import { ME_KEY } from './app/api/session'
import { router } from './app/router'

// A session that ended (expired, logged out elsewhere, user disabled): forget the user, RequireAuth sends to /login.
const queryClient: QueryClient = new QueryClient({
  queryCache: new QueryCache({
    onError: (err) => {
      if (err instanceof ApiError && err.status === 401) queryClient.setQueryData(ME_KEY, null)
    },
  }),
  defaultOptions: {
    queries: { retry: (n, err) => !(err instanceof ApiError && err.status >= 400 && err.status < 500) && n < 2 },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
