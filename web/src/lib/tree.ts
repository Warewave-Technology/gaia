// The contract's resource tree (Argo CD's): the contract, its servers,
// and each server's resources in other systems.
import type { Contract } from "../api/types";
import type { IconName } from "../ui/icons";
import type { PlanServer } from "./plan";

export type Tone = "green" | "yellow" | "red" | "gray";

export type TreeNode = {
  id: string;
  icon: IconName;
  color: string; // the icon's colour: the kind of thing
  name: string;
  kind: string;
  status: string;
  statusIcon: IconName;
  tone: Tone;
  dashed?: boolean;
};

export type TreeEdge = { id: string; source: string; target: string; dashed?: boolean };

// What each external ID kind is (internal/apply writes them).
export const RESOURCE: Record<string, { icon: IconName; color: string; label: string }> = {
  vm: { icon: "vm", color: "var(--blue)", label: "VM" },
  ip: { icon: "network", color: "var(--purple)", label: "IP" },
  host: { icon: "activity", color: "var(--orange)", label: "Monitoring" },
  computer: { icon: "key", color: "var(--aqua)", label: "Directory account" },
  dns: { icon: "pin", color: "var(--purple)", label: "DNS" },
  ssh: { icon: "terminal", color: "var(--fg2)", label: "Bastion (SSH)" },
  rdp: { icon: "terminal", color: "var(--fg2)", label: "Bastion (RDP)" },
};

function resource(kind: string) {
  return RESOURCE[kind] ?? (kind.startsWith("dns") ? RESOURCE.dns! : { icon: "box" as IconName, color: "var(--fg4)", label: kind });
}

const HOST_STATUS: Record<string, { tone: Tone; icon: IconName }> = {
  active: { tone: "green", icon: "heart" },
  provisioning: { tone: "yellow", icon: "clock" },
  draining: { tone: "yellow", icon: "clock" },
  stopped: { tone: "gray", icon: "stop" },
  decommissioning: { tone: "red", icon: "x-circle" },
};

export function buildTree(c: Contract, sync: { label: string; icon: IconName; tone: Tone }, planned: PlanServer[] = []): { nodes: TreeNode[]; edges: TreeEdge[] } {
  const root = `contract:${c.name}`;
  const nodes: TreeNode[] = [{ id: root, icon: "file", color: "var(--yellow)", name: c.name, kind: "Contract (Server)", status: sync.label, statusIcon: sync.icon, tone: sync.tone }];
  const edges: TreeEdge[] = [];
  for (const h of c.hosts) {
    const id = `host:${h.uid}`;
    const st = HOST_STATUS[h.status] ?? { tone: "gray" as Tone, icon: "help" as IconName };
    nodes.push({ id, icon: "server", color: "var(--aqua)", name: h.hostname, kind: "Server", status: h.status, statusIcon: st.icon, tone: st.tone });
    edges.push({ id: `${root}>${id}`, source: root, target: id });
    for (const [key, value] of Object.entries(h.externalIds).sort()) {
      const [provider, kind = ""] = key.split("/");
      if (kind === "smbios") continue;
      const r = resource(kind);
      const rid = `${id}:${key}`;
      nodes.push({ id: rid, icon: r.icon, color: r.color, name: value, kind: `${r.label} · ${provider}`, status: "registered", statusIcon: "check", tone: "green" });
      edges.push({ id: `${id}>${rid}`, source: id, target: rid });
    }
  }
  const live = new Set(c.hosts.map((h) => h.hostname));
  for (const s of planned) {
    if ((s.action !== "create" && s.action !== "adopt") || live.has(s.hostname)) continue;
    const id = `planned:${s.hostname}`;
    nodes.push({ id, icon: "server", color: "var(--aqua)", name: s.hostname, kind: "Server", status: "missing · change pending", statusIcon: "clock", tone: "yellow", dashed: true });
    edges.push({ id: `${root}>${id}`, source: root, target: id, dashed: true });
  }
  return { nodes, edges };
}
