import { Page } from "../ui/shell";

export function NotFound() {
  return (
    <Page crumbs="Not found">
      <h1>Not found</h1>
      <div className="notice warn">There is no such page.</div>
    </Page>
  );
}
