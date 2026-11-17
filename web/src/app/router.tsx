import { createBrowserRouter } from 'react-router'
import { RequireAuth } from './layout/RequireAuth'
import { RouteError } from './layout/States'
import { Shell } from './layout/Shell'
import { EvidencePage } from './pages/EvidencePage'
import { Login } from './pages/Login'
import { NotFound } from './pages/NotFound'
import { Overview } from './pages/Overview'
import { VMPage } from './pages/VMPage'

// The server sends index.html for every path without a file extension outside /api (internal/server/ui.go).
export const router = createBrowserRouter([
  { path: '/login', element: <Login />, errorElement: <RouteError /> },
  {
    path: '/',
    element: <RequireAuth><Shell /></RequireAuth>,
    errorElement: <RouteError />,
    children: [
      // each screen has its own: when one fails to draw, the bar stays
      { index: true, element: <Overview />, errorElement: <RouteError /> },
      { path: 'vms/:id', element: <VMPage />, errorElement: <RouteError /> },
      { path: 'evidence', element: <EvidencePage />, errorElement: <RouteError /> },
      { path: '*', element: <NotFound /> },
    ],
  },
])
