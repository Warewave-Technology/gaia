// A kind's spec as fields (GET /schema/{kind}), with the labels and help
// the forms and tables show for them.
import { useQuery } from "@tanstack/react-query";
import { api } from "./client";

export type Field = {
  key?: string;
  type: "string" | "int" | "float" | "bool" | "object" | "list" | "map" | "any";
  required?: boolean;
  enum?: string[];
  ref?: string;
  fields?: Field[];
  items?: Field;
};

export function useSchema(kind: string) {
  return useQuery({ queryKey: ["schema", kind], queryFn: () => api<Field>(`/schema/${encodeURIComponent(kind)}`), staleTime: Infinity });
}

const ACRONYMS = new Set(["ipam", "dns", "ntp", "ttl", "url", "cpu", "vlan", "vnet", "ou", "dn", "id", "cli", "ui", "mfa", "ca", "cidr", "os", "tls", "vm", "vmid", "vmids", "api", "ssh", "smtp", "s3", "uid", "acr", "amr", "oidc", "ldap", "dc", "gib", "rrule"]);

// LABELS name the fields whose key does not say it well.
const LABELS: Record<string, string> = {
  memoryGiB: "Memory (GiB)", diskGiB: "Disk (GiB)", cpu: "vCPUs", vmids: "VM ID range", vmid: "VM ID", baseDN: "Base DN",
  dn: "Distinguished name", adGroup: "Directory group", approversGroup: "Approvers' directory group", trustIdp: "Trust the identity provider",
  allowedEnvironments: "Allowed environments", requiredLabels: "Required labels", gaiaRoles: "Gaia roles", zabbixProxy: "Zabbix proxy",
  runArchive: "Run archive", runLogs: "Run logs", writeCredentials: "Write credentials", accessTtl: "Access token lifetime", refreshTtl: "Refresh token lifetime",
  ttl: "Lifetime", idle: "Idle timeout", secretsPrefix: "Secrets prefix", displayName: "Display name", createAhead: "Create ahead",
  checkMode: "Check mode", purpose: "Purpose", require: "Second factor", url: "URL", endpoint: "Endpoint",
};

export function label(key: string): string {
  if (LABELS[key]) return LABELS[key]!;
  const words = key.replace(/([a-z0-9])([A-Z])/g, "$1 $2").split(/[\s_-]+/);
  return words.map((w, i) => (ACRONYMS.has(w.toLowerCase()) ? w.toUpperCase() : i === 0 ? w[0]!.toUpperCase() + w.slice(1) : w.toLowerCase())).join(" ");
}

// HELP explains fields, by kind and dotted path (lists as []).
const HELP: Record<string, string> = {
  "Site.compute": "The hypervisor this site's VMs are created on. vSphere uses datacenter, cluster and folder; Proxmox uses nodes, pool and storage.",
  "Site.compute.vmids": "The Proxmox VM ID range Gaia allocates from.",
  "Site.ntp": "Time servers the site's servers use.",
  "Network.site": "The site this network belongs to.",
  "Network.attachment": "How a VM joins the network: a vSphere portgroup, a Proxmox bridge (with a VLAN tag) or a Proxmox SDN VNet. Fill in one.",
  "Network.ipam": "Where addresses come from: an IPAM provider and its pool.",
  "Network.gateway": "The default gateway of the servers on this network.",
  "Network.dns": "The DNS servers, and the zone Gaia registers servers in.",
  "Network.domain": "The directory domain servers on this network join, and the OU their computer accounts go to.",
  "Network.allowedEnvironments": "Environments (the env label) that may use this network; empty means all.",
  "Image.connection": "How Gaia reaches the servers: SSH for Linux, WinRM for Windows.",
  "Image.templates": "The template to clone on each compute provider.",
  "Image.templates[].customization": "How the first boot is configured, e.g. cloud-init.",
  "Group.teams": "Teams whose members are in this access group.",
  "Group.groups": "Other access groups included in this one.",
  "Group.owner": "The team that approves changes to this group's members.",
  "Group.privileged": "Granting sudo through a privileged group needs a platform admin's approval.",
  "Team.adGroup": "The directory group the team's members come from.",
  "Team.approversGroup": "The directory group whose members approve the team's changes.",
  "Team.secrets": "The OpenBao path the team's contracts may refer to.",
  "Provider.credentials": "A reference to OpenBao (bao://…). The secret itself is never written here.",
  "Provider.baseDN": "Where a directory provider searches; empty is the directory's default naming context.",
  "AuthConfig.url": "Where people open Gaia. Passkeys are bound to this host.",
  "AuthConfig.require": "What Gaia asks for after the password: a passkey only, or a passkey or an authenticator app code (mfa).",
  "AuthConfig.providers": "How people sign in. With a directory (ldap), local accounts are for break-glass only.",
  "AuthConfig.providers[].directory": "The directory (an Active Directory provider) people sign in with.",
  "AuthConfig.sessions": "How long sessions last.",
  "Policy.naming": "A CEL expression that names servers.",
  "Policy.requiredLabels": "Labels every contract must have.",
  "Policy.environments": "Per environment: whether changes need approval, and by whom.",
  "Policy.gaiaRoles": "Which teams get Gaia's roles (operator, platform-admin).",
  "Policy.settings": "Who approves settings changes besides platform admins.",
  "TeamSource.select": "A CEL expression: which directory groups become teams.",
  "TeamSource.naming": "A CEL expression: the team name made from the group.",
  "NetworkSource.select": "A CEL expression: which IPAM subnets become networks.",
  "NetworkSource.naming": "A CEL expression: the network name made from the subnet.",
  "NetworkSource.provider": "The IPAM provider whose subnets are read.",
  "NetworkSource.map": "CEL expressions that fill each network's fields from the subnet. Left empty, IPAM is this source's provider and the gateway is the subnet's.",
  "NetworkSource.map.site": "The site of the network, e.g. subnet.site.",
  "NetworkSource.map.attachment": "How VMs join the network, e.g. {\"vnet\": subnet.tags.vnet}.",
  "NetworkSource.output": "A short folder name the made networks are grouped under.",
  "TeamSource.provider": "The directory (an Active Directory provider) whose groups are read.",
  "TeamSource.map": "CEL expressions that fill each team's fields from the group.",
  "TeamSource.map.displayName": "The team's display name, e.g. group.description.",
  "TeamSource.map.approvers": "The common name of the team's approvers group, e.g. group.cn + \"-approvers\".",
  "TeamSource.map.secretsPrefix": "The OpenBao path the team's contracts may use, e.g. \"kv/teams/\" + name.",
  "TeamSource.membership.recursive": "Count members of nested groups as members too.",
  "TeamSource.membership.sync.every": "How often memberships are read from the directory, e.g. 5m (the default).",
  "TeamSource.output": "A short folder name the made teams are grouped under.",
  "AnsibleRoleSource.provider": "The Git provider of the configuration repository.",
  "AnsibleRoleSource.paths": "Globs of role directories to read, inside the provider's path, e.g. roles/*.",
  "AnsibleRoleSource.exclude": "Globs of role directories to skip.",
  "AnsibleRoleSource.naming": "A CEL expression: the role's name in Gaia, e.g. role.name.",
  "AnsibleRoleSource.map": "CEL expressions over role and meta (meta/main.yml) that fill each role's fields.",
  "AnsibleRoleSource.output": "The directory in the platform repository the roles are committed to.",
  "Size.cpu": "Virtual CPUs.",
  "Size.memoryGiB": "Memory, in GiB.",
  "Size.diskGiB": "The system disk, in GiB.",
  "Site.zabbixProxy": "The Zabbix proxy that monitors this site's servers, if any.",
  "Site.compute.provider": "A vSphere or Proxmox provider.",
  "Site.compute.nodes": "Proxmox nodes VMs may be placed on.",
  "Site.compute.storage.default": "The datastore or storage new disks go to.",
  "Site.compute.storage.snapshots": "Whether the storage can take snapshots.",
  "Network.description": "A note for people choosing a network.",
  "Network.attachment.vlan": "The VLAN tag, with a Proxmox bridge.",
  "Network.dns.reverse": "Also register the reverse (PTR) record.",
  "Network.dns.ttl": "Seconds, for the records Gaia writes.",
  "Network.domain.ou": "The OU computer accounts are created in.",
  "Image.os": "linux or windows.",
  "Image.templates[].template": "The template by name (vSphere) or by VM ID (Proxmox).",
  "Group.description": "What the access group is for.",
  "Team.displayName": "The team's name as people see it.",
  "Team.notifications": "Where the team is told about its changes, runs and drift.",
  "Team.notifications[].triggers": "The events to send, e.g. change.pending.",
  "Team.notifications[].to": "Email addresses, for an SMTP channel.",
  "Provider.type": "What kind of system this is; the other fields depend on it.",
  "Provider.endpoint": "The system's API address.",
  "Provider.auth": "How Gaia signs in to OpenBao.",
  "Provider.bucket": "S3: the bucket.",
  "Provider.retention": "S3: how long run records are locked, e.g. 30d or 10y.",
  "Provider.url": "Git: the repository's clone URL.",
  "Provider.path": "Git: the folder inside the repository.",
  "Provider.ref": "Git: the branch to read.",
  "Provider.from": "SMTP: the sender address.",
  "Provider.writeCredentials": "A directory account that may write DNS records and computer accounts, and nothing else (bao://…).",
  "AuthConfig.providers[].purpose": "For local accounts: break-glass.",
  "AuthConfig.providers[].directory.claim": "OIDC only: the token claim that names the directory account (preferred_username by default).",
  "AuthConfig.sessions.ui.ttl": "How long a browser session lasts at most, e.g. 12h.",
  "AuthConfig.sessions.ui.idle": "A browser session ends after this long unused, e.g. 30m.",
  "AuthConfig.sessions.cli.accessTtl": "How long a CLI access token lasts, e.g. 15m.",
  "AuthConfig.sessions.cli.refreshTtl": "How long the CLI stays signed in, e.g. 7d.",
  "Policy.operations": "Who approves Operations (reboots, patching…), per environment and action.",
  "Policy.decommission": "How servers go: the wait before deleting, a last snapshot, and how long the address rests.",
  "Policy.decommission.gracePeriod": "How long a stopped server waits before it is deleted, e.g. 7d. Empty deletes at once.",
  "Policy.decommission.ipQuarantine": "How long a freed address is not handed out again, e.g. 30d.",
  "Policy.drift": "How often each area is checked for drift, and how drift is put right.",
  "Policy.environments.approval": "Whether changes in this environment need approval, and from whom.",
  "Policy.environments.deleteProtection": "Servers in this environment cannot be deleted by a change.",
  "Platform.secrets": "The OpenBao provider secrets are read from.",
  "Platform.engines": "The tools that build servers (provisioning) and configure them (configuration).",
  "Platform.engines.configuration.sshKey": "Gaia's SSH key pair in OpenBao: new servers get the public key, Ansible uses the private one.",
  "Platform.engines.configuration.limits.concurrentRuns": "How many runs may go at once.",
  "Platform.monitoring": "Where servers are registered for monitoring.",
  "Platform.bastion": "The Warpgate people reach servers through.",
  "Platform.runArchive": "The S3 provider run records are archived to, locked.",
  "Platform.runLogs": "The S3 provider each run's raw output is kept in.",
  "Platform.contracts": "Repositories with contracts besides the platform repository.",
  "Platform.notifications.email": "The SMTP provider personal email notifications are sent through.",
};

export function help(kind: string, path: string): string | undefined {
  return HELP[`${kind}.${path}`];
}
