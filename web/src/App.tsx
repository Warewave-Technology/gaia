import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Route, Routes } from "react-router-dom";
import { ApiError } from "./api/client";
import { SessionProvider } from "./api/session";
import { Shell } from "./ui/shell";
import { NotFound, Soon } from "./pages/Soon";
import { ChangeList } from "./pages/changes/List";
import { ChangeDetail } from "./pages/changes/Detail";
import { RunList } from "./pages/runs/List";
import { RunDetail } from "./pages/runs/Detail";
import { ContractList } from "./pages/contracts/List";
import { ContractDetail } from "./pages/contracts/Detail";

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
        <Route index element={<Soon title="Genel bakış" />} />
        <Route path="changes" element={<ChangeList />} />
        <Route path="changes/:id" element={<ChangeDetail />} />
        <Route path="contracts" element={<ContractList />} />
        <Route path="contracts/:name" element={<ContractDetail />} />
        <Route path="runs" element={<RunList />} />
        <Route path="runs/:id" element={<RunDetail />} />
        <Route path="catalog/*" element={<Soon title="Katalog" />} />
        <Route path="providers" element={<Soon title="Provider'lar" />} />
        <Route path="sync" element={<Soon title="Sync" />} />
        <Route path="discovery" element={<Soon title="Keşif" />} />
        <Route path="profile" element={<Soon title="Profil" />} />
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
