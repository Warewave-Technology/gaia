import { Page } from "../ui/shell";

export function NotFound() {
  return (
    <Page crumbs="Bulunamadı">
      <h1>Bulunamadı</h1>
      <div className="notice warn">Böyle bir sayfa yok.</div>
    </Page>
  );
}
