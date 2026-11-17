import { createBrowserRouter } from 'react-router'
import { RequireAuth } from './layout/RequireAuth'
import { Shell } from './layout/Shell'
import { EvidencePage } from './pages/EvidencePage'
import { Login } from './pages/Login'
import { NotFound } from './pages/NotFound'
import { Overview } from './pages/Overview'
import { VMPage } from './pages/VMPage'

// The server sends index.html for every path without a file extension outside /api (internal/server/ui.go).
export const router = createBrowserRouter([
  { path: '/login', element: <Login /> },
  {
    path: '/',
    element: <RequireAuth><Shell /></RequireAuth>,
    children: [
      { index: true, element: <Overview /> },
      { path: 'vms/:id', element: <VMPage /> },
      { path: 'evidence', element: <EvidencePage /> },
      { path: '*', element: <NotFound /> },
    ],
  },
])
