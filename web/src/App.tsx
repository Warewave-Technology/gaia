import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { ApiError } from "./api/client";
import { SessionProvider } from "./api/session";
import { Shell } from "./ui/shell";
import { NotFound } from "./pages/NotFound";
import { ChangeList } from "./pages/changes/List";
import { ChangeDetail } from "./pages/changes/Detail";
import { RunList } from "./pages/runs/List";
import { RunDetail } from "./pages/runs/Detail";
import { ContractList } from "./pages/contracts/List";
import { ContractDetail } from "./pages/contracts/Detail";
import { Overview } from "./pages/overview/Overview";
import { CatalogList, ObjectPage } from "./pages/catalog/Catalog";
import { Profile } from "./pages/profile/Profile";
import { Infra } from "./pages/infra/Infra";
import { Operations } from "./pages/operations/Operations";
import { Access } from "./pages/access/Access";
import { Settings } from "./pages/settings/Settings";
import { VersionPage } from "./pages/settings/Versions";
import { ProposalPage } from "./pages/settings/Proposal";

export function makeClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // A 4xx will not get better by asking again.
        retry: (n, e) => !(e instanceof ApiError && e.status < 500) && n < 2,
        refetchOnWindowFocus: false,
      },
    },
  });
}

export function AppRoutes() {
  return (
    <Routes>
      <Route element={<Shell />}>
        <Route index element={<Overview />} />
        <Route path="changes" element={<ChangeList />} />
        <Route path="changes/:id" element={<ChangeDetail />} />
        <Route path="changes/settings/:id" element={<ProposalPage />} />
        <Route path="contracts" element={<ContractList />} />
        <Route path="contracts/:name" element={<ContractDetail />} />
        <Route path="runs" element={<RunList />} />
        <Route path="runs/:id" element={<RunDetail />} />
        <Route path="operations" element={<Operations />} />
        <Route path="catalog" element={<CatalogList />} />
        <Route path="catalog/:kind/:name" element={<ObjectPage />} />
        <Route path="infra" element={<Infra />} />
        <Route path="infra/:kind" element={<Infra />} />
        <Route path="infra/:kind/:name" element={<ObjectPage />} />
        <Route path="access" element={<Access />} />
        <Route path="access/:tab" element={<Access />} />
        <Route path="access/:kind/:name" element={<ObjectPage />} />
        <Route path="settings" element={<Settings />} />
        <Route path="settings/:tab" element={<Settings />} />
        <Route path="settings/versions/:version" element={<VersionPage />} />

        <Route path="settings/:kind/:name" element={<ObjectPage />} />
        <Route path="providers" element={<Navigate to="/settings/providers" replace />} />
        <Route path="sync" element={<Navigate to="/settings/sources" replace />} />
        <Route path="discovery" element={<Navigate to="/settings/discovery" replace />} />
        <Route path="profile" element={<Profile />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}

export function App({ client = makeClient() }: { client?: QueryClient }) {
  return (
    <QueryClientProvider client={client}>
      <BrowserRouter>
        <SessionProvider>
          <AppRoutes />
        </SessionProvider>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
