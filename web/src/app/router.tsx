import { createBrowserRouter } from 'react-router'
import { RequireAuth } from './layout/RequireAuth'
import { StepUpProvider } from './layout/StepUp'
import { ApproveCLI, ApproveStepUp } from './pages/Approve'
import { RouteError } from './layout/States'
import { Shell } from './layout/Shell'
import { ActivityPage } from './pages/ActivityPage'
import { Capacity } from './pages/Capacity'
import { EvidencePage } from './pages/EvidencePage'
import { Inventory } from './pages/Inventory'
import { MigrationPage } from './pages/MigrationPage'
import { Login } from './pages/Login'
import { NotFound } from './pages/NotFound'
import { Overview } from './pages/Overview'
import { VMPage } from './pages/VMPage'

// The server sends index.html for every path without a file extension outside /api (internal/server/ui.go).
export const router = createBrowserRouter([
  { path: '/login', element: <Login />, errorElement: <RouteError /> },
  { path: '/approve-cli', element: <RequireAuth><StepUpProvider><ApproveCLI /></StepUpProvider></RequireAuth>, errorElement: <RouteError /> },
  { path: '/approve-step-up', element: <RequireAuth><ApproveStepUp /></RequireAuth>, errorElement: <RouteError /> },
  {
    path: '/',
    element: <RequireAuth><StepUpProvider><Shell /></StepUpProvider></RequireAuth>,
    errorElement: <RouteError />,
    children: [
      // each screen has its own: when one fails to draw, the bar stays
      { index: true, element: <Overview />, errorElement: <RouteError /> },
      { path: 'vms/:id', element: <VMPage />, errorElement: <RouteError /> },
      { path: 'capacity', element: <Capacity />, errorElement: <RouteError /> },
      { path: 'activity', element: <ActivityPage />, errorElement: <RouteError /> },
      { path: 'migration', element: <MigrationPage />, errorElement: <RouteError /> },
      { path: 'inventory', element: <Inventory />, errorElement: <RouteError /> },
      { path: 'inventory/:tab', element: <Inventory />, errorElement: <RouteError /> },
      { path: 'evidence', element: <EvidencePage />, errorElement: <RouteError /> },
      { path: '*', element: <NotFound /> },
    ],
  },
])
