// A contract's health and sync as the screens show them.
import type { ContractSummary, Drift, Host, RunStatus } from "../api/types";
import type { SyncState } from "../ui/status";

export type Health = "green" | "yellow" | "red";

// health is worked out from what Gaia knows: the last run, its servers'
// states and open drift. It is not live monitoring data.
export function health(lastStatus: RunStatus | undefined, hosts: Pick<Host, "status">[] | number, drift: number): Health {
  if (lastStatus === "failed") return "red";
  const busy = Array.isArray(hosts) && hosts.some((h) => h.status !== "active");
  if (drift > 0 || busy) return "yellow";
  return "green";
}

export function syncState(sync: ContractSummary["sync"], drift: number): SyncState {
  if (sync === "out-of-sync") return "out-of-sync";
  if (drift > 0) return "drifted";
  return sync ?? "unknown";
}

export function driftCount(drifts: Drift[] | undefined, contract: string): number {
  return (drifts ?? []).filter((d) => d.contract === contract).length;
}
