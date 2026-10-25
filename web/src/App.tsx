import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Route, Routes } from "react-router-dom";
import { ApiError } from "./api/client";
import { SessionProvider } from "./api/session";
import { Shell } from "./ui/shell";
import { NotFound, Soon } from "./pages/Soon";

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
        <Route path="changes/*" element={<Soon title="Değişiklikler" />} />
        <Route path="contracts/*" element={<Soon title="Kontratlar" />} />
        <Route path="runs/*" element={<Soon title="Run'lar" />} />
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
