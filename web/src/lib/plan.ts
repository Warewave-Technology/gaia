// Reading a change's plan (internal/plan): what it does to each area.

export type PlanServer = {
  action: "create" | "keep" | "delete" | "replace" | "adopt";
  uid?: string;
  status?: string;
  hostname: string;
  seq?: number;
  site: string;
  network: string;
  compute?: { provider?: string; type?: string; template?: string };
  ipam?: { provider?: string; pool?: string; gateway?: string };
  adopted?: boolean;
};

export type PlanContract = {
  name: string;
  labels?: Record<string, string>;
  env?: string;
  approval?: string;
  image?: string;
  size?: { name: string; cpu?: number; memoryGiB?: number; diskGiB?: number };
  servers?: PlanServer[];
  baseline?: string;
  roles?: { name: string }[];
  monitoring?: { provider?: string; templates?: string[] };
  apps?: unknown[];
  agents?: unknown[];
};

export type Plan = { commit?: string; hash?: string; contracts?: PlanContract[] };

export type Counts = { add: number; change: number; same: number; remove: number };

export type Area = { key: string; label: string; providers: string[]; counts: Counts };

const zero = (): Counts => ({ add: 0, change: 0, same: 0, remove: 0 });

function bump(c: Counts, action: PlanServer["action"]) {
  if (action === "create" || action === "adopt") c.add++;
  else if (action === "replace") c.change++;
  else if (action === "delete") c.remove++;
  else c.same++;
}

export function isServerPlan(p: unknown): p is Plan {
  return typeof p === "object" && p !== null && Array.isArray((p as Plan).contracts);
}

export function servers(p: Plan): (PlanServer & { contract: PlanContract })[] {
  return (p.contracts ?? []).flatMap((c) => (c.servers ?? []).map((s) => ({ ...s, contract: c })));
}

// areas sums the plan per area, the way the plan boxes show it.
export function areas(p: Plan): Area[] {
  const ipam = { key: "ipam", label: "IPAM", providers: new Set<string>(), counts: zero() };
  const prov = { key: "provisioning", label: "Provisioning", providers: new Set<string>(), counts: zero() };
  const mon = { key: "monitoring", label: "İzleme", providers: new Set<string>(), counts: zero() };
  const conf = { key: "configuration", label: "Konfigürasyon", providers: new Set<string>(), counts: zero() };
  for (const c of p.contracts ?? []) {
    for (const s of c.servers ?? []) {
      bump(ipam.counts, s.action);
      if (s.ipam?.provider) ipam.providers.add(s.ipam.provider);
      bump(prov.counts, s.action);
      if (s.compute?.provider) prov.providers.add(s.compute.provider);
      if (c.monitoring) {
        bump(mon.counts, s.action);
        if (c.monitoring.provider) mon.providers.add(c.monitoring.provider);
      }
      if (s.action !== "delete") conf.counts.same++;
    }
  }
  return [ipam, prov, mon, conf].map((a) => ({ ...a, providers: [...a.providers] }));
}

export function total(p: Plan): Counts {
  const c = zero();
  for (const s of servers(p)) bump(c, s.action);
  return c;
}
