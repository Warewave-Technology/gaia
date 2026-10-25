import { Page } from "../ui/shell";

// A screen that comes in a later step of phase 5.
export function Soon({ title }: { title: string }) {
  return (
    <Page crumbs={title}>
      <h1>{title}</h1>
      <div className="notice">Bu ekran yapım aşamasında.</div>
    </Page>
  );
}

export function NotFound() {
  return (
    <Page crumbs="Bulunamadı">
      <h1>Bulunamadı</h1>
      <div className="notice warn">Böyle bir sayfa yok.</div>
    </Page>
  );
}
