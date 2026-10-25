// Kontratlar: every contract as a card, with its health and sync.
import { useQuery } from "@tanstack/react-query";
import { api } from "../../api/client";
import type { ContractSummary, Drift } from "../../api/types";
import { driftCount } from "../../lib/contract";
import { ContractTile } from "../../ui/contract-tile";
import { ErrorNote, Hint, Loading } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { HealthLegend, SyncLegend } from "../../ui/status";

export function useContracts() {
  const contracts = useQuery({ queryKey: ["contracts"], queryFn: () => api<ContractSummary[]>("/contracts"), refetchInterval: 30_000 });
  const drift = useQuery({ queryKey: ["drift"], queryFn: () => api<Drift[]>("/drift"), refetchInterval: 60_000 });
  return { contracts, drift };
}

export function ContractGrid({ contracts, drift }: { contracts: ContractSummary[]; drift: Drift[] | undefined }) {
  if (contracts.length === 0) return <div className="card empty">Gaia henüz bir kontrat uygulamadı.</div>;
  return (
    <section aria-label="Kontrat kartları" style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(320px, 1fr))", gap: 16 }}>
      {contracts.map((c) => <ContractTile key={c.name} c={c} drift={driftCount(drift, c.name)} />)}
    </section>
  );
}

export function ContractList() {
  const { contracts, drift } = useContracts();
  return (
    <Page crumbs="Kontratlar">
      <div className="title-row">
        <h1>Kontratlar</h1>
        <Hint>Kontrat, bir sunucu grubunun istenen durumunu anlatan tek YAML dosyasıdır. Gaia onu gerçek kaynaklara (VM, IP, izleme, konfigürasyon) çevirir ve öyle tutar.</Hint>
        <span className="row muted" style={{ gap: 6, fontSize: 13 }}>Sağlık<Hint><HealthLegend /></Hint> Sync<Hint><SyncLegend /></Hint></span>
      </div>
      {contracts.isPending ? <Loading /> : contracts.error ? <ErrorNote error={contracts.error} /> : <ContractGrid contracts={contracts.data} drift={drift.data} />}
    </Page>
  );
}
