// The catalog's kinds: an icon and a line on what each one is.
import type { IconName } from "./icons";

export const KINDS: Record<string, { icon: IconName; text: string }> = {
  Platform: { icon: "grid", text: "Platformun genel ayarları: motorlar, izleme, arşiv." },
  Provider: { icon: "plug", text: "Gaia'nın konuştuğu bir sistem: vSphere, Proxmox, IPAM, Zabbix, AD, OpenBao…" },
  Policy: { icon: "lock", text: "Kurallar: onaylar, isimlendirme, zorunlu ajanlar, bakım pencereleri, roller." },
  Site: { icon: "pin", text: "Veri merkezi: hangi compute ve izleme provider'larının kullanılacağı." },
  Network: { icon: "network", text: "Sunucuların bağlandığı ağ ve IPAM havuzu." },
  Image: { icon: "disc", text: "VM'lerin klonlandığı işletim sistemi şablonu." },
  Size: { icon: "size", text: "VM boyutu: vCPU, bellek, disk." },
  Baseline: { icon: "layers", text: "Her sunucuya uygulanan temel Ansible rolleri." },
  AnsibleRole: { icon: "wrench", text: "Kontratların kullanabildiği bir Ansible rolü ve parametre şeması." },
  App: { icon: "box", text: "Bir uygulamanın artifact'ı ve onu kuran playbook." },
  Agent: { icon: "activity", text: "Sunuculara kurulan ajan (izleme, güvenlik…); bazıları zorunlu." },
  AuthConfig: { icon: "key", text: "Giriş ayarları: dizin, ikinci faktör, oturum süreleri." },
  Team: { icon: "users", text: "Bir takım ve onaycıları; çoğu AD'den üretilir." },
  Group: { icon: "shield", text: "Erişim grubu: takımların birleşimi, kontratlarda erişim için kullanılır." },
  Server: { icon: "file", text: "Bir grup sunucunun istenen durumu (kontrat)." },
  Operation: { icon: "zap", text: "Sunucularda bir kez yapılacak iş: yeniden başlatma, yama, runbook." },
  OperationSchedule: { icon: "calendar", text: "Belli aralıklarla Operation yazan zamanlama." },
  TeamSource: { icon: "repeat", text: "Team objelerini AD'den üreten kaynak." },
  NetworkSource: { icon: "repeat", text: "Network objelerini IPAM'dan üreten kaynak." },
  AnsibleRoleSource: { icon: "repeat", text: "AnsibleRole objelerini config reposundan üreten kaynak." },
};

export function kindOf(kind: string) {
  return KINDS[kind] ?? { icon: "box" as IconName, text: "" };
}

// Where each kind lives in the menu (design 17.8). Contracts and
// operations have their own screens; the rest of Gaia's settings are under
// Ayarlar.
export type Section = "catalog" | "infra" | "access" | "settings";

export const SECTIONS: Record<Section, { path: string; label: string }> = {
  catalog: { path: "/catalog", label: "Katalog" },
  infra: { path: "/infra", label: "Altyapı" },
  access: { path: "/access", label: "Erişim" },
  settings: { path: "/settings", label: "Ayarlar" },
};

const SECTION_OF: Record<string, Section> = {
  AnsibleRole: "catalog", App: "catalog", Agent: "catalog", Baseline: "catalog",
  Site: "infra", Network: "infra", Image: "infra", Size: "infra",
  Group: "access", Team: "access",
};

export function sectionOf(kind: string): Section {
  return SECTION_OF[kind] ?? "settings";
}

// SETTINGS_TAB is the Ayarlar tab that lists a kind.
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
