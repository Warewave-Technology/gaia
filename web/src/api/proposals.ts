// Settings changes (design 17.3): checked, proposed, decided.
import { api } from "./client";

export type Edit = { yaml?: string; kind?: string; name?: string; delete?: boolean };
export type ObjectChange = { kind: string; name: string; path: string; op: "add" | "change" | "remove"; before?: string; after?: string };
export type Impact = { contract: string; error?: string };
export type Check = { base: number; contentHash: string; objects: ObjectChange[]; errors: string[]; impact: Impact[]; critical: boolean; whyCritical?: string[] };
export type ProposalStatus = "pending" | "applied" | "rejected" | "stale" | "withdrawn";
export type Proposal = {
  id: string; title: string; reason?: string; status: ProposalStatus; baseVersion: number; contentHash: string; requestedBy: string;
  critical: boolean; whyCritical?: string[]; impact: Impact[]; paths: string[]; decidedBy?: string; comment?: string; appliedVersion?: number;
  staleReason?: string; createdAt: string; decidedAt?: string; mayDecide: boolean; why?: string; objects?: ObjectChange[];
};

export function checkSettings(changes: Edit[]) {
  return api<Check>("/settings/check", { method: "POST", body: { changes } });
}

export function propose(title: string, reason: string, changes: Edit[]) {
  return api<Proposal>("/settings/proposals", { method: "POST", body: { title, reason, changes } });
}
