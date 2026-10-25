// Contracts: every contract as a card, with its health and sync.
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
  if (contracts.length === 0) return <div className="card empty">Gaia has applied no contract yet.</div>;
  return (
    <section aria-label="Contracts" style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(320px, 1fr))", gap: 16 }}>
      {contracts.map((c) => <ContractTile key={c.name} c={c} drift={driftCount(drift, c.name)} />)}
    </section>
  );
}

export function ContractList() {
  const { contracts, drift } = useContracts();
  return (
    <Page crumbs="Contracts">
      <div className="title-row">
        <h1>Contracts</h1>
        <Hint>A contract describes the desired state of a group of servers. Gaia turns it into real resources (VMs, addresses, monitoring, configuration) and keeps them so.</Hint>
        <span className="row muted" style={{ gap: 6, fontSize: 13 }}>Health<Hint><HealthLegend /></Hint> Sync<Hint><SyncLegend /></Hint></span>
      </div>
      {contracts.isPending ? <Loading /> : contracts.error ? <ErrorNote error={contracts.error} /> : <ContractGrid contracts={contracts.data} drift={drift.data} />}
    </Page>
  );
}
