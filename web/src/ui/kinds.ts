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
  AuthConfig: { icon: "key", text: "Giriş ayarları: OIDC sağlayıcıları, passkey, oturum süreleri." },
  Team: { icon: "user", text: "Bir takım ve onaycıları; çoğu AD'den üretilir." },
  Group: { icon: "user", text: "Erişim grubu: takımların birleşimi, kontratlarda erişim için kullanılır." },
  TeamSource: { icon: "repeat", text: "Team objelerini AD'den üreten kaynak." },
  NetworkSource: { icon: "repeat", text: "Network objelerini IPAM'dan üreten kaynak." },
  AnsibleRoleSource: { icon: "repeat", text: "AnsibleRole objelerini config reposundan üreten kaynak." },
};

export function kindOf(kind: string) {
  return KINDS[kind] ?? { icon: "box" as IconName, text: "" };
}
