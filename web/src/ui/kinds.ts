// The catalog's kinds: an icon and a line on what each one is.
import type { IconName } from "./icons";

export const KINDS: Record<string, { icon: IconName; text: string }> = {
  Platform: { icon: "grid", text: "The platform's own settings: engines, monitoring, archives." },
  Provider: { icon: "plug", text: "A system Gaia talks to: vSphere, Proxmox, IPAM, Zabbix, AD, OpenBao…" },
  Policy: { icon: "lock", text: "The rules: approvals, naming, required labels, environments, roles." },
  Site: { icon: "pin", text: "A data centre: where its VMs are created and how they are watched." },
  Network: { icon: "network", text: "A network servers join, and the IPAM pool their addresses come from." },
  Image: { icon: "disc", text: "The operating system template VMs are cloned from." },
  Size: { icon: "size", text: "A VM size: vCPUs, memory, disk." },
  Baseline: { icon: "layers", text: "The base configuration every server gets." },
  AnsibleRole: { icon: "wrench", text: "An Ansible role contracts can use, with its parameters." },
  App: { icon: "box", text: "An application: its artifact and the playbook that installs it." },
  Agent: { icon: "activity", text: "An agent installed on servers (monitoring, security…); some are mandatory." },
  AuthConfig: { icon: "key", text: "How people sign in: the directory, the second factor, session lifetimes." },
  Team: { icon: "users", text: "A team and its approvers; usually from the directory." },
  Group: { icon: "shield", text: "An access group: teams together, given access in contracts." },
  Server: { icon: "file", text: "The desired state of a group of servers (a contract)." },
  Operation: { icon: "zap", text: "A one-off job on servers: a reboot, a patch, a runbook." },
  OperationSchedule: { icon: "calendar", text: "A schedule that writes Operations at set times." },
  TeamSource: { icon: "repeat", text: "Makes Teams from directory groups." },
  NetworkSource: { icon: "repeat", text: "Makes Networks from IPAM subnets." },
  AnsibleRoleSource: { icon: "repeat", text: "Makes AnsibleRoles from the configuration repository." },
};

export function kindOf(kind: string) {
  return KINDS[kind] ?? { icon: "box" as IconName, text: "" };
}

// Where each kind lives in the menu (design 17.8). Contracts and
// operations have their own screens; the rest of Gaia's settings are under
// Settings.
export type Section = "catalog" | "infra" | "access" | "settings";

export const SECTIONS: Record<Section, { path: string; label: string }> = {
  catalog: { path: "/catalog", label: "Catalog" },
  infra: { path: "/infra", label: "Infrastructure" },
  access: { path: "/access", label: "Access" },
  settings: { path: "/settings", label: "Settings" },
};

const SECTION_OF: Record<string, Section> = {
  AnsibleRole: "catalog", App: "catalog", Agent: "catalog", Baseline: "catalog",
  Site: "infra", Network: "infra", Image: "infra", Size: "infra",
  Group: "access", Team: "access",
};

export function sectionOf(kind: string): Section {
  return SECTION_OF[kind] ?? "settings";
}

// SETTINGS_TAB is the Settings tab that lists a kind.
const SETTINGS_TAB: Record<string, string> = {
  Provider: "providers", Platform: "platform", Policy: "policy", AuthConfig: "auth",
  TeamSource: "sources", NetworkSource: "sources", AnsibleRoleSource: "sources",
};

// listPath is the screen that lists a kind.
export function listPath(kind: string): string {
  const s = sectionOf(kind);
  if (s === "catalog") return `/catalog?kind=${encodeURIComponent(kind)}`;
  if (s === "settings") return `/settings/${SETTINGS_TAB[kind] ?? "providers"}`;
  return `${SECTIONS[s].path}/${encodeURIComponent(kind)}`;
}

// objectPath is an object's own page.
export function objectPath(kind: string, name: string): string {
  if (kind === "Server") return `/contracts/${encodeURIComponent(name)}`;
  if (kind === "Operation" || kind === "OperationSchedule") return `/operations?q=${encodeURIComponent(name)}`;
  return `${SECTIONS[sectionOf(kind)].path}/${encodeURIComponent(kind)}/${encodeURIComponent(name)}`;
}
