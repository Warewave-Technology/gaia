# Gaia – Tasarım Taslağı

> Durum: **Taslak v0.3** (2026-10-25: Git ile Gaia ayarlarının ayrılması ve LDAP kimlik modeli, bkz. 17)
> Kapsam: Altyapı bileşenlerinin (Linux, Windows/AD, vCenter/Proxmox ve çevre sistemler) tamamını tek bir kontrat üzerinden yöneten servisin tasarımı.

## 0. Amaç ve Prensipler

Teknolojiler çeşitli olsa da hepsini **tek bir YAML kontratı** yönetir.

- **Kontrat sadece referans içerir.** Endpoint, credential ve altyapı detayı kontratta bulunmaz; bunlar Gaia'nın kendi objelerinde tanımlıdır.
- **Provisioning Pulumi'de, konfigürasyon Ansible'da.** Diğer sistemlerle (Zabbix, IPAM, AD, Warpgate, Pulp, Zot, OpenBao, SeaweedFS) doğrudan API üzerinden konuşulur.
- **Tüm bileşenler açık kaynak.**
- **Sunucuların istenen durumu Git'tedir; Gaia'nın kendi ayarları Gaia'dadır** (bkz. 17).
  - Git'te: kontratlar (Server, Operation, OperationSchedule) ve sunuculara ne kurulacağı (AnsibleRole, App, Agent, Baseline).
  - Gaia'da: geri kalan her şey; provider'lar, altyapı seçenekleri, erişim grupları, kurallar, kimlik.
  - İki taraftaki değişiklik de aynı onay kurallarından ve aynı onaycılardan geçer, denetim kaydına yazılır.
- **Değişikliği kişi önerir, Gaia uygular.** Git'teki değişiklik commit ile, Gaia ayarındaki değişiklik arayüzden bir öneriyle gelir. İkisi de incelenir ve onaylanır; uygulamayı Gaia başlatır (bkz. 7.6 ve 17.3).
- **Her verinin tek bir sahibi vardır.** Gaia veriyi kopyalamaz, sahibine referans verir.
- **Bir plan, bir commit ve bir ayar sürümünden üretilebilir.** Canlı bir kaynağı okuyarak plan değiştirilemez. Kaynaklardan gelen veri önce Git'e ya da ayar deposuna, sürümlenerek yazılır.

## 1. Kavramlar

| Kavram | Ne | Kim yazar |
|---|---|---|
| **Provider** | Bir sisteme nasıl bağlanılacağı (endpoint, credential referansı) | Platform ekibi |
| **Katalog objesi** | Kontratın seçebileceği isimli seçenekler: ağ, imaj, boyut, rol, uygulama… | Platform ekibi veya Source (otomatik) |
| **Source** | Katalog objelerini asıl kaynağından üreten tanım | Platform ekibi |
| **Policy** | İsimlendirme, ortam kuralları, zorunlu etiketler | Platform ekibi |
| **Kontrat** | İstenen durum (`Server`) veya tek seferlik iş (`Operation`) | Uygulama ekipleri |
| **Change** | Bir commit'in (veya drift'in) bir kontrata etkisi: plan, gereksinimler, onaylar (bkz. 7.6) | Gaia |
| **Run** | Bir kontratın bir commit'teki uygulaması ve bu uygulamanın değiştirilemez kaydı | Gaia |

Katalog, provider ile kontrat arasındaki katmandır. Kontrat doğrudan provider'a bağlansaydı kullanıcı cluster, datastore ve portgroup gibi detayları bilmek zorunda kalırdı. Katalogla birlikte tek bir isim (ör. `network: ank-prod-app`) birden çok sisteme yayılır: hipervizördeki ağ bağlantısı (vCenter portgroup'u veya Proxmox bridge/VLAN), IPAM havuzu, DNS zone'u ve Zabbix proxy'si aynı isimden çözülür.

## 2. Repo Düzeni

```
platform/                          # platform ekibinin reposu
  platform.yaml                    # kind: Platform (tekil)
  auth.yaml                        # kind: AuthConfig
  providers/
  sources/                         # NetworkSource, AnsibleRoleSource
  catalog/
    sites/  images/  sizes/  baselines/  apps/  agents/
    networks/                      # elle yazılanlar ve patch'ler
    ansible-roles/                 # elle yazılanlar ve patch'ler
  generated/                       # SADECE sync botu yazar
    networks/
    ansible-roles/
  policies/

contracts/                         # ayrı repo, uygulama ekipleri yazar
  portal/portal-web.yaml
```

**Kurallar**

- **Dosya yolu önemsiz.** Gaia dosyanın nerede olduğuna değil, `kind` ve `metadata.name` alanlarına bakar. Bir dosyada birden fazla doküman (`---`) olabilir.
- **Kimlik:** Bir objenin kimliği `kind + name` ikilisidir ve global olarak tekildir. Aynı ikiliden iki tane varsa yükleme hata verir. Bunun tek istisnası bölüm 5.5'teki patch objeleri.
- **Onay:** Kimin neyi onaylayacağını Gaia belirler (bkz. 7.6). CODEOWNERS ve PR review'u ekiplerin tercihidir; Gaia onlara bakmaz.

## 3. Kind Listesi

"Nerede" sütunu 17. bölümdeki ayrımı gösterir: **Git** (repo, commit ile) ya da **Gaia** (ayar deposu, arayüzden öneri ile).

| Kind | Grup | Nerede | Elle / üretilen | Not |
|---|---|---|---|---|
| `Server` | Kontrat | Git | Elle | İstenen durum |
| `Operation` | Kontrat | Git | Elle | Tek seferlik iş: patch, snapshot, replace, reconcile, runbook… (bkz. 6.4) |
| `OperationSchedule` | Kontrat | Git | Elle | Tekrarlayan Operation şablonu (bkz. 6.4) |
| `AnsibleRole` | Katalog | Git | **Üretilen** + elle | `AnsibleRoleSource` üretir |
| `App` | Katalog | Git | Elle | Uygulama: artifact, playbook, parametreler |
| `Agent` | Katalog | Git | Elle | BigFix, Kaspersky, Zabbix ajanı… |
| `Baseline` | Katalog | Git | Elle | Hardening playbook'u |
| `Site` | Altyapı | Gaia | Elle | Lokasyon: hipervizör yerleşimi, NTP, Zabbix proxy |
| `Network` | Altyapı | Gaia | **Üretilen** + elle | `NetworkSource` üretir |
| `Image` | Altyapı | Gaia | Elle | VM şablonu |
| `Size` | Altyapı | Gaia | Elle | CPU/RAM/disk profili |
| `Group` | Erişim | Gaia | Elle | Team'lere referans veren erişim grubu |
| `Team` | Erişim | Gaia | **Üretilen** + elle | Dizinden (LDAP) `TeamSource` üretir; dizin yoksa elle (bkz. 17.5) |
| `Policy` | Ayar | Gaia | Elle | İsimlendirme, ortamlar, onaylar, zorunlu etiketler |
| `Platform` | Ayar | Gaia | Elle | Tekil. Engine ayarları |
| `AuthConfig` | Ayar | Gaia | Elle | Kimlik doğrulama (bkz. 17.5) |
| `Provider` | Ayar | Gaia | Elle | Her dış sistem için bir tane; bildirim kanalları da |
| `NetworkSource` | Source | Gaia | Elle | IPAM'dan Network üretir |
| `AnsibleRoleSource` | Source | Gaia | Elle | Config reposundan AnsibleRole üretir (çıktısı Git'e) |
| `TeamSource` | Source | Gaia | Elle | Dizin gruplarından Team üretir |

Rol objesine `Ansible` öneki bilerek eklendi. "Rol" kelimesi Warpgate rolleri ve RBAC rolleri için de kullanılıyor; önek bu karışıklığı önlüyor.

## 4. Platform Objeleri

### 4.1 Provider

```yaml
apiVersion: gaia/v1alpha1
kind: Provider
metadata:
  name: vc-ank
spec:
  type: vsphere
  endpoint: https://vcenter-ank.corp.local
  credentials: bao://kv/platform/vsphere/ank       # secret'a sadece referans verilir
```

Tanımlı provider tipleri: `openbao`, `vsphere`, `proxmox`, `ipam`, `activedirectory`, `zabbix`, `warpgate`, `pulp`, `nexus`, `s3` (SeaweedFS), `postgres` (Pulumi state), `git`, ve bildirim kanalları `slack`, `msteams`, `smtp`, `webhook`. Harici bir Ansible runner'ı yoktur (bkz. 7.4).

**Artifact deposu: Pulp 3 ve Zot.**

- **Pulp 3** (GPLv2+). Şunları tutar:
  - OS paketleri: Gaia'nın kendi `.deb`/`.rpm` reposu (imzalı yayın) ve internete çıkamayan siteler için Rocky/RHEL ve Debian/Ubuntu repolarının aynaları.
  - Ansible Galaxy collection'ları ve PyPI aynası.
  - App artifact'ları (file repository). Gaia bunlarla `pulp` provider'ı üzerinden konuşur.
- **Zot** (Apache-2.0). OCI registry'si:
  - EE imajları, Gaia'nın imajı ve Helm chart'ı (OCI artifact) burada durur.
  - Gaia Zot ile API üzerinden konuşmaz; imajlara digest'le referans verir.
- **Ortak özellikler:** İkisi de OSI lisanslı, EULA istemez ve internetsiz çalışır. Depolama olarak SeaweedFS'i (S3) kullanırlar.
- **Nexus:** `nexus` provider'ı, Nexus'u zaten olan kurulumlar için kalır. App artifact'ı ikisinde de aynı biçimde tanımlanır (bkz. 6.1).

### 4.2 Platform

```yaml
apiVersion: gaia/v1alpha1
kind: Platform
metadata:
  name: platform
spec:
  secrets: bao
  engines:
    provisioning:  { tool: pulumi,  state: state }        # Pulumi Automation API, state PostgreSQL'de (12.1)
    configuration: { tool: ansible, executor: ansible-runner, source: config-repo }   # ayrıntılar 7.4'te
  monitoring:
    provider: zabbix
    hostGroups: ["'Gaia/env/' + env", "'Gaia/app/' + app", "'Gaia/site/' + site.name"]   # CEL, naming değişkenleri
    templates:
      linux: [Linux by Zabbix agent]                      # Image.os'a göre
      tomcat: [Apache Tomcat by JMX]                      # AnsibleRole.monitoring adına göre
  runArchive: run-records                                 # run kayıtlarının arşivi, SeaweedFS (Object Lock)
```

### 4.3 Örnek katalog objeleri

```yaml
apiVersion: gaia/v1alpha1
kind: Site
metadata:
  name: ank
spec:
  compute: { provider: vc-ank, datacenter: DC-ANK, cluster: CL-PROD-01, folder: /Prod }   # vSphere
  ntp: [ntp1.corp.local, ntp2.corp.local]
  zabbixProxy: zbx-proxy-ank
---
apiVersion: gaia/v1alpha1
kind: Site
metadata:
  name: ist
spec:
  compute:                                  # Proxmox
    provider: pve-ist
    cluster: pve-ist-01
    nodes: [pve01, pve02, pve03]
    pool: gaia
    storage: { default: ceph-vm, snapshots: true }
  ntp: [ntp1.corp.local]
  zabbixProxy: zbx-proxy-ist
---
apiVersion: gaia/v1alpha1
kind: Image
metadata:
  name: rhel9
spec:
  os: linux
  connection: ssh
  templates:                                # her hipervizör için şablon (bkz. 4.5)
    - provider: vc-ank
      template: tpl-rhel9-latest
    - provider: pve-ist
      template: { name: tpl-rhel9-latest }
      customization: cloud-init
---
apiVersion: gaia/v1alpha1
kind: Size
metadata:
  name: medium
spec: { cpu: 4, memoryGiB: 8, diskGiB: 80 }
---
apiVersion: gaia/v1alpha1
kind: App
metadata:
  name: portal
spec:
  artifact: { provider: pulp, repository: releases, path: portal/portal-${version}.tar.gz }
  playbook: apps/portal.yml
  requires: [web.tomcat]                           # → AnsibleRole/web.tomcat
  params:
    version: { type: str, required: true, gaia: { format: semver } }
    heapMiB: { type: int, default: 2048, gaia: { min: 512, max: 16384 } }   # format: 6.2
---
apiVersion: gaia/v1alpha1
kind: Agent
metadata:
  name: bigfix
spec:
  os: [linux, windows]
  ansibleRole: agents.bigfix
  mandatory: true
```

### 4.4 Policy

```yaml
apiVersion: gaia/v1alpha1
kind: Policy
metadata:
  name: default
spec:
  naming: site.name + env.substring(0, 1) + app.substring(0, 3) + pad(seq, 2)   # CEL → ankppor01
  requiredLabels: [env, owner, app]
  environments:
    prod: { approval: required, deleteProtection: true }
    test: { approval: none }
```

### 4.5 Compute provider'ları: vSphere ve Proxmox

Gaia birden fazla hipervizörü destekler: `vsphere` (vCenter) ve `proxmox` (Proxmox VE). Kontrat hipervizörden bağımsızdır. `network` alanı Site'ı, Site da hangi hipervizörün kullanılacağını belirler. Hipervizöre özgü bilgi sadece katalogda durur:
- `Site.compute`: yerleşim bilgisi.
- `Network.attachment`: ağ bağlantısı.
- `Image.templates`: her hipervizör için şablon.

Bu alanların içeriği provider tipine göre değişir ve validasyon da buna göre yapılır. Bir Image'ın, kontratın site'ındaki hipervizör için şablonu yoksa plan reddedilir.

**Kavram eşlemesi**

| Kavram | vSphere | Proxmox | Not |
|---|---|---|---|
| Yerleşim | datacenter, cluster, folder, datastore | cluster, node, pool, storage | `Site.compute` |
| Ağ bağlantısı | portgroup | bridge + VLAN tag veya SDN vnet | `Network.attachment` |
| Şablon | VM template / content library | Template VM | `Image.templates` |
| İlk yapılandırma | customization spec | cloud-init (Windows'ta cloudbase-init) | |
| Graceful shutdown | VMware Tools | QEMU guest agent | Şablonlarda kurulu olmalı |
| Tag | Kategori + tag | Kategorisiz, sınırlı karakter seti | Proxmox'ta `env.prod` gibi kodlanır |
| `gaia.uid` işareti | Custom attribute | Custom attribute yok | Proxmox'ta tag'e veya VM notlarındaki yapısal bir bloğa yazılır |
| VM kimliği | BIOS UUID (`config.uuid`) | VMID tekrar kullanılabilir | Proxmox'ta dış kimlik olarak SMBIOS UUID tutulur (bkz. 9.1) |
| Snapshot | Her datastore'da | Depolamaya bağlı (ZFS, Ceph, qcow2, LVM-thin) | Snapshot alınamayan depolama Site'ta işaretlenir; `snapshot: true` isteyen plan reddedilir |
| Olay kaydı | `LogUserEvent` | Kullanıcı olayı API'si yok | Run kaydı yeterlidir |

**Araçlar**
- **Pulumi:** vSphere için `vsphere`, Proxmox için topluluk tarafından geliştirilen `proxmoxve` provider'ı (muhlba91, bpg'nin Terraform provider'ını sarar). `proxmoxve` resmi bir Pulumi provider'ı olmadığı için olgunluğu PoC'de doğrulanır. Gaia'nın içindeki VM bileşeni Size, Image ve Network bilgisini provider'ın kaynaklarına çevirir: `vsphere.VirtualMachine` veya `proxmoxve.VmLegacy`.
- **Day-2 işler:** Güç ve snapshot işleri Ansible ile değil, provider'ların Go API'si ile doğrudan yapılır (vSphere için govmomi, Proxmox için Proxmox API client'ı). Her compute provider aynı arayüzü uygular:

```go
type Compute interface {
    Power(ctx, vm, action) error          // on | off | restart (graceful)
    Snapshot(ctx, vm, name) error
    Revert(ctx, vm, name) error
    DeleteSnapshot(ctx, vm, name) error
    Discover(ctx) ([]ObservedVM, error)   // keşif, sahipsiz kaynaklar, reconcile
}
```

Operation aksiyonları (`power`, `snapshot`, `revert`) her hipervizörde aynı arayüzden geçer ve hipervizör farkı tek bir yerde kalır.

- **Uygulamada:** `internal/compute.Compute`. VM, Gaia'nın kaydettiği kimlikle anılır: Proxmox'ta VMID, vSphere'de BIOS UUID.
- **Proxmox:**
  - güç: start; shutdown ve reboot guest agent üzerinden, 5 dakika zaman aşımıyla
  - snapshot belleksiz alınır
  - revert: rollback ve ardından start (belleksiz snapshot VM'i kapalı bırakır)
  - snapshot silme
  - keşif: cluster kaynakları (güç durumu, tag'ler, şablonlar)
  - her iş görev bitene kadar beklenir
- **vSphere:**
  - credentials: `username`, `password`, `insecure` (kendinden imzalı sertifika); endpoint `https://vcenter`
  - VM, Image'daki adlı şablondan klonlanır: Site'ın datacenter, cluster (kaynak havuzu), folder ve varsayılan datastore'u; Network'ün portgroup'u; diskler thin
  - ilk yapılandırma: `customization: cloud-init` ise ağ, kullanıcı ve Gaia'nın anahtarı `guestinfo` ile cloud-init'in VMware datasource'una verilir. Diğer durumda Linux guest customization (ad, adres, gateway, DNS) kullanılır; bu SSH anahtarı kuramaz, şablonda Gaia'nın anahtarı olmalıdır
  - `gaia.uid` ve kontrat VM notlarına (annotation) yazılır
  - VM kimliği Pulumi'nin bildirdiği BIOS UUID'dir; dış kimlik olarak kaydedilir, VMID ayrılmaz
  - güç: power on; shutdown ve reboot VMware Tools üzerinden, kapanış 5 dakika beklenir
  - snapshot belleksiz alınır, revert sonrası VM kapalıysa açılır, snapshot silme konsolidasyonla yapılır
  - keşif: container view ile tüm VM'ler (güç durumu, şablonlar)
  - govmomi'nin `vcsim` simülatörüyle test edilir
- **Operation aksiyonları:**
  - `power` (`options.state: on|off|restart`); `off` sonrası sağlık kontrolü yapılmaz
  - `revert` (`options.snapshot`), sonrasında sağlık kontrolü

**Önce Proxmox:** İlk yazılacak compute provider Proxmox'tur, vSphere ikinci gelir. Böylece soyutlama baştan iki provider ile sınanmış olur. PoC hedefi: tek bir Linux VM'in Proxmox üzerinde uçtan uca açılması. Bunun için Proxmox'ta cloud-init destekli ve QEMU guest agent kurulu bir şablon gerekir.

## 5. Üretilen Objeler

`Network`, `AnsibleRole` ve `Team` objeleri asıl kaynaklarından üretilir. Diğer bütün kind'lar elle yazılır. `Team` ve `TeamSource` 14. bölümde anlatılıyor.

### 5.1 İfade dili: CEL

Source'ların `map` ve `naming` alanlarında, ayrıca Policy'de ifade dili olarak **CEL** kullanılır.

- **Neden CEL:** Yan etkisi yok, tip kontrolü yapıyor ve Go kütüphanesi olgun. Go `text/template` koşul ve döngü desteklediği için kaynak objelerin içine zamanla mantık yazılmaya başlanır.
- **Özel fonksiyonlar:** Gaia ihtiyaç duyulan yardımcı fonksiyonları kendisi kaydeder, ör. `pad(n, width)`.
- **Hatalar:** Bir ifade değerlendirilemezse o obje üretilmez. Hata sync raporunda görünür.

### 5.2 NetworkSource

```yaml
apiVersion: gaia/v1alpha1
kind: NetworkSource
metadata:
  name: ipam-ank
spec:
  provider: ipam
  select: 'subnet.site == "ank" && "gaia-managed" in subnet.tags && subnet.cidr != "10.20.0.0/24"'
  naming: 'subnet.site + "-" + subnet.env + "-" + subnet.zone'
  map:
    site: subnet.site
    attachment: '{"portgroup": "dvPG-VLAN" + string(subnet.vlan)}'    # Proxmox site'ı için: {"bridge": "vmbr0", "vlan": subnet.vlan}
    gateway: subnet.gateway
    ipam: '{"pool": subnet.cidr}'
    dns: '{"provider": "corp-ad", "zone": subnet.dnsZone, "reverse": true}'
    allowedEnvironments: '[subnet.env]'
    labels: '{"site": subnet.site, "env": subnet.env, "zone": subnet.zone}'
  output: generated/networks/ank/
  trigger: { schedule: "*/30 * * * *", webhook: true }
```

Örnek, subnet üzerinde site, env, zone, vlan, gateway, dnsZone ve tags alanlarının tanımlı olduğu bir IPAM varsayar.

- **Gaia'nın IPAM'ında** her havuzun bir adı, label'ları (site, env, zone, vlan, dnsZone...) ve tag'leri vardır (`gaia-ipam pool set --label ... --tag ...`).
- **İfadelerin gördüğü `subnet`:** `cidr`, `gateway`, `name`, `rangeStart`, `rangeEnd`, `tags`, `labels`. Her label ayrıca üst seviyede de görünür (`subnet.site`). Label değerleri metindir; sayı gereken yerde `int(subnet.vlan)` yazılır.
- **map ifadeleri** metin, sayı, liste veya map döndürebilir ve ağın adını (`name`) da görür.
- **Varsayılanlar:** `ipam` yazılmazsa Source'un provider'ı ve havuzun cidr'ı kullanılır; `gateway` yazılmazsa havuzunki.
- **Dışarıda bırakılanlar:** İfadesi hata veren ya da geçerli bir Network olmayan havuz atlanır. Yeni katalog yüklenmezse Gaia hatanın gösterdiği, bu Source'un eklediği veya değiştirdiği dosyaları dışarıda bırakıp yeniden dener; böylece tek bir bozuk havuz diğerlerini bekletmez. Hata Source'un kendi dosyasına bağlanamıyorsa (örneğin kullanılan bir ağın silinmesi) commit yazılmaz. Hepsi raporda görünür.

### 5.3 AnsibleRoleSource

```yaml
apiVersion: gaia/v1alpha1
kind: AnsibleRoleSource
metadata:
  name: corp-roles
spec:
  provider: config-repo
  ref: main
  paths: ["collections/ansible_collections/corp/*/roles/*"]
  exclude: ["*/roles/_*"]
  naming: 'role.collection + "." + role.name'           # → web.nginx
  map:
    os: 'meta.galaxy_info.platforms.map(p, p.name.lowerAscii())'
    labels: '{"category": role.collection}'
    params: argumentSpecs                                # meta/argument_specs.yml
    extra: meta/gaia.yml                                 # opsiyonel: monitoring, conflicts, param uzantıları, rules
  output: generated/ansible-roles/
  trigger: { onCommit: true }
```

- **İsimlendirme:** Roller birden çok collection'a dağıldığında aynı isimli roller çıkar. Bu yüzden isim `collection.role` biçiminde.
- **`meta/gaia.yml`:** Gaia'ya özgü alanlar (`monitoring`, `conflicts`, `checkMode`, `runbook`, parametre uzantıları ve `rules`) rolün yanında tutulur, böylece rol ve metadata'sı aynı commit'te birlikte değişir. Parametre formatı 6.2'de.
- **Okunan repo:** Roller Source'un provider'ından okunur. Bu provider görüntünün config reposuysa görüntüdeki commit kullanılır, değilse provider'ın başı. `paths` ve `exclude`, provider'ın `path`'i içindeki rol dizinlerinin glob'larıdır. `exclude` desenleri yolun sonuyla da eşleşir (`*/roles/_*`).
- **İfadeler:** `role` değişkeni `name`, `collection`, `namespace`, `fqcn` ve `path`'i verir. Collection rolleri `.../ansible_collections/<ns>/<collection>/roles/<ad>` yolundan tanınır. `meta`, `meta/main.yml`'dir. `map.os` bir liste, `map.labels` bir map döndürür.
- **Üretilen AnsibleRole:**
  - `source.role` rolün FQCN'idir.
  - Parametreler `meta/argument_specs.yml`'nin `main` giriş noktasından gelir. `meta/gaia.yml`'deki `params` sadece var olan parametrelere `gaia` uzantısı ekler.
  - `source-revision` rol dizininin git tree hash'idir. Rolü değiştirmeyen commit'ler üretilen dosyayı değiştirmez.
- **Dışarıda bırakılanlar:** Metadata'sı okunamayan ya da geçerli bir AnsibleRole olmayan rol (örneğin izin verilmeyen bir parametre tipi) raporla dışarıda bırakılır.

### 5.4 Üretilen objelerin Git'e yazılması

1. Sync bileşeni Source'u değerlendirir ve objeleri `generated/` dizinine yazar.
2. Fark varsa Gaia, kendi anahtarıyla **imzalı bir commit** atar ve bunu doğrudan izlenen branch'e yazar.
3. Commit'in etkisi normal bir Change olarak görünür ve aynı kurallarla onaylanır (bkz. 7.6). Etki planı (bkz. 7.3) Change'te yer alır.
4. **Gaia doğrulamayan bir commit yazmaz.** Örneğin kullanılan bir ağı silecek bir commit yazılmaz; sorun sync raporunda görünür.

Üretilen her objede şu annotation'lar bulunur:

```yaml
metadata:
  annotations:
    gaia/generated-by: AnsibleRoleSource/corp-roles
    gaia/source-revision: 3f9c2e1
    gaia/content-hash: sha256:9d1e…
```

`content-hash` tutmayan, yani elle düzenlenmiş bir obje CI'da ve yükleme sırasında reddedilir.

- **Hash'in kapsamı:** `content-hash`, objenin kind, ad, label ve spec'inin hash'idir. Kaynakların hepsi aynı makineyi kullanır: render, çıktı dizinindeki dosyalarla karşılaştırma, yeni katalogun yüklenip yüklenmediğinin denenmesi ve imzalı commit.
- **Senkron:** `gaia server` kaynakları belirli aralıklarla (`--source-every`, varsayılan 10 dk) ve istek üzerine (`POST /api/v1/sources/sync`) çalıştırır. Aynı anda tek replika çalıştırır.
- **Rapor:** Her kaynağın son raporu `GET /api/v1/sources`'tadır:
  - eklenen, değişen ve silinen dosyalar
  - dışarıda bırakılanlar ve nedenleri
  - yazılmayan bir commit'in sebebi
- **Çakışma:** Kaynağın commit'i, okuduğu baş commit'in üzerine yazılır. Arada branch ilerlemişse yazılmaz; bir sonraki çalışmada yeniden denenir.

### 5.5 Elle ekleme

**Yeni obje.** IPAM'da olmayan bir ağ veya repoda olmayan bir rol için elle `Network` / `AnsibleRole` yazılabilir. İsmi üretilmiş bir objeyle çakışmamalıdır.

**Üretilen objeyi zenginleştirme.** Aynı isimle bir patch objesi yazılır:

```yaml
apiVersion: gaia/v1alpha1
kind: Network
metadata:
  name: ank-prod-app
  annotations:
    gaia/mode: patch
spec:
  allowedEnvironments: [prod, preprod]
  labels: { pci: "true" }
```

Patch'ler alan sahipliği kuralına bağlıdır:

| Kind | Kaynağın sahibi (patch'lenemez) | Elle eklenebilir |
|---|---|---|
| Network | pool, gateway, site, DNS zone, attachment | labels (metadata), allowedEnvironments, description |
| AnsibleRole | source, revision, params, os, checkMode, runbook, rules | labels (metadata), monitoring, conflicts, allowedEnvironments |
| Team | adGroup, approversGroup | displayName, secrets.prefix |

`patch` işareti olmadan yazılan aynı isimli bir obje yükleme hatası verir.

## 6. Kontrat

### 6.1 Server

```yaml
apiVersion: gaia/v1alpha1
kind: Server
metadata:
  name: portal-web
  labels: { app: portal, env: prod, owner: team-portal }
spec:
  replicas: 3
  image: rhel9
  size: medium
  network: ank-prod-app
  disks:
    - { sizeGiB: 100, mount: /data }
  baseline: linux-cis-l1
  ansibleRoles: [web.nginx, web.tomcat]
  apps:
    - name: portal
      params: { version: 2.4.1 }
  access:
    - group: portal-admins          # Group objesi (bkz. 14.3)
      sudo: true
  patch:
    wave: 2
    snapshot: true
```

**Kontrat alanlarının baktığı kind'lar**

| Alan | Kind |
|---|---|
| `image` | Image |
| `size` | Size |
| `network` / `topology[].network` | Network (site ve hipervizör buradan gelir; bkz. 6.5) |
| `baseline` | Baseline |
| `ansibleRoles` | AnsibleRole |
| `apps[].name` | App |
| `agents` | Agent (zorunlu ajanlar Policy'den kendiliğinden eklenir) |
| `access[].group` / `access[].team` | Group / Team |
| `metadata.labels.owner` | Team |

**Uygulama sırası:** baseline, ajanlar (önce zorunlular), kontratın rolleri, uygulamalar, erişim.
- **Zorunlu ajanlar:** `mandatory: true` olan ve `os`'u imajın işletim sistemini kapsayan Agent'lar, kontrat yazmasa da eklenir.
- **App:** Config reposundaki playbook'u, `requires`'taki rollerin kontratta olmasını ister. Parametreleri ve kuralları rollerle aynı biçimdedir.
- **App'in artifact'ı:** `artifact.path`'teki `${param}` değerleriyle depodaki adrese çevrilir ve playbook'a `gaia_app.artifact` olarak verilir.
  - Provider'lar ortak bir arayüzle çözer: `Resolve(repository, path) → {url, sha256}`.
  - **Pulp:** `repository` bir file distribution'ın adıdır, `path` ise içerikteki `relative_path`.
    - Gaia içeriği distribution'ın son repository version'ında `relative_path` ile arar ve `sha256`'sını alır.
    - İndirme adresi distribution'ın `base_url`'i ile `path`'in birleşimidir.
  - **Nexus:** search API ile çözülür; bulunamazsa dosyanın kendisine HEAD atılır.
  - Run, sunuculara dokunmadan önce artifact'ı arar. Yoksa run `configure` aşamasında, hiçbir sunucu yapılandırılmadan durur.
  - Deponun bildirdiği SHA-256 playbook'a `gaia_app.sha256` olarak verilir (playbook indirmeyi bununla doğrulayabilir) ve run kaydına yazılır. Checksum plana girmez: repo run başladığında okunur.
  - Gaia depoyu provider'ın credentials'ı varsa onunla (`username`, `password`), yoksa anonim okur. Sunucunun indirmek için kimlik bilgisine ihtiyacı varsa bu, App'in `bao://` secret parametresi olarak verilir.
  - Nexus 3.77 ve sonrası Community Edition, API'ye izin vermeden önce EULA kabulü ister. Bu yüzden istemci gerçek bir Nexus'a karşı değil, Nexus 3'ün REST API'sini taklit eden bir sunucuyla test edildi; gerçek Nexus doğrulaması açık.

**Kontratta yazılmayan ama yine de uygulananlar**

- **Zorunlu ajanlar:** Policy'den gelir.
- **NTP ve Zabbix proxy:** Site'tan gelir.
- **İzleme şablonları:** Rollerden türetilir.
- **Etiketler:** `labels` tek kaynak. vCenter tag'lerine, Zabbix tag ve host group'larına ve Ansible gruplarına aynı anda yazılır.

### 6.2 Kaçış kapısı: `params` şeması

Kontrat Ansible'a serbest `vars` geçemez. Her katalog öğesi kendi `params` şemasını tanımlar ve kontrat sadece bu şemadaki parametreleri geçebilir. Şemada olmayan bir parametre veya tipi yanlış bir değer validasyonda reddedilir.

Kontratta parametre iki yerde geçilir:
- **App:** `apps[].params` alanında.
- **AnsibleRole:** `ansibleRoles` listesindeki bir eleman düz bir isim (`web.nginx`) ya da `{ name, params }` olabilir.

```yaml
  ansibleRoles:
    - web.nginx
    - name: web.tomcat
      params: { tomcat_heap_mib: 4096 }
```

#### Format

Katalogda tek format kullanılır. Parametre alanları Ansible `argument_specs` ile aynıdır: `type`, `required`, `default`, `choices`, `elements`, `options`, `no_log`, `description`. Her parametreye bir `gaia` uzantı bloğu, şemanın geneline de CEL ile yazılan `rules` eklenir:

```yaml
kind: App
metadata:
  name: portal
spec:
  params:
    version:
      type: str
      required: true
      gaia: { format: semver }
    heapMiB:
      type: int
      default: 2048
      gaia: { min: 512, max: 16384 }
    dbPassword:
      type: str
      no_log: true
      gaia: { secret: true }          # değer sadece bao:// referansı olabilir
    listenNetwork:
      type: str
      gaia: { ref: Network }          # katalogda olan bir Network adı olmalı
  rules:
    - rule: 'params.heapMiB * 4 <= size.memoryGiB * 1024 * 3'      # %75; CEL aritmetikte int ve double'ı karıştırmaz
      message: "heapMiB, sunucu belleğinin %75'ini aşamaz"
```

**`gaia` uzantıları**

| Alan | Anlamı |
|---|---|
| `min`, `max` | Sayılarda değer sınırı, string ve listelerde uzunluk sınırı |
| `pattern` | Regex |
| `format` | `semver`, `ipv4`, `cidr`, `hostname`, `fqdn`, `duration` |
| `secret` | Değer sadece `bao://` referansı olabilir (bkz. kural 2) |
| `ref` | Değer belirtilen kind'daki bir katalog objesinin adı olmalı |
| `deprecated` | Kullanımdan kaldırma notu; planda uyarı çıkar |
| `freeform` | Serbest yapıya izin verir (bkz. kural 1) |

**Rollerde Ansible'ın dosyasına dokunulmaz.** `meta/argument_specs.yml` olduğu gibi kalır. Gaia uzantıları ve kurallar `meta/gaia.yml` dosyasına parametre adıyla yazılır:

```yaml
# roles/tomcat/meta/gaia.yml
monitoring: [tomcat]
params:
  tomcat_heap_mib: { min: 512, max: 16384 }
  tomcat_admin_password: { secret: true }
rules:
  - rule: 'params.tomcat_heap_mib * 4 <= size.memoryGiB * 1024 * 3'
    message: "Heap, sunucu belleğinin %75'ini aşamaz"
```

`AnsibleRoleSource` bu iki dosyayı birleştirir ve katalogdaki `AnsibleRole` objesini App ile aynı formatta üretir. Kaynakta iki dosya olsa da katalogda tek format vardır.

#### Kurallar

1. **Kısıtlı tipler.** `raw`, `json`, `jsonarg`, `options`'ı olmayan `dict` ve `elements`'ı olmayan `list` reddedilir. Gerçekten serbest yapı gereken bir parametre için `gaia: { freeform: true }` yazılır. Bu, katalog değişikliğinde `platform-admin` onayı gerektirir.
2. **Secret parametreler.** `secret: true` olan bir parametre sadece `bao://` referansı kabul eder. Referans, kontratın owner'ı olan Team objesindeki yol önekiyle sınırlıdır (`secrets.prefix`, bkz. 14.1); bir takım başka bir takımın secret'ına referans veremez. Değer run sırasında çözülür ve loglarda maskelenir.
   - **Referans biçimi:** `bao://<yol>#<alan>`. Alan yazılmazsa sırrın `value` alanı kullanılır.
   - **Planda:** Plan ve run kaydı referansı taşır, değeri değil. Değer sadece run sırasında çözülür, Ansible'a verilir ve olaylarda maskelenir.
   - **`no_log` parametreler:** Kontratta değer olarak yazılamaz, `bao://` referansı gerekir. Rolün kendi `no_log` varsayılanı planda maskelenir ve Ansible'a geçilmez; rol kendi varsayılanını kullanır.
3. **Kurallar CEL ile.** `rules` ifadeleri `params`, `size`, `image`, `labels` ve `network` değerlerini görebilir. Kubernetes'in CRD'lerde CEL ile yaptığı validasyonla aynı yaklaşım.
   - CEL aritmetikte int ile double'ı karıştırmaz: `size.memoryGiB * 0.75` hata verir. Ya tam sayılarla yazılır (`x * 4 <= y * 3`) ya da `double(...)` ile dönüştürülür. Karşılaştırmalarda karıştırmak serbesttir (`int <= double`).
   - Kurallar yüklemede derlenir; derlenmeyen bir kural validasyonda hata verir. `params` dinamik tipli olduğu için tip uyuşmazlıkları kural çalışırken raporlanır.
4. **İki aşamalı doğrulama.** Gaia plan sırasında doğrular ve hata Change'te (ve önizlemede) hemen görünür. Ansible çalışırken `argument_specs` ile bir kez daha doğrular. Roller için iki doğrulama aynı kaynaktan geldiği için birbiriyle çelişmez.
5. **Varsayılanlar planda görünür.** Kontratta yazılmayan parametrelerin varsayılan değerleri planda açıkça gösterilir. Böylece hangi değerle çalışıldığı run kaydında yer alır.
6. **Uyumluluk.** Bir rolden parametre kaldırılırsa ya da adı değişirse Change'in etki planı o parametreyi kullanan kontratları listeler ve validasyon geçmez. Kaldırma önce `gaia: { deprecated: "yerine x kullanın" }` ile duyurulur.

#### Editör desteği

Gaia belirli bir commit'teki katalogdan kontrat şemasını JSON Schema olarak dışa verir (`gaia schema export`). Şema seçime göre daralır; örneğin `apps[].name: portal` seçildiğinde sadece portal'ın parametreleri önerilir. VS Code'un YAML eklentisi bu şemayla kontrat yazarken otomatik tamamlama yapar ve hataları anında gösterir. JSON Schema elle yazılmaz, Gaia üretir. İlk sürümün kapsamında.

### 6.3 Versiyon güncellemesi

`apps[].params.version` değerinin değişmesi planda rolling update olarak görünür: sunucu load balancer'dan çıkarılır, güncellenir, sağlık kontrolünden geçer ve geri alınır. Böylece uygulama güncellemesi ayrı bir iş olmaktan çıkıp kontrattaki tek bir alanın değişmesine dönüşür.

### 6.4 Operation

`Operation`, istenen durumu değiştirmeyen ama Git'e commit ile giren ve Gaia'da onaylanan tek seferlik işler için kullanılır: patch, reboot, snapshot, replace, drift düzeltmesi.

#### Yaşam döngüsü

```
commit → Change (plan, gereksinimler) → onay → pending → (pencere gelince) running
       → succeeded | failed | partial | cancelled | expired
```

- **Bir kez çalışır.** Çalışma durumu isimle birlikte veritabanında tutulur. Aynı dosya tekrar commit'lense bile yeniden çalışmaz. Tekrar çalıştırmak için yeni isimle yeni bir Operation yazılır.
- **Değiştirilemez.** `pending` veya daha ileri durumdaki bir Operation dosyasını değiştiren commit validasyondan geçmez.
- **`notAfter`:** Bu zamana kadar çalışmamış bir Operation `expired` olur. Örneğin bakım penceresi kaçırılırsa onaylı bir patch haftalar sonra kendiliğinden çalışmaz.
- **Temizlik:** Tamamlanmış Operation dosyalarını Gaia 30 gün sonra bir commit ile siler. Kayıtlar veritabanında ve run kayıtlarında kalır.
- **Sonuç:** Sunucu bazında sonuç Change'te görünür ve bildirim olarak gider.

#### Hedef ve strateji

```yaml
apiVersion: gaia/v1alpha1
kind: Operation
metadata:
  name: patch-2026-11-prod-w2
spec:
  action: patch
  selector: 'labels.env == "prod" && spec.patch.wave == 2'     # CEL
  window: { start: "2026-11-09T02:00:00+03:00", end: "2026-11-09T06:00:00+03:00" }
  notAfter: "2026-11-09T06:00:00+03:00"
  strategy:
    batchSize: 20%            # veya sabit sayı
    maxFailures: 1            # aşılırsa kalan batch'ler durur
    pauseBetweenBatches: 10m
  options: { scope: security, reboot: auto, snapshot: true }
```

- **Hedef onay anında sabitlenir.** Selector plan sırasında çözülür ve etkilenecek sunucular planda listelenerek plan hash'iyle onaylanır. Arada eklenen sunucular çalışmaya dahil edilmez ve raporda belirtilir; arada silinenler atlanır.
- **Standart adımlar:** Her sunucuda Gaia şu adımları kendisi uygular: Zabbix bakım modu, snapshot (istenmişse), load balancer'dan çıkarma, aksiyonun kendisi, sağlık kontrolü, load balancer'a geri alma, bakım modunun kapatılması.
- **Kilit:** Operation etkilediği kontratların kilidini alır. O sırada bir apply çalışıyorsa Operation kuyrukta bekler.

Change'te görünen plan:

```
Operation patch-2026-11-prod-w2
  Hedef      18 sunucu, 6 kontrat · pencere 09.11 02:00–06:00
  Batch      4 batch (5, 5, 4, 4) · hata eşiği 1
  Adımlar    bakım → snapshot → drain → patch (security) → reboot (gerekirse) → sağlık → geri al
  Onay       platform-admin (birden fazla owner var)
```

#### Uygulamada

- **Change:** Bir Operation, kontratlarınkiyle aynı akışta `Operation/<ad>` anahtarlı bir Change'tir.
  - Planı seçilen sunucuları, batch'leri ve adımları sabitler. Onay bu planın hash'ine bağlıdır.
  - Onaylanınca pencerenin başına kuyruklanır; Change `running` olarak görünür.
- **Onaylar:** Policy'deki `operations.approvals` ortam ve aksiyona göre bakılır. Yazılmamışsa owner onaylar. Birden fazla owner'a dokunan Operation'da owner onayının yerine `multiOwner` (platform-admin) geçer.
- **Planlanmayanlar:** Hiç aktif sunucu seçmeyen, `notAfter`'ı veya penceresi geçmiş, ya da Policy'nin istediği pencereyi yazmamış (`requireWindow`) bir Operation için Change açılmaz; sebebi kontrat durumunda görünür.
- **Kimlik:** Çalışma durumu `operations` tablosunda isimle tutulur. Spec'in hash'i onay anındaki haliyle saklanır; dosyayı değiştiren commit reddedilir.
- **Çalışma:**
  - Pencere gelince worker çalıştırır; geç kalmışsa `expired` olur.
  - Batch'ler sırayla gider; her batch kendi kontratlarının kilidini tutar, meşgul bir kontrat batch'i bekletir.
  - Onaydan beri aktif olmayan sunucular atlanır (`skipped`). `maxFailures` aşılınca kalan batch'ler çalışmaz (`not run`).
  - Sonuç `succeeded`, `partial` veya `failed` olur.
  - Sunucu başına sonuçlar `GET /api/v1/operations`'tadır; run kaydı ve bildirimler de bunları içerir.
- **Bu sürümdeki aksiyonlar:**
  - `patch`, `reboot`, `snapshot`, `runbook`; `power`, `revert` ve `replace` sonraki fazlarda.
  - `patch` Red Hat ailesinde dnf ile çalışır (`scope: security` varsayılan veya `all`, `exclude`); yeniden başlatma gereğine `needs-restarting -r` ile bakılır. Debian ailesinde apt ile çalışır ve `/var/run/reboot-required`'a bakılır. `reboot: auto|always|never`.
  - `snapshot` (ya da `options.snapshot: true`), VM'in Proxmox snapshot'ını belleksiz alır; adı `gaia_<operation>_<zaman>`'dır. Snapshot'ı alınamayan sunucu başarısız sayılır ve üzerinde işlem yapılmaz.
- **Batch akışı:**
  - Batch'in sunucuları izleme sisteminde bakım moduna alınır; bakım pencerenin sonuna kadar (pencere yoksa iki saat) sürer ve batch bitince kapatılır.
  - Aksiyondan sonra sağlık kontrolü yapılır: sunucu yeniden cevap veriyor mu.
  - Load balancer adımı LB provider'ı gelince eklenecek.
- **Temizlik:** Bitmiş Operation dosyaları 30 gün sonra Gaia'nın imzalı commit'iyle silinir (kaynak senkronunun yanında); kayıtlar veritabanında ve run kayıtlarında kalır.

#### Tekrarlayan işler: OperationSchedule

```yaml
apiVersion: gaia/v1alpha1
kind: OperationSchedule
metadata:
  name: monthly-patch-prod-w2
spec:
  recurrence: "FREQ=MONTHLY;BYDAY=2SU"      # iCalendar RRULE: her ayın 2. pazarı
  start: "02:00"
  duration: 4h
  createAhead: 7d                           # Operation commit'i pencereden 7 gün önce yazılır
  approval: perInstance                     # perInstance | once
  template:
    action: patch
    selector: 'labels.env == "prod" && spec.patch.wave == 2'
    strategy: { batchSize: 20%, maxFailures: 1 }
    options: { scope: security, reboot: auto, snapshot: true }
```

- **`perInstance`:** Gaia her pencere için bir Operation commit'i yazar; her biri ayrıca onaylanır. Prod için varsayılan budur.
- **`once`:** Schedule bir kez onaylanır, her pencerenin Operation'ı doğrudan oluşturulup çalışır. Onay gerektirmeyen ortamlar için kullanılır.
- **Neden RRULE:** "Her ayın ikinci pazarı" gibi kurallar cron ile düzgün ifade edilemiyor.
- **Uygulamada:**
  - Gaia, kaynak senkronunda, başlangıcı `createAhead` (varsayılan 7 gün) içine düşen her pencere için `<schedule>-<tarih>t<saat>` adlı bir Operation üretir. Penceresi ve `notAfter`'ı pencereye göre yazılır. Dosya `output` dizinine Gaia'nın imzalı commit'iyle girer.
  - `start` saati schedule'ın `timezone`'undadır (IANA; varsayılan UTC).
  - Daha önce üretilenlere dokunulmaz; çalışmış ve dosyası temizlenmiş bir pencere tekrar üretilmez.
  - **`once`:** Schedule kendisi bir Change'tir (`OperationSchedule/<ad>`). Onaycılar şablonun aksiyonu ve selector'ın şu anda dokunduğu owner ve ortamlardan hesaplanır. Onaylanınca hiçbir şey çalışmaz; schedule bu haliyle onaylı sayılır. Bu haliyle üretilen Operation'ların onay gereksinimi "schedule ile onaylı" olarak geçer. Schedule değişirse yeniden onay gerekir.

#### Aksiyonlar

| Aksiyon | Ne yapar | Not |
|---|---|---|
| `patch` | OS güncellemesi (Linux paket yöneticisi, Windows Update) | `scope`, `reboot`, hariç tutulanlar |
| `reboot` | Kontrollü yeniden başlatma | Drain ve sağlık kontrolüyle |
| `power` | VM'i açar, kapatır veya yeniden başlatır | Varsayılan graceful |
| `snapshot` | Snapshot alır | İsim ve açıklama standardıyla |
| `revert` | Snapshot'a geri döner | Veri kaybına yol açar |
| `replace` | Sunucuyu aynı isim ve IP ile yeniden kurar | Bkz. 9.5 |
| `reconcile` | Kontratı onaylı commit'e yeniden yakınsatır | Drift düzeltmesi; genelde bot açar (bkz. 7.5) |
| `runbook` | Katalogdaki bir runbook rolünü çalıştırır | Aşağıda |
| `discover` | Sunucu içi keşif: fact'ler ve check-mode, değişiklik yapmaz | Bkz. 8.3 |

**runbook.** Serbest Ansible çalıştırılamaz. Sadece `meta/gaia.yml` dosyasında `runbook: true` işaretli `AnsibleRole`'ler çalıştırılabilir; parametreleri `params` şemasından geçer (bkz. 6.2). Yeni bir operasyonel iş için Gaia'ya kod yazmak gerekmez, bir rol eklemek yeterlidir.

```yaml
spec:
  action: runbook
  runbook: web.tomcat-restart
  selector: 'contract == "portal-web"'
  params: { graceful: true }
```

#### Onay kuralları

Onaylar Gaia'da, Change'in gereksinimi olarak toplanır (bkz. 7.6). Kimin onaylaması gerektiği Policy'den gelir; Git'teki review'lar sayılmaz. Prod'da silme için gereken çift onay da aynı mekanizmayla uygulanır (bkz. 9.9).

```yaml
kind: Policy
spec:
  operations:
    approvals:
      prod:
        default: [owner]
        revert:  [owner, platform-admin]
        replace: [owner, platform-admin]
      default:
        default: [owner]
    multiOwner: platform-admin           # birden fazla owner'ı etkileyen işler
    requireWindow:
      prod: [patch, reboot, power, revert, replace]
```

**Birden fazla takımı etkileyen işler:** Selector birden fazla takımın kontratlarına dokunuyorsa her owner'dan ayrı onay beklenmez. `platform-admin` onayı yeterlidir; owner'lar bildirim alır.

#### Durdurma ve duraklatma

Yanlış giden bir işi durdurmak için yeni bir commit ve onay beklemek çok yavaş kalır. Bu yüzden `operator` rolü çalışan bir run'ı arayüzden veya CLI'dan:
- **durdurabilir** (cancel),
- **duraklatabilir** (pause): kalan batch'ler bekletilir.

Durdurmak istenen durumu değiştirmez, sadece yapılan işi azaltır. Kimin durdurduğu run kaydına yazılır. Kaldığı yerden devam ettirmek (resume) bir onaydır: Change'te gereken onaycılardan biri verir.

### 6.5 Çoklu site: `topology`

Bir kontrat birden fazla site'a yayılabilir. Tek site için `network` + `replicas` kısa yazımı kalır; birden fazla site için `topology` kullanılır. Site'lar farklı hipervizörlerde olabilir.

```yaml
spec:
  # tek site
  network: ank-prod-app
  replicas: 3

  # veya birden fazla site
  topology:
    - network: ank-prod-app      # vSphere
      replicas: 2
    - network: ist-prod-app      # Proxmox
      replicas: 2
```

**Kurallar**

- **İsimler:** Sunucu isimleri site'ı içerdiği için çakışmaz (`ankppor01`, `istppor01`). Sıra numarası her site için ayrı ilerler.
- **Stack:** Her kontrat ve site çifti için ayrı bir Pulumi stack'i açılır. Bir site'taki sorun diğerinin stack'ini etkilemez.
- **Kısmi başarısızlık:** Bir site'ın hipervizörüne erişilemezse o site'ın aşamaları başarısız olur, diğer site devam eder ve run sonucu `partial` olur.
- **Sıralı ilerleme:** Rolling update'ler ve Operation'lar varsayılan olarak site site ilerler; bir site tamamen bittikten sonra diğerine geçilir. Böylece bir hata iki site'ı birden etkilemez. Sıra Operation strategy'sinde `siteOrder: [ank, ist]` ile belirlenebilir.
- **Site kaldırma:** `topology` listesinden bir satırın kaldırılması o site'taki sunucuların decommission edilmesi demektir ve silme semantiğine (bölüm 9) tabidir. Önce satırdaki `replicas` 0 yapılır, decommission tamamlanınca satır kaldırılır. Yaşayan sunucusu olan bir satırı doğrudan silen PR'ı CI reddeder.
- **Site'a bağlı bileşenler:** Zabbix proxy, NTP, site worker ve DNS zone zaten Site'tan gelir.

## 7. Çözümleme ve Yürütme

### 7.1 Yükleme

1. Platform reposu belirli bir commit'ten okunur.
2. Objelerden bir graf kurulur. Kontrattan kataloğa (`network → Network`) ve katalog içinde (`Network.site → Site → Provider`) bütün referanslar çözülür.
3. Çözülemeyen referans, döngü, şema hatası, tekil olmayan isim ya da hash'i tutmayan üretilmiş obje varsa **hiçbir şey uygulanmaz.**

### 7.2 Plan ve aşamalar

```
$ gaia plan portal-web.yaml
Çözümleme
  network ank-prod-app → vc-ank / DC-ANK / CL-PROD-01, dvPG-VLAN120, 10.20.120.0/24, corp.local
  image   rhel9        → tpl-rhel9-latest (ssh)
  isimler              → ankppor01, ankppor02, ankppor03
Plan
  ipam      + 3 IP rezervasyonu
  pulumi    + 3 VirtualMachine, tag'ler
  corp-ad   + 3 DNS A/PTR kaydı
  zabbix    + 3 host
  warpgate  + 3 SSH hedefi              (Windows imajlarında RDP)
  ansible   ▸ linux-cis-l1 → web.nginx → web.tomcat → portal 2.4.1 → ajanlar
env=prod → onay gerekli
```

Aşama sırası: `validate → ipam → pulumi → register → ansible → verify`. Bir aşama başarısız olursa sonraki aşamalar çalışmaz ve run'ın hangi aşamada durduğu kayıtta görünür.

### 7.3 Katalog değişikliğinin etkisi

Bir katalog commit'inde Gaia, değişikliğin kaç kontratı ve kaç sunucuyu etkilediğini gösteren bir etki planı üretir ve etkilenen kontratların Change'lerini birlikte gösterir. Değişikliğin mevcut sunuculara nasıl yayılacağı kind'a göre sabittir:

| Kind | Mevcut sunuculara etkisi |
|---|---|
| Image | Yok, sadece yeni VM'lerde kullanılır |
| Size | Drift olarak raporlanır, otomatik uygulanmaz |
| AnsibleRole, Baseline, App, Agent | Sonraki apply'da uygulanır |
| Network, Site | Değiştirilemez; değişiklik için yeni obje açılır |

### 7.4 Ansible'ın çalıştırılması

Ansible'ı harici bir runner (AWX, Semaphore) değil, **Gaia kendisi çalıştırır.** Zamanlama, RBAC, onay, iz kaydı ve canlı log zaten Gaia'da var. Harici bir runner bunların hepsini ikiye katlar ve kendi credential deposunu ister, bu da credential'ların sadece OpenBao'da durması kuralını bozar.

**Motor: ansible-runner.** AWX'in de içeride kullandığı çalıştırma motoru. Ayrı bir servis değil, bir kütüphane ve CLI. Gaia worker'ı her run için şu adımları izler:

1. Geçici bir çalışma dizini açar (tmpfs). Envanteri ve değişkenleri plandan üretip buraya yazar.
2. Credential'ları OpenBao'dan **o anda** çeker ve bu dizine koyar. Run bitince dizin silinir.
3. `ansible-runner run -j` komutunu execution environment container'ı içinde çalıştırır.
4. Komutun JSON olay akışını satır satır okur. Olaylar `run_events` tablosuna yazılır ve canlı olarak arayüze gider; içlerindeki secret değerleri maskelenir.

**Envanterde UID:** `inventory_hostname` sunucunun UID'si, `ansible_host` ise IP veya FQDN'idir. Böylece her olay doğrudan UID ile gelir ve hostname tekrar kullanılsa bile loglar karışmaz (bkz. 9.1).

**Execution environment (EE):** Collection'ların ve Python bağımlılıklarının sürümleri sabitlenmiş bir container imajı. `ansible-builder` ile üretilir ve Zot'ta tutulur.
- Şimdilik tek bir varsayılan EE var. Rollerin farklı EE seçmesi gerekirse bir `ExecutionEnvironment` kind'ı eklenir.
- EE imajının digest'i run kaydına yazılır. Böylece bir run tamamen tekrar üretilebilir: kontrat commit'i, katalog commit'i, config reposu commit'i ve EE digest'i.

```yaml
kind: Platform
spec:
  engines:
    configuration:
      tool: ansible
      executor: ansible-runner
      executionEnvironment: registry.corp.local/gaia/ee-default@sha256:4c1e…
      source: config-repo
      limits: { concurrentRuns: 10, forks: 25, timeout: 2h }
```

**Gaia'nın sorumlulukları:** Run'ı iptal etmek, timeout'u uygulamak, eşzamanlılık limitlerini korumak ve container'ı temizlemek. Kalan işi ansible-runner yapar.

**Site worker'ı:** Ayrılmış bir ağ segmentindeki sunuculara merkezden SSH/WinRM ile erişilemiyorsa o segmentte bir site worker'ı çalışır (`gaia server --role=worker --site=dmz`).
- Site worker veritabanına bağlanmaz. Gaia API'sine mTLS ile, sadece dışarıya doğru bağlanır, iş alır ve olayları geri gönderir. Böylece segmentin içine doğru firewall izni açmak gerekmez.
- Pulumi işleri ve day-2 işleri merkezde kalır; hipervizör API'leri (vCenter, Proxmox) oradan erişilebilir durumda.
- Site worker tasarımda var, ama gerçekten ihtiyaç doğana kadar yazılmaz.

**PoC'de doğrulananlar:** Çalışma dizini tmpfs'te; EE imajı rootless podman'da `--userns keep-id` ile çalışır (imajın kullanıcısı çağırana eşlenir). Gaia projeye bir `.gaia/site.yml` yazar: baseline'ı import eder, sonra kontratın rollerini parametreleriyle çalıştırır. Olaylardaki secret'lar event_data çözüldükten sonra maskelenir (Python ASCII dışı karakterleri escape ediyor). İş, imajın digest'iyle çalışır ve bu digest run'a yazılır.

**Runner soyutlaması:** Ansible'ı çalıştıran katman bir arayüzün arkasında durur. İleride AWX gibi bir runner zorunlu tutulursa bu arayüze yeni bir uygulama eklenebilir. Şimdilik tek uygulama ansible-runner.

### 7.5 Drift kontrolü

Drift, kontratın Git'teki istenen durumu ile sistemlerin gerçek durumu arasındaki farktır. Her alanın tespit yöntemi, maliyeti ve riski farklı olduğu için drift alan bazında ele alınır.

#### Alanlar

| Alan | Tespit yöntemi | Maliyet | Varsayılan sıklık |
|---|---|---|---|
| Kayıtlar (Zabbix, DNS, IPAM, Warpgate, AD, hipervizör tag'leri) | API'den okuyup karşılaştırma | Düşük | 15 dk |
| Uygulama versiyonu | `App` objesindeki `versionProbe` | Düşük | 10 dk |
| Provisioning (VM boyutu, disk, network) | Pulumi `preview --refresh`; state'e yazmaz | Orta | 1 saat |
| Konfigürasyon (OS, roller, baseline) | Ansible `--check --diff`; aynı commit ve aynı EE ile | Yüksek: her sunucuya SSH/WinRM | Günde bir, gece penceresinde |
| Katalog sapması | Otomatik yayılmayan katalog değişiklikleri (ör. Size, bkz. 7.3) | Yok: plan sırasında çıkar | Her katalog değişikliğinde |
| Sahipsiz kaynaklar | `gaia.uid` taşıyıp veritabanında karşılığı olmayanlar; Gaia'nın yönettiği folder'larda UID'siz VM'ler | Orta (keşif motoru) | Günde bir |
| Süresi dolan istisnalar | Kontrattaki `exceptions` | Yok | Günde bir |
| Erişim | AD üyelik değişiklikleri ve devre dışı hesaplar (USN) ile dağıtılmış erişimin karşılaştırılması; her ortamda otomatik düzeltilir (bkz. 14.5) | Düşük | 5 dk |

İmajın yeni bir şablonu çıkmış olması drift sayılmaz, çünkü imaj sadece VM oluşturulurken kullanılır. Bu durum bilgi amaçlı "eski şablonla kurulmuş" notu olarak gösterilir.

**Uygulama versiyonu** tam bir check-mode çalıştırmadan, ucuz bir sorguyla izlenir:

```yaml
kind: App
spec:
  versionProbe:
    http: { path: /actuator/info, port: 8080 }
    extract: 'body.build.version'          # CEL
```

#### Ansible check-mode

Check-mode her rolde güvenilir değildir: `command` ve `shell` task'ları check-mode'da atlanır, bazı roller ise check-mode'da hata verir. Bu yüzden:

- **Rol yazma kuralı:** Roller check-mode uyumlu olmalıdır. `command` ve `shell` task'ları `changed_when`, `creates` veya `check_mode` ile doğru işaretlenir.
- **Uyumsuz roller:** Uyumlu olmayan bir rol `meta/gaia.yml` içinde `checkMode: unsupported` olarak işaretlenir. Bu rolün drift sonucu "temiz" değil, **"bilinmiyor"** olarak gösterilir, böylece yanlış bir güven oluşmaz.

#### Drift bulununca

Davranışı alanın yönetim seviyesi belirler (bkz. 8.1):

| Seviye | Davranış |
|---|---|
| `observe` | Kaydedilir, arayüzde görünür |
| `report` | Kaydedilir ve bildirim gönderilir |
| `enforce` | Kaydedilir, bildirim gönderilir ve Policy'ye göre düzeltilir |

İstenen durum değişmez, zaten onaylanıp uygulanmış bir commit'te durur. Değişen, gerçek durumdur. Düzeltme bir Change'tir ve kaynağı commit değil drift'tir (bkz. 7.6).

- **`auto`:** Gaia aynı commit'e yeniden yakınsar; Change'in gereksinimi yoktur. Run kaydında `requestedBy: gaia-drift` yazar; onay olarak orijinal commit'in onayına referans verir. Sadece düşük riskli sınıflarda kullanılır.
- **`approval`:** Gaia drift raporuyla birlikte bir düzeltme Change'i açar; düzeltme onaylanınca çalışır. Prod ve yüksek riskli sınıflar için kullanılır.

```yaml
kind: Policy
spec:
  drift:
    registration:  { every: 15m }
    versionProbe:  { every: 10m }
    provisioning:  { every: 1h }
    configuration: { every: 24h, window: "02:00-05:00", concurrency: 20 }
    remediation:
      registration:  auto                         # her ortamda
      configuration: { default: auto, prod: approval }
      provisioning:  approval                     # VM boyutu, disk: hiçbir zaman otomatik değil
```

**Drift'i kabul etmek:** Bilinçli bir sapma kontrata `exceptions` kaydı eklenerek kabul edilir. Bu bir commit olduğu için karar Git'te kayıt altında kalır. Arayüzden drift kabul etmenin bir yolu yoktur.

**Çakışma:** Bir kontrat için apply çalışırken o kontratta drift taraması yapılmaz; kontrat başına alınan kilit bunu sağlar.

**Uygulamada (kayıt alanı):**
- Tarama, change motorunun çalıştığı sunucuda her dakika hangi alanların zamanı geldiğine bakar (Policy `drift.<alan>.every`, `window`); her alanı tek bir replika tarar (advisory lock, `drift_scans`).
- Kayıt alanı her kontratın son uygulanan planındaki aktif sunucuları, kaydedilen ID'lerle kontrol eder: IPAM'daki adres; Zabbix host'u, açık olması, adresi, host grupları ve template'leri; DNS A ve PTR kayıtları; AD bilgisayar hesabı ve açık olması; Warpgate hedefleri, adresleri ve rolleri. Sisteme ulaşılamazsa bulgu `unknown` olur.
- Bulgular `drift_reports`'ta `(sunucu, alan, anahtar)` ile tekilleşir; görülmeyen bulgu `resolved` olur. Yeni bulgular `drift.found` bildirimini tetikler.
- Düzeltme bir Change'tir ve uygulanan planı tekrar uygular: `remediation: auto` için `drift-auto` (onay istemez; onay olarak uygulanan planı gösterir), `approval` için `drift` (owner onayı ister, ortamın onay ayarından bağımsız). Değerlendirme bu Change'i "Git uygulanana döndü" diye kapatmaz. Kontratta açık bir Change varsa yenisi açılmaz; başarısız olan drift Change'i aynı planla kendiliğinden tekrar açılmaz. Drift kendiliğinden geçerse bekleyen düzeltme Change'i `superseded` olur.
- `GET /api/v1/drift[?contract=]` ve `gaia drift [kontrat]` açık bulguları gösterir.
- `gaia reconcile KONTRAT` (`POST /api/v1/contracts/{name}/reconcile`, operator) taramayı beklemeden, uygulanan planı yeniden uygulayan bir `drift` Change'i açar; owner onaylar.
- **Provisioning:** Her stack, son başarılı run'ın uyguladığı VM tanımlarıyla (`hosts.vm_spec`, bekleyen sunucular kapalı olarak) hipervizörden yenilenip önizlenir (`pulumi preview --refresh`, state'e yazmaz). Pulumi'nin değiştireceği her VM bir bulgudur (işlem ve farklı alanlar); programda olmayıp state'te olan VM de görünür.
- **Konfigürasyon:** Kontratın play'i aktif sunucularda `--check --diff` ile, run'ın config commit'iyle çalıştırılır. Değişiklik yapacak task'lar bulgudur; check'in bitmediği sunucu `unknown` olur. Varsayılan pencere 02:00-05:00'tir (sunucunun saati).
- Varsayılanlar: kayıt 15 dakikada bir ve `auto`; provisioning saatte bir ve konfigürasyon günde bir, ikisi de `approval`. Düzeltme Change'i uygulanan planı yeniden uygular: Pulumi VM'i plana döndürür, Ansible yapılandırmayı.
- Henüz yok: `versionProbe`, check-mode desteklemeyen rollerin `meta/gaia.yml` ile işaretlenmesi, `concurrency`, drift sayılarının Zabbix'e trapper olarak gönderilmesi ve `exceptions` ile kabul.

#### Kayıt, gösterim ve bildirim

- **Tekilleştirme:** `drift_reports` tablosunda her drift `(host_uid, alan, anahtar)` ile tekilleştirilir; aynı sapma her taramada yeni kayıt oluşturmaz. Kayıtta `first_seen`, `last_seen` ve `resolved_at` alanları vardır. Durum `open`, `resolved`, `accepted` (istisnaya dönüştü) veya `unknown` (check-mode desteklenmiyor) olabilir.
- **Arayüz:** Genel bakışta alan bazında açık drift sayıları görünür. Kontrat sayfasında sunucu ve alan bazında diff gösterilir (Pulumi diff veya Ansible diff, secret'lar maskeli). İlk görülme zamanı ve varsa düzeltme Change'i de yanında yer alır.
- **Olası kaynak:** Drift'in ilk görüldüğü zaman aralığında aynı sunucuda bir Warpgate oturumu varsa, arayüz bunu "olası kaynak" olarak gösterir.
- **Bildirim:** Gaia drift sayılarını Zabbix'e trapper item olarak gönderir, alarmları Zabbix'teki trigger'lar üretir; böylece mevcut eskalasyon düzeni drift için de çalışır. Ayrıca bildirim kanallarına (bkz. 7.6) "drift bulundu" olayı gider.

### 7.6 Değişiklik akışı: Git geçmişi tutar, Gaia yönetir

Model Argo CD'den esinlenir. Git istenen durumun geçmişidir; değişikliğin incelenmesi, onaylanması ve uygulanması Gaia'da olur. Bu bölüm Gaia'nın omurgasıdır: arayüz, onaylar, drift düzeltmesi, Operation'lar ve bildirimler aynı akışı kullanır.

**İzlenen kaynaklar.** Platform reposu, kontrat repoları ve config reposu Gaia'nın takip ettiği repolardır. Gaia her birinin izlenen branch'ini (`ref`) takip eder.
- Platform reposunu sunucunun kendi ayarı söyler (`gaia server --repo`, `--repo-credentials`). Kontrat repoları `Platform.spec.contracts`'ta, config reposu `engines.configuration.source`'ta birer `git` Provider'ı olarak yazılıdır.
- Her repo yerel bir aynada (bare clone) tutulur. Kimlik bilgisi OpenBao'dadır: https için token; ssh için anahtar ve `known_hosts`. Host anahtarı bilinmeyen bir sunucuya bağlanılmaz.
- **Anlık görüntü (snapshot):** İstenen durum, platform reposu ile kontrat repolarının birer commit'idir. Plan, Change ve run bu görüntüye bağlıdır. Config reposunun commit'i de görüntüde sabitlenir, ama planın kimliğine girmez: rol kodu bir sonraki run'da uygulanır (7.3).
- Yeni commit'i webhook ile hemen, webhook gelmezse periyodik fetch ile (varsayılan 3 dakika) öğrenir. GitHub webhook'u `POST /api/v1/webhooks/github` adresine `push` olayıyla gelir ve `X-Hub-Signature-256` ile doğrulanır (sır OpenBao'da, `gaia server --webhook-secret`). Gaia olayın içeriğine güvenmez; geçerli bir olay sadece fetch'i öne alır, commit'ler Git'ten okunur.
- PR, review ve branch koruması ekiplerin tercihidir; Gaia bunlara bakmaz ve onların yerine geçmez. Branch korumasının ve CI'da `gaia validate`'in açık olması önerilir.
- İzlenen branch'in son commit'i doğrulanmıyorsa o repodan hiçbir Change uygulanmaz; Gaia hatayı gösterir.

**Change.** Gaia yeni bir commit'te grafı çözer ve her kontratın planını mevcut durumla karşılaştırır. Planı değişen her kontrat için bir Change açılır.
- **Birim kontrattır**, Argo CD'deki Application gibi: her kontratın kendi gereksinimleri, onaycıları ve sonucu olur. Aynı commit'ten doğan Change'ler arayüzde birlikte görünür.
- **İçeriği:** commit aralığı (uygulanan → hedef) ve commit sahipleri, YAML diff'i, plan ve uygulanan plana göre plan diff'i, gereksinimler ve her birinin durumu.
- **Kontrat başına tek açık Change** olur ve her zaman izlenen branch'in son commit'ini hedefler. Ara commit'ler ayrı ayrı uygulanmaz.

**Gereksinimler (gates).** Hepsi katalogdan hesaplanır; arayüz her biri için kimin ne yapması gerektiğini isimleriyle yazar ("team-portal onaycılarından biri: ayse.k, mehmet.y", "platform-admin", "Cmt 02:00 penceresi").

| Gereksinim | Kaynağı |
|---|---|
| Validasyon | Yükleme, referans grafı, `params` ve CEL kuralları |
| İmza | Commit'ler imzalı, imzalayan Gaia'da kayıtlı |
| Onay | Policy (`environments`, `operations.approvals`, silme kuralları), Group (`privileged`), çoklu owner kuralı |
| Pencere | Policy `requireWindow`, Operation `window` |
| Kilit | Kontratta çalışan başka bir run yok |

**Onay.**
- Onay Gaia'da verilir: arayüzden ya da `gaia change approve` ile (komut, Change'in onay sayfasını tarayıcıda açar). Onay anında passkey veya MFA yeniden istenir (step-up, 10 dakika geçerli). Terminal token'ı tek başına onay veremez.
- Onay ve red, Change'in plan hash'iyle gönderilir: onaycının gördüğü plan artık Change'in planı değilse karar kabul edilmez. Kararlar silinmez ve değiştirilmez.
- Onaycıların Git hesabı olması gerekmez. Kimlikleri AD'den gelir (Team'in `approversGroup`'u, Policy'deki `gaiaRoles`).
- Onay, Change'in o anki **plan hash'ine** bağlıdır. Yeni bir commit kontratın planını değiştirmezse onay geçerli kalır; değiştirirse düşer.
- **Kendi kendini onaylamak yok.** Commit sahibi kendi Change'ini onaylayamaz. Erişim veren bir değişikliği, o erişimi alacak takımın üyesi onaylayamaz (bkz. 14.3).
- **Red:** Onaycı gerekçesiyle reddeder. Reddedilen Change, kontratı değiştiren yeni bir commit gelene kadar bekler.
- **Gruplu onay:** Birden fazla owner'ı etkileyen bir commit'in Change'lerini (ör. bir katalog değişikliği) `platform-admin` tek seferde onaylayabilir. Bir Change'in getirdiği commit'lerden biri başka bir owner'ın açık Change'inde de varsa, owner onayını `platform-admin` de verebilir. Aynı commit'in Change'leri `?commit=` ile listelenir, toplu onay her Change için ayrı karar olarak kaydedilir.

**Uygulama.** Bütün gereksinimler sağlanınca Gaia uygular; ayrıca bir "uygula" adımı yoktur. Onay gerektiren bir ortamda sunucuları değiştiren plan sadece onaylanmış bir Change'in run'ıyla uygulanır; `gaia apply` onu reddeder. Onay gerektirmeyen ortamlarda bu, commit gelir gelmez olur (Argo CD'deki auto-sync).
- Uygulamadan hemen önce plan yeniden alınır. Hash onaylananla aynı değilse (arada gerçek durum değiştiyse) uygulama durur ve Change yeniden onaya düşer.
- **Asıl koruma Gaia'dadır.** Git'e yazabilen biri istediği commit'i atabilir; Gaia o commit'i ancak gereksinimler sağlanınca uygular. Uygulamanın kanıtı Git'teki bir merge değil, Gaia'daki onay kaydıdır.

**Yaşam döngüsü:**

```
pending ──gereksinimler──► approved ──pencere──► running ──► applied | failed
   │                          │
   ├─ rejected                └─ expired (zamanında uygulanamadı)
   └─ superseded (aynı kontrata yeni bir commit geldi ve planı değiştirdi)
```

**Başarısız Change.** Başarısız, iptal edilmiş veya reddedilmiş bir Change aynı planla ve aynı commit'lerle kendiliğinden tekrar açılmaz; döngüye girmemek için. Yeni bir commit ya da `operator`'ün "tekrar dene" (`retry`) komutu yeni bir Change açar ve gereksinimler yeniden kontrol edilir.

**Plan hash'i.** Planın ne yaptığını tanımlar, nereden geldiğini değil: commit etiketi hash'e girmez. Kontratın planını değiştirmeyen bir commit Change'i ve onaylarını korur; Change yeni commit'e kayar.

**Değerlendirme.** Her repo yenilemesinde, bir run bitince ve bir onay geldiğinde yapılır. Run'ı worker'a hiç ulaşmadan biten (kuyruktayken iptal edilen) ya da worker'ı ölen bir Change, sonraki değerlendirmede run'ın sonucuna göre kapatılır. Aynı anda tek replika değerlendirir (advisory lock); değerlendirme tekrar tekrar çalıştırılabilir. Bir run, tamamlandığında kontratın "uygulanan" durumunu (görüntü ve hemen sonra alınan plan hash'i) yazar.

**Kontrat durumu.** Argo CD'deki gibi iki eksen:
- **Sync:** `Synced` (son commit uygulandı), `OutOfSync` (açık bir Change var), `Drifted` (gerçek durum sapmış).
- **Sağlık:** son run'ın sonucu ve izleme durumu.

**Commit sahibi.** Commit imzası zorunludur.
- Herkes imza anahtarını (SSH veya GPG) Gaia profiline kaydeder. Gaia imzayı doğrular ve commit'i AD kimliğine bağlar. Bu Git sunucusundan bağımsız çalışır.
- İmzasız ya da tanınmayan bir anahtarla imzalanmış commit'in Change'i "imza" gereksinimini sağlamaz ve uygulanmaz.
- Run kaydındaki `requestedBy` buradan gelir.

**Gaia'nın yazdıkları.** Source'ların ürettiği objeler, decommission sonrası kontratın silinmesi, Operation temizliği ve geri almalar Gaia'nın kendi anahtarıyla imzalanmış commit'lerdir ve doğrudan izlenen branch'e yazılır. Etkileri normal bir Change olarak görünür ve aynı kurallarla onaylanır; örneğin katalog değişikliği `platform-admin` ister. Gaia doğrulamayan bir commit yazmaz.

**Geri alma.** Git tek geçmiş olduğu için geri alma da bir commit'tir. Arayüzde "geri al" denince Gaia ilgili commit'in revert'ünü yazar; bu da bir Change olarak onaydan geçer. Gaia hiçbir zaman izlenen branch'in son halinden farklı bir şey uygulamaz.
- Gaia'nın anahtarı OpenBao'dadır (`gaia server --signing-key`, OpenSSH `private_key`). Gaia açılışta bu anahtarın açık yarısını `gaia` sistem hesabına kayıtlı imza anahtarı olarak ekler. Bu hesap giriş yapamaz: passkey'i, parolası ve OIDC kimliği yoktur.
- Gaia commit'i kimin için yazdıysa onu `Gaia-Requested-By` trailer'ına yazar. Motor, Gaia'nın imzaladığı commit'lerde commit sahibi olarak bu kişiyi alır. Kişi bu commit'in Change'ini onaylayamaz. Başkalarının commit'lerindeki trailer'a bakılmaz.
- Revert, commit'in değiştirdiği dosyaları ondan önceki hallerine döndürür. Sonraki bir commit aynı dosyaları değiştirmişse Gaia revert'ü yazmaz ve o dosyaları sayar; birleştirme elle yapılır. Merge commit'leri revert edilmez.
- **Yazma yetkisi:**
  - Gaia sadece URL ile izlediği repolara yazar; yerel bir çalışma kopyasına yazmaz. İtme, okumada kullanılan kimlik bilgisiyle yapılır; bu kimlik bilgisinin yazma yetkisi olmalıdır.
  - Gaia sadece yazmaya başladığı baş commit'in üzerine iter; arada branch ilerlemişse yeniden denenir.
  - Operator her commit'i revert ettirebilir; diğerleri sadece Change'leri tamamen kendi kontratlarına olan commit'leri.

**Önizleme.** İzlenmeyen bir branch veya PR için plan istenebilir (`gaia preview REF`, ileride PR yorumu olarak). Önizleme hiçbir şey uygulamaz ve Change açmaz.
- Sunucu, izlenen bir platform veya kontrat reposunun ref'ini (branch, `refs/tags/…`, GitHub PR'ı için `pull/N` ya da tam commit SHA'sı) aynaya ayrı bir yere (`refs/gaia/preview/…`) çeker. Diğer repolar başlarında kalır. Sonuç, her kontratın planı ve uygulanan planla farkıdır. Ref'te artık olmayan uygulanmış kontratlar "kaldırılıyor" olarak gösterilir. Görme yetkisi olmayan kontratların sadece adı görünür.
- `gaia plan --ref` aynı işi yerel bir repodan ve veritabanı erişimiyle yapar.

**Bildirimler.** argocd-notifications'tan esinlenir.
- **Tetikleyiciler:** onay bekliyor, onaylandı, reddedildi, uygulandı, başarısız, drift bulundu, onayın süresi doluyor.
- **Kanallar:** Slack, Microsoft Teams, e-posta (SMTP) ve webhook. Her biri bir Provider'dır (`slack`, `msteams`, `smtp`, `webhook`).
- **Abonelik:** Team başına (`Team`'e patch ile eklenen `notifications`) ve kişi başına (Gaia profili). Onay bekleyen bir Change, onaylayabilecek takımların kanallarına gider.

```yaml
kind: Provider
metadata: { name: slack-portal }
spec: { type: slack, credentials: bao://kv/platform/notify/slack-portal }   # url: gelen webhook adresi
---
kind: Provider
metadata: { name: mail }
spec: { type: smtp, endpoint: "smtp.corp.local:587", from: gaia@corp.local, credentials: bao://kv/platform/notify/smtp }
---
kind: Team
metadata: { name: team-portal }
spec:
  notifications:
    - { provider: slack-portal, triggers: [change.pending, change.failed] }
    - { provider: mail, triggers: [change.applied], to: [portal-ops@corp.local] }
```

- **Kanal tipleri:**
  - `slack` ve `msteams`: gelen webhook'un adresi bir sırdır (`credentials` içinde `url`)
  - `webhook`: `endpoint`'e JSON gönderir; `credentials` içinde `secret` varsa gövde `X-Gaia-Signature-256` ile imzalanır
  - `smtp`: `endpoint` (host:port) ve `from`
- **Tetikleyiciler:** `change.pending`, `change.approved`, `change.rejected`, `change.applied`, `change.failed`, `drift.found`, `approval.expiring`.
- **Kişisel abonelikler** `Platform.spec.notifications.email`'deki smtp kanalından gider. Kişi tetikleyicilerini profilinden seçer (`gaia notifications set`). Adres OIDC hesaplarında sağlayıcının doğruladığı `email`'dir ve her girişte güncellenir; yerel hesaplarda kişi kendisi yazar.
- **Kime gider:**
  - kontratın owner takımının kanalları
  - onay bekleyen bir Change için ayrıca onaylayabilecek takımların kanalları
  - tetikleyiciye abone olan ve kontratı tam görebilen kişiler (owner takımı, operator, platform-admin)
- **Gönderim:**
  - Her bildirim `notifications` tablosuna bir satır olarak yazılır ve teslimi kuyruğa girer. Bir olay (Change + tetikleyici) her alıcıya bir kez gider; motor aynı Change'i tekrar tekrar değerlendirse de tekrar gönderilmez.
  - Başarısız teslim yaklaşık bir gün boyunca, aralıklar uzayarak tekrar denenir; tablo son hatayı ve deneme sayısını tutar.
  - Bildirim bir Change'i bekletmez.
- **İçerik:** başlık ("poc-web: değişiklik onay bekliyor") ve satırlar (değişiklik, isteyen, plan özeti, onaylayabilecekler, onaylayan, karar ve gerekçe, run, hata, bağlantı).
  - Teams'e Adaptive Card gider.
  - `webhook` kanalına olayın kendisi JSON olarak gider (`X-Gaia-Event`; sır varsa `X-Gaia-Signature-256`).
  - E-posta düz metindir; 465'te baştan TLS, diğer portlarda sunucu destekliyorsa STARTTLS kullanılır. Kimlik bilgisi TLS olmadan gönderilmez.
- **Team objesi:** TeamSource gelene kadar (Faz 7) elle yazılır. Katalogda Team varsa kontratın `owner`'ı bir Team olmak zorundadır; Team'i olmayan eski kataloglar yüklenmeye devam eder.

## 8. Keşif ve Adopt (Mevcut Sunucuları İçeri Alma)

### 8.1 Akış

1. **Keşif.** Hipervizörler (vCenter, Proxmox), Zabbix, AD, DNS ve IPAM taranır. Kayıtlar VM UUID'si, hostname ve IP üzerinden eşleştirilir. Çıktı bir rapordur: her yerde kaydı olanlar, eksik kayıtlar, sahipsiz IP'ler. Keşfin kapsamı 8.3'te.
2. **Taslak kontrat.** Gözlenen değerler kataloğa ters yönde eşlenir (portgroup → `network`, CPU/RAM → `size`). Eşleşmeyenler açıkça işaretlenir.
3. **İnceleme.** Taslak arayüzde hazırlanır. Keşfin bilemeyeceği alanları (owner, app, ortam) insan tamamlar; tamamlanınca Gaia commit'i yazar.
4. **Import.** Kaynaklar Pulumi stack'ine `import` edilir, IP'ler IPAM'a kaydedilir. Bu adımın planı **sıfır değişiklik** göstermek zorundadır.
5. **Kademeli sıkılaştırma.** Her alan ayrı ayrı `observe → report → enforce` seviyesine yükseltilir.

### 8.2 Adopt edilmiş kontrat

```yaml
apiVersion: gaia/v1alpha1
kind: Server
metadata:
  name: legacy-portal
  labels: { app: portal, env: prod, owner: team-portal }
  adoption: { discovery: disc-2026-10-28 }
spec:
  management:
    provisioning: observe          # VM'e dokunma, sadece drift raporla
    configuration: report          # Ansible check/diff modu
    registration: enforce          # Zabbix, DNS, IPAM, tag'ler düzeltilsin
  hosts:
    - { name: portalsrv01, vm: vc-ank:vm-1043, ip: 10.20.120.15 }
    - { name: portalsrv02, vm: vc-ank:vm-1044, ip: 10.20.120.16 }
  image: { os: rhel8 }
  size: custom
  sizeOverride: { cpu: 6, memoryGiB: 12 }
  network: ank-prod-app
  baseline: linux-cis-l1
  ansibleRoles: [web.nginx, web.tomcat]
  exceptions:
    - rule: baseline/ssh-root-login
      reason: "Eski deploy script'i root ile bağlanıyor"
      expires: 2027-03-31
      approvedBy: sec-team
```

**Kurallar**

- **`hosts` ile `replicas` birlikte kullanılamaz.** Adopt edilen kontratta sunucular açıkça listelenir.
- **Şablonsuz kontratta ölçekleme yok.** `size: custom` veya `image: { os: … }` olan bir kontratta ölçekleme yapılamaz, çünkü kullanılacak bir şablon yok.
- **İstisnalar süreli ve onaylıdır.** Süresi dolan bir istisna planda uyarı olarak çıkar.
- **Önce kayıtlar.** `registration: enforce` en az riskli seviye; ilk gün uygulanabilir ve envanteri hemen düzeltir.

**Uygulamada (ilk sürüm):**
- Adopt edilen kontrat `hosts: [{ name, vm: <provider>:<id>, ip }]` ile yazılır; `replicas`, `topology` ve `retire` ile birlikte kullanılamaz. `image` bir Image adıdır (OS ve bağlantı için; şablon gerekmez), `size` bir Size'dır. `image: { os }` ve `size: custom` henüz yok.
- `management` varsayılanları: `provisioning: observe`, `configuration: report`, `registration: enforce`. `provisioning: enforce` VM'lerin Pulumi'ye import edilmesini ister ve bu sürümde reddedilir; bu yüzden adopt edilen VM'ler hiçbir stack'e girmez.
- Plan listedeki her yeni sunucuyu `adopt` eder: Gaia host'u verilen adla açar, VM'i ve adresi dış kimlik olarak kaydeder, IPAM verilen adresi bu sunucuya ayırır (aralığın dışında olabilir, havuzun dışında ya da başkasının olamaz). Daha önce adopt edilenler adlarıyla `keep` edilir.
- `registration: enforce` ise kayıtlar (DNS, Zabbix, AD hesabı, Warpgate) Gaia'nın sunucularındaki gibi yazılır; sunucunun mevcut AD hesabı Gaia'nın olur. `configuration: enforce` ise yapılandırılır; `report`'ta sadece check-mode drift'i raporlanır, `observe`'ta sadece kaydedilir. Drift düzeltme Change'leri yalnızca `enforce` alanlarda açılır.
- Adopt edilen VM hiçbir zaman silinmez: listeden çıkan ya da `decommissioned` olan sunucu `unmanaged` olur ve kayıtları kalır. Adopt edilmiş sunucusu olan bir kontrat `hosts` olmadan planlanmaz.
- Gaia sunuculara kendi anahtarıyla `gaia` kullanıcısı olarak girer; adopt edilen sunucuya bu kullanıcı ve anahtar önceden konmalıdır. Host anahtarı ilk temasta kaydedilir. Aynı adla mevcut bir Zabbix host'u varsa kayıt hata verir; önce o host kaldırılmalı ya da yeniden adlandırılmalıdır.

### 8.3 Keşif

Bir Provider ve Site tanımlandıktan sonra Gaia ortamı keşfeder. Keşif her zaman salt okumadır ve erişim düzeyine göre üçe ayrılır. İlk iki düzey sadece API okur ve site eklenir eklenmez kendiliğinden çalışabilir. Üçüncü düzey sunuculara oturum açtığı için onay gerektirir.

#### Düzey 1: Hipervizörün yapısı (API)

| Ne | Kullanıldığı yer |
|---|---|
| Cluster'lar, node'lar ve host'lar | Site objesi taslağı |
| Datastore ve storage'lar: tip, kapasite, snapshot desteği | `Site.compute.storage`; snapshot desteği otomatik işaretlenir |
| Ağlar: portgroup, bridge/VLAN, SDN vnet | Network ↔ IPAM subnet eşleme önerisi |
| Şablonlar: OS, guest agent veya VMware Tools var mı | Image taslakları; guest agent'ı olmayan şablon uyarısı |
| Mevcut tag'ler, folder'lar ve pool'lar | Kontrat gruplama önerisi, label eşlemesi |

#### Düzey 2: VM'ler ve çevre sistemler (API)

- **VM'ler:** UUID, CPU/RAM, diskler, NIC'ler ve bağlı oldukları ağ, guest'in bildirdiği IP'ler ve hostname, OS bilgisi, güç durumu, snapshot'lar (sayı, yaş, boyut), tag'ler ve oluşturulma tarihi. Bunlar şunlara kaynak olur:
  - adopt taslakları (Size ve Network eşleşmesi),
  - eski snapshot, uzun süredir kapalı VM ve guest agent'ı olmayan VM raporları.
- **IP kullanımı:** Guest IP'leri DNS kayıtlarıyla karşılaştırılır. Bu karşılaştırma iki işe yarar:
  - **IPAM'ı ilk kez doldurmak.** Gaia'nın IPAM'ı boş başlayacağı için bu önemlidir.
  - **Tutarsızlıkları bulmak:** aynı IP'yi kullanan iki VM, DNS'te olup VM'de olmayan adresler, VM'de olup DNS'te olmayan adresler, eksik PTR kayıtları.
- **Zabbix:** İzlenmeyen VM'ler; karşılığı olmayan eski host'lar.
- **AD:** Uzun süredir oturum açılmamış bilgisayar hesapları. OU yapısından `TeamSource` seçim kuralı için öneri.
- **DNS:** Zone'lar ve kayıtlar.
- **Ajan konsolları:** Ajanı eksik sunucular, eski kayıtlar.
- **Warpgate:** Erişilebilir hedefler, karşılığı olmayan hedefler.

#### Düzey 3: Sunucunun içi (SSH/WinRM)

Ansible fact'leri ve check-mode ile toplanır, sunucuda hiçbir şey değiştirilmez.

| Ne | Kullanıldığı yer |
|---|---|
| OS sürümü, bekleyen güncelleme sayısı | Patch durumu raporu |
| Kurulu paketler ve versiyonları (nginx, haproxy, apache, tomcat, JDK…) | Rol önerisi, uygulama versiyon envanteri |
| Çalışan servisler ve dinleyen portlar | Rol bazlı servis ve port standardını bugünkü durumdan çıkarmak |
| Yerel kullanıcılar, sudoers, `authorized_keys` | Erişim durumu, bilinmeyen SSH anahtarları |
| sshd, NTP, auditd, rsyslog yapılandırmaları | Baseline sapma raporu (`report` modu) |
| Disktaki ve portlardaki sertifikalar | Süresi yaklaşan sertifikalar |
| Windows: domain üyeliği, feature/role'ler, kurulu ajanlar | Adopt taslağı, ajan durumu |

Örnek bir rol önerisi: "Bu 14 sunucuda nginx ve tomcat çalışıyor, 80/443/8080 dinleniyor. Bunlar `web.nginx` + `web.tomcat` ile tek bir kontrata adaylar."

Bu düzey her sunucuya oturum açtığı için bir `Operation` (`action: discover`) olarak çalışır. Git'te onaylanır, site site ve batch batch ilerler, run kaydına girer.

```yaml
apiVersion: gaia/v1alpha1
kind: Operation
metadata:
  name: discover-ist-2026-11
spec:
  action: discover
  selector: 'site == "ist"'          # keşifte bulunan, henüz Gaia'ya ait olmayan sunucular da seçilebilir
  strategy: { batchSize: 20, maxFailures: 5 }
  options: { facts: [os, packages, services, ports, users, configs, certificates] }
```

#### Çıktılar

| Çıktı | Nereye gider |
|---|---|
| Raporlar (envanter sağlığı, tutarsızlıklar, temizlik bulguları) | Veritabanı ve arayüzdeki Keşif ekranı; Git'e girmez |
| IPAM'ın ilk kez doldurulması | Arayüzde gözden geçirilir, onaylanınca IPAM'a aktarılır |
| Katalog taslakları (Site, Image, `TeamSource` seçim kuralı) | Arayüzde taslak, tamamlanınca Gaia'nın commit'i |
| Adopt taslakları (Server kontratları) | Arayüzde taslak, tamamlanınca Gaia'nın commit'i (bkz. 8.1) |

**Uygulamada (düzey 1–2, ilk sürüm):**
- Keşif raporu günde bir (ya da `gaia discover --now` / `POST /api/v1/discovery` ile hemen) drift taramasıyla aynı yerde üretilir ve `discovery_reports`'a yazılır (son on rapor kalır). `gaia discover` ve `GET /api/v1/discovery` son raporu gösterir (operator).
- Katalogdaki her Proxmox ve vSphere provider'ının VM'leri kaydedilen VM ID'leriyle eşleştirilir: `managed` (bir sunucunun), `orphan` (`gaia-uid_` tag'i veya `gaia.uid=` notu taşıyıp Gaia'da sunucusu olmayan), `unmanaged` (adopt adayı), `template`. Kaydı olup hipervizörde olmayan sunucular `missing` olarak, Zabbix'te `gaia.uid` taşıyıp sunucusu olmayan host'lar ayrıca listelenir. Okunamayan provider rapordaki hatalarda görünür, raporun geri kalanı durur.
- Henüz yok: ağ/şablon/datastore envanteri, IP–DNS karşılaştırması, AD ve Warpgate tarafı, düzey 3 (`discover` Operation'ı) ve taslak üretimi.

İlk iki düzeyin raporları sunuculara dokunmadan, sadece API erişimiyle üretilir. Bu yüzden bir ortama girildiğinde ilk hafta verilebilecek somut bir çıktıdır: envanter sağlığı raporu.

## 9. Sunucu Kimliği ve Silme

Ana kural: **bir şeyin silinmesi her zaman açıkça istenir.** Dosya silmek ya da satır kaldırmak gibi örtük işlemler kaynak silmez.

### 9.1 Sunucu kimliği: UID

Hostname'in tekrar kullanılması engellenemez. Silinen bir sunucunun adı yeniden verilebilir, Gaia dışında da aynı isimle makine açılabilir. Bu yüzden Gaia bir sunucuyu hostname ile değil, oluşturulduğu anda atanan değişmez bir **UID** ile tanır. Hostname sadece sunucunun bir özelliğidir.

- **Dış sistemlerdeki kimlikler:** Gaia'nın veritabanı her UID için bu kimlikleri tutar: vCenter instance UUID veya Proxmox SMBIOS UUID (VMID tekrar kullanılabildiği için kimlik değildir), AD objectGUID, Zabbix hostid, Warpgate hedef ID'si, ajan konsollarındaki ID'ler, IPAM kaydı. Bu kayıtlar sonradan isimle aranmaz, bu ID'lerle bulunur.
- **İşaretleme:** UID dış sistemlere de yazılır: vCenter'da `gaia.uid` custom attribute'u, Proxmox'ta tag veya VM notları, Zabbix'te `gaia.uid` tag'i, AD bilgisayar hesabında uygun bir attribute.
- **Pulumi kaynak adı UID'den türetilir** (`vm-<uid>`). Böylece aynı isimli eski sunucu henüz silinmemişken açılan yeni sunucu stack'te onunla çakışmaz.
- **Görüntüleme:** Run kayıtları, loglar ve arayüz sunucuyu hostname ve UID ile birlikte gösterir (`ankppor03 · 7f3k`). Aynı isimli farklı sunucuların geçmişi birbirine karışmaz.

### 9.2 İsim ayırma ve çakışma kontrolü

- **Sıra numarası:** En küçük boş numara verilir. Bir isim, onu taşıyan sunucu silinene kadar dolu sayılır; bekleme süresi de buna dahil. Silme tamamlanınca isim boşalır ve tekrar kullanılabilir.
- **Çakışma kontrolü:** Plandan önce yeni isim hipervizörde, AD'de (tüm DC'lerde), DNS'te, Zabbix'te, Warpgate'te ve ajan konsollarında aranır:
  - **Kayıt yoksa** isim kullanılır.
  - **Kayıt Gaia'ya ait ve UID'si eşleşiyorsa** (ör. replace) sorun yoktur.
  - **Kayıt başka birine aitse:** Gaia'nın ürettiği isimlerde bir sonraki boş numaraya geçilir ve bu planda belirtilir. Elle verilmiş isimlerde (adopt edilen `hosts` listesi) plan durur.
- **AD replikasyonu:** Silinen bilgisayar hesabı tüm DC'lerden kalkmadan aynı isimle yeni hesap açılmaz.
- **DNS:** Eski A/PTR kaydı silindikten sonra, istemci cache'leri boşalsın diye en az kaydın TTL süresi kadar beklenir.

### 9.3 Yeniden kullanılan isim

- **SSH host key:** Aynı isimle açılan yeni sunucunun host key'i farklıdır. Gaia, Ansible'ın `known_hosts` kayıtlarını ve Warpgate hedeflerinin host key'lerini UID'ye bağlı yönetir. Yeni sunucu kurulunca eski kayıt silinir, yenisi yazılır. İleride kullanıcı tarafındaki host key uyarılarını da tamamen kaldırmak istenirse OpenBao SSH CA ile host sertifikası kullanılabilir.
- **Zabbix:** Host silinince geçmişiyle birlikte silinir. Aynı isimle açılan yeni host sıfırdan başlar.
- **Ajan konsolları:** Eski kayıt, saklanan ajan ID'si üzerinden silinir; isimle aranmaz.

### 9.4 Ölçek küçültme

`replicas` azaltılırsa varsayılan olarak sıra numarası en yüksek sunucu gider. Belirli bir sunucunun çıkarılması isteniyorsa `retire` alanı kullanılır:

```yaml
spec:
  replicas: 2
  retire: [ankppor01]          # en yüksek sıra numarası yerine bu sunucu gider
```

```
$ gaia plan portal-web.yaml
  ankppor01 · 2c9a   - decommission     drain → snapshot → kapat → 7 gün bekle → sil
  IPAM               10.20.120.31 karantinaya (14 gün)
env=prod → onay gerekli (owner + platform-admin)
```

Silme tamamlandıktan sonra `retire` satırının bir etkisi kalmaz; Gaia bunu bir commit ile temizler. Bir sunucuyu `retire` listesine eklemeden sadece `Operation` ile silmek mümkün değildir. Aksi halde kontrat hâlâ aynı sayıda sunucu istediği için bir sonraki apply silinen sunucunun yerine yenisini kurar.

### 9.5 Değiştirme (replace)

Bozuk bir sunucunun yerine aynı isim ve aynı IP ile yenisi kurulur. Yeni sunucu yeni bir UID alır.

```yaml
apiVersion: gaia/v1alpha1
kind: Operation
metadata:
  name: replace-ankppor02
spec:
  action: replace
  target: { contract: portal-web, host: ankppor02 }
```

Replace işleminde bekleme süresi uygulanmaz, çünkü isim ve IP yeni sunucuya geçer.

**Uygulamada:**
- Operation `action: replace` ile `selector` ve değiştirilecek sunucuların adlarını veren `hosts` alanı kullanılır (`hosts` diğer aksiyonlarda da seçilen kontratların sunucularını daraltır; replace'te zorunludur).
- Kontrat son uygulandığı gibi olmalıdır: uygulanmamış bir değişikliği varsa replace reddedilir, çünkü replace başka bir şey uygulamaz.
- Tek run'da: eski sunucunun kayıtları (DNS, AD hesabı, Warpgate hedefi, Zabbix host'u) kaydedilen ID'leriyle silinir; yeni UID eski hostun yerini, adını ve sıra numarasını aynı anda alır; IPAM adresi yeni UID'ye devreder (`reassign`). Eski VM, yeni VM ağa çıkmadan önce stack'ten düşürülür ve hipervizör API'siyle hemen durdurulup silinir (çoğu zaman sunucu asılı kaldığı için değiştirilir). Sonra yeni VM kurulur, kayıtları yazılır ve sadece yeni sunucu yapılandırılır.
- Eski VM aynı run'da silindiği için son snapshot alınmaz; geri dönüş noktası gerekiyorsa replace'ten önce `snapshot` Operation'ı çalıştırılır.

### 9.6 Kontratın kaldırılması

```yaml
spec:
  lifecycle:
    state: active              # active | decommissioned
    deletionPolicy: delete     # delete | retain
```

- **Kontrat dosyasını silmek kaynakları silmez.** Yaşayan kaynağı olan bir kontratın dosyası silinirse CI bunu reddeder.
- **Silme iki adımda yapılır:**
  1. Kontratta `lifecycle.state: decommissioned` yapılır ve commit izlenen branch'e girer. Change onaylanınca sunucular decommission akışına girer.
  2. Akış tamamlanınca Gaia, kontrat dosyasını silen commit'i yazar.
- **Uygulamada (adım 2):** Kaynak senkronunun her turunda Gaia, `decommissioned` olan, uygulanmış (`synced`) ve hiç sunucusu kalmamış (bekleyenler dahil) kontratların dosyalarını tek bir imzalı commit'le siler. Dosyada başka objeler de varsa silmez ve loglar. Dizin, son dosyası silinse de boş bir `.gaia-keep` ile Git'te kalır: izlenen bir yolun kaybolması kataloğun yüklenmesini durdururdu (Operation temizliğinde de aynı).
- **`deletionPolicy: retain`:** Kaynaklar silinmez, sadece Gaia'nın yönetiminden çıkarılır. Pulumi state'inden düşürülür ve envanterde "yönetilmiyor" olarak işaretlenir. Bir sistemi başka bir ekibe veya araca devrederken kullanılır.

### 9.7 Adopt edilmiş sunucular

- Gaia kendi kurmadığı bir VM'i, `provisioning: enforce` seviyesine gelmeden silemez. `observe` veya `report` seviyesindeyken `decommissioned` istenirse plan reddedilir; bu durumda kullanılabilecek tek seçenek `retain`.
- Adopt edilmiş kontratlarda `deletionPolicy` varsayılanı `retain`.

### 9.8 Decommission akışı

Bu akış provisioning'in tersidir, ama arada bir bekleme süresi vardır. Bekleme süresince geri dönmek için sunucuyu açıp devre dışı bırakılan kayıtları yeniden etkinleştirmek yeterlidir.

| Aşama | Ne yapılır | Geri alınabilir mi |
|---|---|---|
| 1. Drain | Sunucu load balancer'dan çıkarılır, bağlantıların bitmesi beklenir | Evet |
| 2. Bakım modu | Zabbix'te bakıma alınır, alarmlar susturulur | Evet |
| 3. Son snapshot | Policy'ye göre alınır ve saklanır | – |
| 4. Kapatma | Graceful shutdown. Zabbix host'u, Warpgate hedefi ve AD bilgisayar hesabı **devre dışı** bırakılır | Evet: aç ve etkinleştir |
| 5. Bekleme | Prod'da 7 gün, test'te 1 gün; arayüzde "geri al" | Evet |
| 6. Silme | VM (Pulumi destroy), AD hesabı, DNS kayıtları, Zabbix host'u (geçmişiyle birlikte), Warpgate hedefi ve ajan konsollarındaki kayıtlar silinir | Hayır |
| 7. Karantina | IP karantinaya alınır. İsim 9.2'deki kurallara göre boşalır | – |

- **Aşama 4'te sunucuya müdahale edilmez.** Domain'den çıkarma veya ajan kaldırma gibi işler yapılmaz, kayıtlar dışarıdan devre dışı bırakılır. Bu sayede geri dönüşte sunucuyu yeniden yapılandırmak gerekmez. Kalıcı temizlik aşama 6'da, sunucuya bağlanmadan, doğrudan API'lerle yapılır.
- **Kesintiler:** Her adım idempotent ve run devam ettirilebilir. Akış yarıda kalırsa sunucu `decommissioning` durumunda görünür ve sonraki apply kaldığı yerden devam eder.

**Uygulamada:**
- Policy `decommission: { gracePeriod, finalSnapshot: { enabled }, ipQuarantine }`; ortamlar (`environments.<env>.decommission`) alan alan üzerine yazar. Bekleme süresi yoksa sunucu aynı run'da silinir.
- **Durdurma (aynı run):** izleme kapatılır (Zabbix host'u devre dışı; geçmiş kalır), Policy istiyorsa son snapshot alınır, sunucu graceful kapatılır, AD bilgisayar hesabı devre dışı bırakılır, Warpgate hedeflerinin rolleri alınır. Host `stopped` olur ve `delete_after` yazılır. Ayrı bir bakım penceresi açılmaz: devre dışı host alarm üretmez.
- **Bekleme:** VM, son başarılı run'ın uyguladığı tanımla (`hosts.vm_spec`) stack'te kalır, kapalı olarak (`started` ve `on_boot` kapalı). Böylece Pulumi ona dokunmaz ve geri dönüşte VM aynen kalır.
- **Geri alma:** Sunucuyu silen commit geri alınırsa (ya da `replicas` eski haline gelirse) plan onu `keep` eder; run sunucuyu açar, SSH'ın gelmesini bekler, izlemeyi ve hesabı açar. Warpgate rolleri erişim aşamasında geri gelir.
- **Silme:** Her 15 dakikada bir, süresi dolan sunucusu olan kontratlar için bir "decommission run"ı kuyruğa girer (`requestedBy: gaia: decommission`). Run kontratın son uygulanan planını kullanır, onaylanmış Change'in devamıdır ve kontratın diğer sunucularına dokunmaz: kayıtlar silinir, VM Pulumi state'inden düşürülür (Pulumi çalıştırılmaz) ve hipervizörün API'siyle silinir (Proxmox: durdur, `purge`; vSphere: kapat, Destroy), adres Policy'nin `ipQuarantine`'iyle karantinaya girer. Run bitince kontratın uygulanan planı güncellenir, yeni Change açılmaz.
- Son snapshot VM'le birlikte silinir: bekleme süresince geri dönüş noktasıdır, silmeden sonra saklanmaz. Silme sonrası saklama yedekleme ister.
- `deletionPolicy: retain`: VM'ler stack çalışmadan önce state'ten düşürülür, hiçbir kayıt silinmez. Host `unmanaged` olur; adını ve adresini tutar, böylece Gaia ikisini başka sunucuya vermez.
- vSphere'de kapalı VM'in Pulumi tarafından açılmadığı gerçek vCenter'da doğrulanmalıdır (vcsim ile test edilemiyor).

### 9.9 Prod koruması ve Policy

Prod'da silme iki onay gerektirir: kontratın owner'ı ve `platform-admin`. Bu, Change'in onay gereksinimi olarak zorunlu tutulur (bkz. 7.6). Onaylanan plan ile uygulanan planın hash'i aynı olmalıdır.

```yaml
apiVersion: gaia/v1alpha1
kind: Policy
metadata:
  name: default
spec:
  decommission:
    gracePeriod: 7d
    finalSnapshot: { enabled: true, retention: 30d }
    ipQuarantine: 14d
  environments:
    prod: { deleteProtection: true, decommissionApprovers: [owner, platform-admin] }
    test: { decommission: { gracePeriod: 1d, finalSnapshot: { enabled: false } } }
```

### 9.10 Katalog objelerinin silinmesi

- **Kullanımda olan bir katalog objesi silinemez.** Silinirse yükleme sırasında bir referans çözülemez hale gelir ve CI etkilenen kontratları listeler.
- **Üretilen objeler:** IPAM'dan bir subnet kalkarsa Gaia ilgili `Network` objesini silen commit'i yazar. Ağ kullanımdaysa aynı kural nedeniyle bu commit yazılmaz; sorun sync raporunda görünür.
- **Kullanımdan kaldırma:** Objeye `gaia/deprecated: "true"` annotation'ı eklenir. Yeni kontratlar bu objeyi seçemez, mevcut kontratların planında uyarı çıkar.

## 10. Audit

İş Git'e dayandığı için iki katman yeterli bulundu.

### 10.1 Katman 1: Değiştirilemez run kaydı

```yaml
run: run-20261104-0042
requestedBy: mehmet.y                    # commit sahibi (Gaia'da kayıtlı imza anahtarından)
approvedBy: [ayse.k]                     # Gaia'daki onaylar, plan hash'iyle
source: { repo: platform/contracts, commit: 3f9c2e1, signed: true }
catalogCommit: 9a1b0c4
configCommit: 51e07aa                    # Ansible reposu
executionEnvironment: registry.corp.local/gaia/ee-default@sha256:4c1e…
contract: portal-web
planHash: sha256:ab12…                   # onaylanan plan ile uygulanan plan aynı mı
accessSnapshot: sha256:77d0…             # erişim dağıtılırken kullanılan üye listesi (bkz. 14.2)
executedAs: { vc-ank: svc-gaia-vm, corp-ad: svc-gaia-ad, linux: gaia }
startedAt: 2026-11-04T14:02:11Z
finishedAt: 2026-11-04T14:19:40Z
result: failed
changes: [...]
```

- **Commit imzası zorunlu, imzalayan Gaia'da kayıtlı.** "Kim istedi" sorusunun kanıtı imzalı commit'tir; "kim onayladı" sorusununki Gaia'daki onay kaydıdır (bkz. 7.6).
- **Kayıtlar hash zinciriyle bağlıdır** ve anında SIEM'e gönderilir. Silinen veya değiştirilen bir kayıt zinciri kırar.
- **Arşiv:** Kayıtların bir kopyası SeaweedFS'te, S3 Object Lock'lu bir bucket'ta tutulur.
  - SeaweedFS S3 Object Lock'u destekler: GOVERNANCE ve COMPLIANCE modları, legal hold. Bu desteğin bulunduğu bir sürüm kullanılmalı; özellik 2025 ortasında eklendi.
  - Run kayıtları için **COMPLIANCE** modu kullanılır. Bu modda kayıt, admin dahil kimse tarafından saklama süresi bitene kadar silinemez veya değiştirilemez. GOVERNANCE modunda ise bypass yetkisi olan admin silebilir.
  - Bucket versioning açık olarak ve Object Lock'u baştan etkin şekilde oluşturulmalıdır; mevcut bir bucket'a sonradan eklenemez.
  - Saklama süresi `runArchive`'ın gösterdiği provider'da `retention` ile ayarlanır (`30d`, `10y`); en fazla 100 yıl olabilir. COMPLIANCE modunda süre sonradan kısaltılamaz.
  - Ham Ansible ve Pulumi logları kanıt niteliği taşımadığı için Object Lock'suz ayrı bir bucket'ta durur.
  - Object Lock yazılım seviyesinde bir korumadır. Depolama sunucularına doğrudan erişimi olan biri veriyi yine de silebilir. Bu yüzden SIEM'e gönderilen kopya ikinci bir güvencedir.

### 10.2 Katman 4: Servis hesaplarını insanlar kullanamasın

Bir servis hesabıyla yapılmış işlem, ancak o hesabı sadece Gaia'nın kullanabildiği kanıtlanabiliyorsa kanıt değeri taşır.

- Credential'lar sadece OpenBao'da durur ve onlara sadece Gaia'nın policy'si erişebilir.
- AD'de bu hesaplara interaktif oturum açma GPO ile yasaklanır.
- Hesap Gaia'nın IP'si dışından kullanılırsa Zabbix/SIEM'de alarm üretilir.

## 11. Arayüz ve CLI

### 11.1 Prensip

Arayüz ve CLI aynı API'nin istemcileridir; ikisinin yetkisi aynıdır. İnsanlar okur, plan alır ve Change'leri onaylar veya reddeder; istenen durumu değiştiremez ve uygulamayı kendileri başlatamaz. Uygulamayı Gaia, gereksinimler sağlanınca kendisi başlatır (bkz. 7.6). `operator` rolü ayrıca çalışan bir run'ı durdurabilir veya duraklatabilir (bkz. 6.4).

Arayüzün amacı değişiklikleri incelemek ve onaylamak, diyagramları görmek, logları takip etmek ve hataları debug etmektir.

### 11.2 Kimlik doğrulama

```yaml
apiVersion: gaia/v1alpha1
kind: AuthConfig
metadata:
  name: default
spec:
  url: https://gaia.corp.local        # passkey'ler bu adresin host'una bağlı
  require: passkey                    # mfa | passkey
  providers:
    - name: corp-sso
      type: oidc
      issuer: https://sso.corp.local/realms/corp
      clientId: gaia
      clientSecret: bao://kv/platform/gaia/oidc
      mfa:
        trustIdp: true
        acceptAmr: [hwk, mfa]
        fallback: local-passkey
    - name: local
      type: local
      purpose: break-glass
  sessions:
    ui:  { ttl: 8h, idle: 30m }
    cli: { accessTtl: 1h, refreshTtl: 12h }
```

**OIDC.** Kullanıcının OIDC ile giriş yapmış olması MFA yaptığını göstermez. Gaia bunu ID token'daki `amr`/`acr` claim'lerinden doğrular. Kanıt yoksa kullanıcıdan Gaia'nın kendi passkey'i istenir. Böylece politika IdP'nin yapılandırmasından bağımsız olarak aynı uygulanır.

**Local hesaplar.** Öncelikli kullanım amacı break-glass, yani OIDC çalışmadığında erişim.
- `require: passkey` ise sadece passkey ile giriş yapılır (parolasız, user verification zorunlu).
- `require: mfa` ise giriş parola + TOTP veya passkey ile yapılır.
- Kurtarma kodları ilk kayıtta bir kez gösterilir.

### 11.3 CLI login

```
$ gaia login                      # loopback redirect + PKCE (RFC 8252)
$ gaia login --device             # device authorization grant (RFC 8628), tarayıcısız makineler için
```

- **Login sonrası:** CLI tarayıcıyı açar, kullanıcı giriş yapar ve CLI **süreli bir token** alır.
- **Device akışı:** Phishing'e açık olduğu için onay ekranında isteğin geldiği makine ve IP gösterilir, kullanıcı açıkça onaylar.
- **Token'lar:** Gaia'nın kendi opak token'larıdır, IdP token'ı CLI'a verilmez. Sunucu sadece hash'lerini tutar ve token'lar anında iptal edilebilir. CLI tarafında OS keychain'de saklanır.
- **Oturumlar:** "Oturumlarım" ekranında aktif UI ve CLI oturumları görülüp tek tek kapatılabilir.

### 11.4 Yetkiler ve görünürlük

Roller Team üyeliklerinden türetilir (AD senkronu, bkz. 14). OIDC sadece kimlik doğrulama için kullanılır. Hangi takımın `operator` veya `platform-admin` olacağı Policy'de tanımlıdır:

```yaml
kind: Policy
spec:
  gaiaRoles:
    operator: [team-ops]
    platform-admin: [team-platform]
```

| Rol | Görebildikleri |
|---|---|
| `viewer` | Kendi takımının kontratları (`labels.owner`) |
| `operator` | Tüm kontratlar, loglar, `plan`; çalışan run'ı durdurma ve duraklatma |
| `platform-admin` | Ek olarak provider sağlığı, oturum yönetimi, kullanıcılar |

**Görünürlük kuralı:** Bir kullanıcının yetkisi olmayan bir kontrat, ilişki diyagramlarında veya "kullanan kontratlar" listelerinde (ör. aynı ağı kullananlar) **adıyla görünür, detayı kapalıdır.** YAML'ı, sunucuları, logları ve run kayıtları açılmaz.

### 11.5 Ekranlar

1. **Genel bakış.** Onay bekleyen Change'ler, son run'lar, başarısız olanlar, drift sayıları ve süresi dolmak üzere olan istisnalar.
2. **Değişiklikler.** Açık Change'ler ve gereksinimleri, "onayımı bekleyenler", YAML ve plan diff'i, onay ve red; commit'e göre gruplu görünüm (bkz. 7.6).
3. **Kontratlar.** Sync ve sağlık durumu (uygulanan ve son commit), YAML ve çözümlenmiş hali, ilişki diyagramı (kontrat → katalog objeleri → provider'lar → gerçek kaynaklar), sunucular, yönetim seviyeleri ve drift (sunucu ve alan bazında diff, olası kaynak, düzeltme Change'i).
4. **Run'lar.** Aşama zaman çizelgesi, host × task matrisi, canlı log, Pulumi diff ve run kaydı. Hata ilgili provider'a ve onun credential referansına bağlanır.
5. **Katalog.** Kind'a göre gezinme ve label filtreleri. Her obje için nereden üretildiği, patch'leri ve onu kullanan kontratlar.
6. **Provider'lar.** Bağlantı durumu, son başarılı çağrı, credential'ın geçerlilik durumu.
7. **Sync.** Source'ların son sync zamanı, CEL hataları ve Gaia'nın yazdığı son commit'ler.
8. **Keşif.** Site bazında keşif raporları (envanter sağlığı, tutarsızlıklar, temizlik bulguları), IPAM'ın ilk doldurulması için gözden geçirme ekranı, adopt ve katalog taslakları (bkz. 8.3).
9. **Profil.** İmza anahtarları, oturumlar ve bildirim abonelikleri.

### 11.6 Loglar

- **Ansible:** ansible-runner'ın JSON olay akışı kullanılır (`ansible-runner run -j`); ayrı bir callback plugin'i gerekmez. Olaylar UID ile gelir ve host × task matrisi bu olaylardan oluşur (bkz. 7.4).
- **Pulumi:** Automation API'nin event stream'i kullanılır.
- **Maskeleme:** Run sırasında OpenBao'dan alınan her değer loglarda maskelenir. Ansible'daki `no_log` ayarına güvenilmez.
- **Saklama:** Canlı loglar SSE ile akar, run bitince SeaweedFS'e arşivlenir.

### 11.7 Teknoloji

- **Backend:** Go. API, SSE ve statik dosyalar tek binary'de; frontend `go:embed` ile içine gömülür.
- **Frontend:** React + TypeScript. Diyagramlar için React Flow ve otomatik yerleşim için ELK.js.
- **Kütüphaneler:** `go-webauthn/webauthn` (passkey), `coreos/go-oidc` (OIDC), `google/cel-go` (CEL), Pulumi Automation API (Go).

## 12. Veritabanı

İlke: **istenen durum Git'te, işletme durumu veritabanında.** Kontratlar, katalog ve policy'ler veritabanına girmez. Veritabanında sadece Git'te tutulamayan veriler durur.

### 12.1 Ne tutulur

| Alan | İçerik | Kaybolursa |
|---|---|---|
| Sunucu kimlikleri | UID, hostname, durum, dış sistemlerdeki ID'ler | `gaia reconcile` ile dış sistemlerdeki `gaia.uid` işaretlerinden yeniden kurulur |
| İsim kiralamaları | Hangi ismi hangi UID taşıyor, isim ne zaman boşaldı | Sunucu kimliklerinden türetilir |
| Run'lar | Run kaydı (hash zinciri), aşamalar, yapısal olaylar | Kayıtların kopyaları SIEM'de ve SeaweedFS'te; asıl kaynak veritabanı |
| Planlar | Onaylanan planın içeriği ve hash'i | Plan yeniden alınır |
| İş kuyruğu | Bekleyen apply'lar, sync işleri, drift taramaları, bekleme süresi dolunca çalışacak silme aşaması | Kritik: bekleme süresi zamanlayıcıları burada |
| Sync durumu | Source'ların son revizyonu, Gaia'nın yazdığı commit'ler, CEL hataları | Sonraki sync'te yeniden oluşur |
| Keşif ve drift | Adopt raporları, drift sonuçları | Yeniden taranır |
| Kimlik doğrulama | Local kullanıcılar, passkey'ler, TOTP, kurtarma kodları, oturumlar, device kodları | Kullanıcılar yeniden kayıt olur, oturumlar düşer |
| Katalog indeksi | Yüklenen commit'in objeleri ve ters referansları | Git'ten yeniden üretilir (cache) |

IPAM bu veritabanında değildir. IPAM ayrı bir servis ve ayrı bir veritabanıdır; Gaia ona diğer sistemler gibi bir provider üzerinden bağlanır. Böylece provider modeli korunur ve gerekirse NetBox gibi bir alternatife geçmek için sadece provider değişir. Aynı PostgreSQL cluster'ında ayrı bir veritabanı olarak durabilir. Zabbix'in veritabanı ise yüksek yazma yükü nedeniyle ayrı bir cluster'da olmalıdır.

Pulumi state'i de aynı cluster'da ayrı bir veritabanında durur (Pulumi'nin `postgres://` DIY backend'i). State'teki secret'lar OpenBao transit anahtarıyla şifrelidir (`hashivault://pulumi`). Stack ayarları (secrets provider ve şifreli anahtar) Gaia'nın veritabanında `pulumi_stacks` tablosunda tutulur, çünkü Automation API her run'da geçici bir çalışma dizini açar. SeaweedFS state için kullanılmadı: boşalan dizinleri listede 0 baytlık obje olarak döndürüyor ve bu, Pulumi'nin kilit kontrolünü bozuyor.

### 12.2 PostgreSQL ve Gaia'nın gereksinimleri

Veritabanı PostgreSQL'dir. PostgreSQL'in nasıl kurulacağı ve işletileceği (HA, replikasyon, yedek, izleme) **veritabanı yöneticisinin kararıdır**; Gaia buna bağımlı değildir. Gaia'nın beklediği şeyler şunlar:

- **Bağlantı:** Bir DSN, `bao://kv/platform/gaia/db` referansıyla OpenBao'dan okunur. TLS zorunludur (`sslmode=verify-full`).
- **Sürüm:** Minimum **PostgreSQL 16**. Sebebi:
  - pgx, PostgreSQL ekibinin desteklediği sürümleri destekler (şu an 14 ve üstü).
  - River ise en güncel üç ana sürümle test edilir. Daha dar olan sınır River'ınki; şu an 16, 17 ve 18.
  - Kural: Gaia'nın minimum sürümü, River'ın test ettiği en eski ana sürümdür. Yeni bir ana sürüm (ör. 19) çıktığında minimum bir sürüm yükselir; bu sürüm notlarında duyurulur.
  - Yeni kurulumlarda en güncel ana sürüm önerilir.
- **Eklenti veya superuser gerekmez.** Bu sayede managed PostgreSQL servislerinde de çalışır.
- **İki rol:**
  - `gaia_owner`: Şemanın sahibi. Migration'ları sadece bu rol çalıştırır (`gaia migrate`).
  - `gaia_app`: Uygulamanın çalışırken kullandığı rol. Sadece DML yapabilir; `run_records` tablosunda `UPDATE` ve `DELETE` yetkisi yoktur.
- **Bağlantı havuzu:** PgBouncer transaction modunda `LISTEN/NOTIFY` çalışmaz. Önünde böyle bir havuz varsa Gaia'ya canlı bildirimler için ayrı, doğrudan bir DSN verilmelidir (`listenDsn`). Kilitler transaction seviyesinde advisory lock ile alındığı için transaction modundaki havuzdan etkilenmez.
- **Partition yönetimi Gaia'dadır.** Gelecek ayların partition'larını Gaia önceden açar, saklama süresi dolanları kendisi düşürür. `pg_partman` gibi bir eklenti gerekmez.
- **Failover:** Gaia bağlantı kopmalarına dayanıklıdır. Bağlantıyı yeniden kurar ve yarım kalan işleri kuyruktan devam ettirir.
- **Yedek:** Yedek yöneticinin sorumluluğundadır. Önerimiz point-in-time recovery destekli bir yedek, örneğin pgBackRest ile SeaweedFS'e. Yedekten dönülemezse son çare `gaia reconcile`.

### 12.3 Şema taslağı

```sql
-- Sunucu kimliği
hosts (
  uid           text primary key,
  contract      text not null,
  hostname      text not null,
  seq           int,
  site          text,
  status        host_status not null,   -- provisioning | active | draining | stopped | decommissioning | deleted
  created_run   text references runs,
  deleted_at    timestamptz
)

host_external_ids (
  uid           text references hosts,
  provider      text,                   -- vc-ank, zabbix, corp-ad …
  kind          text,                   -- vm, host, computer, target, agent:bigfix …
  external_id   text,
  unique (provider, kind, external_id)
)

name_leases (
  name          text,
  uid           text references hosts,
  held_since    timestamptz,
  released_at   timestamptz
)
create unique index on name_leases (name) where released_at is null;   -- bir isim aynı anda tek UID'de

-- Run'lar
runs          (id, contract, commit, catalog_commit, plan_hash, status, started_at, finished_at)
run_stages    (run_id, stage, status, started_at, finished_at)
run_events    (run_id, seq, ts, stage, host_uid, task, status, data jsonb)   -- aylık partition, 90 gün
run_records   (run_id, body jsonb, prev_hash, record_hash)                    -- sadece insert
plans         (hash, contract, commit, body bytea, created_at)
operations   (name, commit, action, status, plan_hash, targets jsonb, window_start, window_end, not_after, cancelled_by, created_at, finished_at)
operation_schedule_instances (schedule, window_start, operation_name, commit)

-- Değişiklikler (bkz. 7.6)
repos         (provider, ref, last_seen_commit, applied_commit, fetched_at)
changes       (id, contract, source, from_commit, to_commit, plan_hash, status, created_at, decided_at, applied_run)
change_gates  (change_id, gate, status, detail jsonb)
approvals     (change_id, plan_hash, user_id, decision, comment, webauthn_assertion, created_at)   -- sadece insert
signing_keys  (user_id, kind, fingerprint, public_key, created_at, revoked_at)
notifications (id, trigger, change_id, channel, status, sent_at)

-- Sync, keşif, drift
sync_runs     (source, revision, status, commit, errors jsonb, finished_at)
discoveries   (id, started_at, findings jsonb)
drift_reports (contract, host_uid, area, key, status, findings jsonb, first_seen, last_seen, resolved_at)   -- bkz. 7.5

-- Kimlik doğrulama
users                (id, username, ad_guid, display_name, provider, enabled, synced_at, created_at, disabled_at)
team_memberships     (team, user_id, via_group, synced_at)      -- AD'den senkron (bkz. 14.2)
webauthn_credentials (user_id, credential_id, public_key, sign_count, created_at)
totp_secrets         (user_id, ciphertext)     -- OpenBao transit ile şifreli
recovery_codes       (user_id, code_hash, used_at)
sessions             (token_hash, user_id, kind, expires_at, last_seen, ip, user_agent)
device_codes         (code_hash, user_code, status, expires_at)
```

### 12.4 Kurallar

- **Run kaydı değiştirilemez.** Değiştirilemez kayıt, run bittiğinde `run_records` tablosuna tek seferde yazılır. `UPDATE` ve `DELETE` hem rol yetkisiyle hem bir trigger ile engellenir. Her kayıt bir öncekinin hash'ini taşır. Run sürerken değişen durum bilgisi `runs` ve `run_stages` tablolarındadır.
- **Loglar ikiye ayrılır.** Arayüzde sorgulanan yapısal olaylar `run_events` tablosunda 90 gün tutulur. Ham Ansible ve Pulumi çıktısı SeaweedFS'te süresiz arşivlenir.
- **Kilit:** Aynı kontrat için aynı anda tek run çalışır. Bunu kontrat başına alınan advisory lock sağlar.
- **İş kuyruğu:** River (`SKIP LOCKED`) kullanılır. Hangi Gaia replikası boştaysa işi o alır; leader election gerekmez.
- **Canlı log:** Replikalar arasında `LISTEN/NOTIFY` ile dağıtılır. Kullanıcı hangi replikaya bağlıysa logu canlı görür.
- **Veritabanında oturum açmaya yarayacak bilgi tutulmaz.** TOTP secret'ları OpenBao transit ile şifrelidir. Token'ların ve kurtarma kodlarının sadece hash'i tutulur. Passkey'lerin sadece public key'i tutulur.
- **`gaia reconcile`:** Hipervizörleri, Zabbix'i ve AD'yi tarar ve UID'leri `gaia.uid` işaretlerinden bularak `hosts` ile `host_external_ids` tablolarını yeniden kurar.

### 12.5 Go tarafı

`pgx` (sürücü), `sqlc` (tip güvenli sorgular), `goose` (migration), `river` (iş kuyruğu).

## 13. Dağıtım

Birincil dağıtım hedefi **VM**. Kubernetes için bir **Helm chart** de hazırlanır. İki yöntemde de aynı binary ve aynı konfigürasyon kullanılır.

### 13.1 Bileşenler

Gaia tek bir binary'dir:

- `gaia server` her şeyi birlikte çalıştırır: API, arayüz, worker'lar, sync ve zamanlayıcı.
- Ölçeklemek gerekirse roller ayrılabilir: `gaia server --role=api` ve `gaia server --role=worker`. Ayrılmış ağ segmentleri için `--site` ile site worker'ı çalıştırılabilir (bkz. 7.4).
- Replikalar stateless'tır; tüm durum veritabanında ve SeaweedFS'te durur. Bu yüzden önlerine bir load balancer konarak istenen sayıda replika çalıştırılabilir.

### 13.2 VM

- **Paketleme:** `nfpm` ile `.deb` ve `.rpm` paketleri üretilir ve Pulp'taki apt/dnf reposuna imzalanarak konur.
- **Servis:** systemd unit'i ile çalışır. Konfigürasyon `/etc/gaia/` altında durur; servis ayrı bir sistem kullanıcısıyla, systemd'nin hardening seçenekleri açık şekilde çalışır.
- **OpenBao kimliği:** AppRole. `secret_id` OpenBao Agent ile ya da systemd credentials üzerinden verilir.
- **Migration:** Güncelleme sırasında `gaia migrate`, `gaia_owner` rolüyle ayrı bir adım olarak çalıştırılır.
- **Ansible çalıştırma:** Worker sunucularında EE container'ları için `podman` kurulu olmalıdır. EE imajları Zot'tan digest'le çekilir.
- **Kurulumun kendisi:** Gaia'yı VM'e kuran bir Ansible rolü (`gaia.server`) hazırlanır. Gaia'yı da aynı araçlarla kurmuş oluruz.

**Uygulamada:**
- `make package`, linux/amd64 ve linux/arm64 için `dist/`'e `.deb` ve `.rpm` üretir (nfpm). Paket `gaia` ve `gaia-ipam` binary'lerini, `gaia.service` ve `gaia-ipam.service` unit'lerini, `/usr/lib/sysusers.d/gaia.conf`'u ve `/etc/gaia/gaia.env`, `/etc/gaia/gaia-ipam.env` örneklerini (config, güncellemede korunur) taşır. Etiket yoksa sürüm `0.0.0~<commit>` olur.
- Kurulum `gaia` sistem hesabını açar, rootless podman için ona alt UID/GID aralığı verir ve `/etc/gaia`'yı `root:gaia 0750` yapar. Servisler kendiliğinden başlamaz: önce yapılandırma yazılır, `gaia migrate` (`gaia_owner` ile) çalıştırılır.
- `gaia.service`: `EnvironmentFile=/etc/gaia/gaia.env`, `ExecStart=/usr/bin/gaia server $GAIA_SERVER_ARGS`; durum `/var/lib/gaia` (StateDirectory), Pulumi CLI'ı `/var/cache/gaia`, plugin'ler `PULUMI_HOME=/var/lib/gaia/pulumi`. Hardening açıktır (`ProtectSystem=strict`, sadece bu dizinlere yazma, kernel ve cgroup koruması vb.); rootless podman setuid `newuidmap` istediği için `NoNewPrivileges` ve system call filtresi kapalıdır. Sadece API çalıştıran bir sunucu bunları bir drop-in ile açabilir.
- `gaia-ipam.service` tam hardening'le çalışır (`systemd-analyze security`: 2.1).
- AppRole `secret_id`'si `GAIA_BAO_SECRET_ID_FILE` ile bir dosyadan ya da systemd credential'ı (`LoadCredential=bao-secret-id:...`, `CREDENTIALS_DIRECTORY`) olarak verilir; env dosyasında düz metin secret yoktur.
- `gaia pulumi prepare` sabitlenmiş Pulumi CLI'ını ve provider plugin'lerini indirir; internete çıkamayan sunucularda ya da imaj üretiminde run'ların indirme yapmaması için `gaia` kullanıcısıyla (aynı `HOME`, `XDG_CACHE_HOME`, `PULUMI_HOME` ile) bir kez çalıştırılır.
- `gaia.server` rolü (`deploy/ansible/roles/gaia.server`) paketi (dosyadan ya da repodan), `/etc/gaia`'yı, secret dosyalarını, migration'ı, `gaia pulumi prepare`'i ve servisleri kurar; Rocky 9'da iki kez çalıştırıldı, ikincisi bir şey değiştirmedi.
- Rocky 9'da (systemd'li container) kuruldu: servisler hardening altında gerçek PostgreSQL'le çalıştı, `gaia pulumi prepare` servis kullanıcısıyla çalıştı. Debian 12'de kurulum ve kaldırma denendi.

### 13.3 Kubernetes (Helm)

- **Chart içeriği:** Deployment, Service, Ingress, ConfigMap, ServiceAccount ve PodDisruptionBudget. HorizontalPodAutoscaler isteğe bağlı.
- **Migration:** `pre-install` / `pre-upgrade` hook'u olarak çalışan bir Job.
- **OpenBao kimliği:** Kubernetes auth method. Secret'lar OpenBao Agent Injector veya CSI provider ile pod'a gelir; chart Kubernetes Secret'ına düz metin yazmaz.
- **Rol ayrımı:** `values.yaml` ile API ve worker ayrı Deployment olarak açılabilir.
- **Ansible çalıştırma:** Her Ansible run'ı ayrı bir pod olarak çalışır. Worker'ın ServiceAccount'ı sadece kendi namespace'inde pod oluşturup silebilir.
- **Veritabanı chart'ın içinde yoktur.** PostgreSQL dışarıda durur ve DSN referansıyla verilir.
- **Dağıtım:** Container imajı ve chart Zot'ta tutulur; chart bir OCI artifact'ıdır (`helm install oci://…`).

**Uygulamada:**
- `make image` imajı üretir: distroless (`base-debian12:nonroot`), `gaia` ve `gaia-ipam`, önceden indirilmiş Pulumi CLI'ı ve plugin'leri (`gaia pulumi prepare`; run'lar bir şey indirmez). Varsayılan komut `gaia server --listen 0.0.0.0:8080 --data-dir /gaia/data`.
- Chart `deploy/helm/gaia`: tek Deployment (`--role=all`) ya da `split: true` ile API ve worker ayrı; Service, isteğe bağlı Ingress ve HPA, PDB, ServiceAccount, Ansible pod'ları için namespace'e sınırlı Role, `pre-install`/`pre-upgrade` migration Job'ı. Pod'lar root olmadan, salt okunur kök dosya sistemiyle, bütün capability'ler düşürülmüş çalışır; repo aynaları `emptyDir`'dedir.
- DSN chart'a yazılmaz: başka bir aracın yönettiği bir Secret'ın anahtarından (`database.existingSecret`) ya da OpenBao Agent'ın/CSI'ın yazdığı bir dosyadan (`database.file`, `GAIA_DSN_FILE`) gelir.
- OpenBao'ya Kubernetes auth ile girilir: `GAIA_BAO_K8S_ROLE` (mount `GAIA_BAO_K8S_MOUNT`, varsayılan `kubernetes`), pod'un service account token'ıyla; token her girişte dosyadan okunur, oturum AppRole'deki gibi yenilenir.
- kind'da denendi: migration hook'u şemayı kurdu, API ve worker ayrı Deployment'larla ayağa kalktı, worker OpenBao'ya Kubernetes auth ile girdi.
- **Ansible pod'ları:** Chart'taki worker'lar `--ansible-executor pod` ile çalışır: her Ansible run'ı release'in namespace'inde ayrı bir pod'dur (EE imajı, `ansible-runner`, bellekte bir `emptyDir`). Run'ın veri dizini (proje, envanter, SSH anahtarı) pod'a `exec` ile tar olarak aktarılır, hiçbir API nesnesine (Secret, ConfigMap) yazılmaz; olaylar pod'un log'undan okunur ve aynı maskelemeden geçer; pod run bitince silinir. Pod'lar service account token'ı taşımaz, ayrıcalık yükseltemez, bütün capability'ler düşürülmüştür. Worker'ın yetkisi namespace'teki pod'ları oluşturmak, silmek, exec ve log'la sınırlıdır.
- Pod executor EE'nin digest ile sabitlenmesini ister (`image@sha256:...`), böylece run kaydı tam olarak çalışanı adlandırır; `--ansible-allow-tags` (chart: `ansible.allowTags`) sadece geliştirme içindir. Ansible pod'larının sunuculara SSH ile ulaşabilmesi cluster'ın çıkış kurallarına bağlıdır.
- kind'da gerçek bir pod'la denendi: olaylar ve play recap okundu, secret maskelendi, pod silindi; worker'ın yetkileri `kubectl auth can-i` ile doğrulandı.

## 14. Erişim Yönetimi: Takımlar ve Gruplar

Kişiler ve gruplar AD'de oluşturulur ve yönetilir. Gaia **AD'ye kullanıcı veya grup olarak hiçbir şey yazmaz**; AD'yi okur ve var olan grupların erişimini dağıtır. Model, Network ve AnsibleRole ile aynıdır: kaynak dışarıdadır, Gaia oradan obje üretir.

```
AD ──(TeamSource, sync)──► Team objeleri (Git, Gaia'nın commit'i)
                            ▲
Group objeleri (Git, elle) ─┘  ──► kontrattaki access ──► Warpgate, Linux, Gaia arayüzü, Zabbix
AD üyelikleri ──(5 dk sync)──► Gaia veritabanı ──► aynı tüketicilere dağıtım
```

Kişinin yaşam döngüsü (hesap açma, kapatma, parola, kilit açma) Gaia'nın kapsamı dışındadır ve AD ile İK/ITSM süreçlerinde kalır.

### 14.1 TeamSource ve Team

`TeamSource`, hangi AD gruplarının takım sayılacağını ve nasıl eşleneceğini tanımlar:

```yaml
apiVersion: gaia/v1alpha1
kind: TeamSource
metadata:
  name: corp-ad-teams
spec:
  provider: corp-ad
  select: 'group.dn.endsWith(",OU=Teams,DC=corp,DC=local")'           # CEL
  naming: 'group.cn.lowerAscii().replace("sg-team-", "team-")'        # SG-Team-Portal → team-portal
  map:
    displayName: group.description
    approvers: 'group.cn + "-Approvers"'      # onay verebilecekler ayrı bir AD grubu
    secretsPrefix: '"bao://kv/apps/" + name + "/"'
  membership:
    recursive: true                           # iç içe grupları açar
    sync: { every: 5m, mode: usn }            # AD'den sadece değişiklikleri okur
  output: generated/teams/
```

Üretilen Team objesi:

```yaml
apiVersion: gaia/v1alpha1
kind: Team
metadata:
  name: team-portal
  annotations:
    gaia/generated-by: TeamSource/corp-ad-teams
spec:
  displayName: Portal Ekibi
  adGroup: { provider: corp-ad, dn: "CN=SG-Team-Portal,OU=Teams,DC=corp,DC=local" }
  approversGroup: { dn: "CN=SG-Team-Portal-Approvers,OU=Teams,DC=corp,DC=local" }
  secrets: { prefix: "bao://kv/apps/team-portal/" }
```

- **Team, sahiplik birimidir.** Kontratın `labels.owner` alanı bir Team referansıdır; olmayan bir takım yazılırsa yükleme hata verir.
- **Owner onayı:** Policy'deki `owner` onayı (bkz. 6.4) Team'in `approversGroup` üyelerine çözülür. Onaylayanlar ayrı bir AD grubunda tutulur; `managedBy` attribute'u tek kişi veya tek grup alabildiği için tercih edilmedi.
- **Secret öneki:** Secret parametrelerinin izin verilen yol öneki Team'den gelir (bkz. 6.2).
- **Onaycılar:** Change'lerin owner onayı, Team'in `approversGroup` üyelerine çözülür (bkz. 7.6). CODEOWNERS üretilmez.
- **Alan sahipliği:** `adGroup` ve `approversGroup` kaynağa aittir ve patch'lenemez. `displayName`, `secrets` ve `notifications` patch ile değiştirilebilir (bkz. 5.5).
- **Üretim:** `select` ile seçilen her AD grubu bir Team olur. `map.approvers` onaycı grubunun adını (cn) verir; böyle bir grup yoksa Team onaycı grubu olmadan yazılır ve raporda görünür. Aynı ada çıkan iki gruptan hiçbiri yazılmaz.
- **Gaia'nın objenin kaynağını izlemesi:** `source-revision`, AD grubunun objectGUID'idir. Grup yeniden adlandırılsa da aynı kalır.

### 14.2 Üyelik Git'te değil, veritabanında

Takımın kendisi Git'e girer; nadiren değişir ve Gaia'nın commit'iyle gelir. Takımın **üyeleri** ise Git'e girmez, AD'den Gaia'nın veritabanına senkronlanır:

- **Üyeliğin doğru kaynağı ve audit'i AD'dir.** Bir kişinin takıma eklenmesine AD'nin süreci karar verir. Bunun için Git'e yazmanın anlamı yoktur; bir Change'i reddetmek AD'deki gerçeği değiştirmez.
- **Gürültü:** Üyelikler sık değişir. Her değişiklik bir commit olsaydı repo sürekli dolardı.
- **Kişisel veri:** Kullanıcı adları bile Git'e girmez. Kullanıcılar Gaia'nın veritabanında tutulur: kullanıcı adı, objectGUID, görünen ad ve hesabın etkin olup olmadığı.
- **Tekrar üretilebilirlik:** Bir run erişimi hangi üye listesine göre dağıttıysa, o listenin bir kopyası run kaydına yazılır (`accessSnapshot`, bkz. 10.1).

**Senkron:**
- Gaia her Team'in AD grubunun ve onaycı grubunun üyelerini okur (TeamSource'un `membership.recursive`'i ile iç içe gruplar dahil). Varsayılan aralık 5 dakikadır (`membership.sync.every`).
- Hesaplar objectGUID ile tutulur. Hesabın adı (sAMAccountName), görünen adı ve adresi her senkronda güncellenir. Ad yerel bir hesapla çakışırsa Gaia'daki adı `ad.<provider>` ekiyle yazılır (örneğin `ayse.corp-ad`).
- Üyelikler "üye" ve "onaycı" olarak ayrı tutulur. Owner onayı, Team'in onaycı grubu varsa onun üyelerinden, yoksa takımın üyelerinden gelir.
- AD'de devre dışı bırakılan veya silinen hesap Gaia'da kapanır, oturumları ve CLI token'ları biter. AD'de yeniden açılan hesap Gaia'da da açılır; ama bir yöneticinin Gaia'da kapattığı hesabı AD açamaz.
- Her senkronun raporu (`Directory/<provider>`) takıma girenleri, çıkanları ve kapanan hesapları listeler (`GET /api/v1/sources`).

**Giriş:** OIDC sağlayıcısında `directory: { provider, claim }` varsa giriş AD hesabına bağlanır. Claim'in değeri (`@`'den önceki kısmı), Gaia'nın AD'den aldığı hesabın sAMAccountName'idir. Takımlar ve roller token'dan değil AD'den gelir. Gaia'nın tanımadığı ya da kapalı bir hesapla giriş reddedilir.

**Onayın yeri:**

| Değişiklik | Onaylandığı yer |
|---|---|
| Kişinin bir takıma girmesi veya takımdan çıkması | AD süreci |
| Bir takıma veya gruba erişim verilmesi (Group, kontrattaki `access`) | Git'e commit ve Gaia'da onay (bkz. 7.6) |

### 14.3 Group

Group elle yazılır ve Team'lere ya da başka Group'lara referans verir. Group'a doğrudan kullanıcı eklenemez; kullanıcılar her zaman AD'den, bir takım üzerinden gelir.

```yaml
apiVersion: gaia/v1alpha1
kind: Group
metadata:
  name: portal-admins
spec:
  teams: [team-portal, team-dba]
  groups: []                  # iç içe Group
  owner: team-portal          # bu Group'u değiştiren Change'leri onaylayan takım
  privileged: true            # prod'da sudo verir
```

Kontrattaki `access` alanı bir Group'a veya doğrudan bir Team'e referans verir:

```yaml
access:
  - group: portal-admins
    sudo: true
  - team: team-portal
  - team: team-web-ops
    sudo:                           # sadece bu komutlar, root olarak
      - /usr/bin/systemctl restart nginx
      - /usr/bin/systemctl restart podman
```

**`sudo` alanı** üç şekilde yazılır:
- `true`: her komut (`NOPASSWD: ALL`).
- Komut listesi: sadece bu komutlar, root olarak.
  - Her komut mutlak yoluyla ve düz argümanlarla yazılır.
  - sudoers'ın ayraçları (`,` `:` `=` `\`), joker karakterler (`*` `?`), `!`, `ALL` ve `sudoedit` kabul edilmez. Böylece bir liste tam yetkiye dönüşemez.
- `false` ya da hiç yazılmaması: sudo yok.

Sınırlı sudo:
- Bastion'da (Warpgate) sudo hedefi açmaz.
- Zabbix'te sadece okuma hakkı verir.
- Bir kişi birden fazla girdiden erişim alıyorsa en genişi geçer: tam sudo varsa o, yoksa listelerin birleşimi.

**Kurallar**

- **Kendi kendini onaylamak yok.** Bir kişi, kendi üyesi olduğu bir takıma veya gruba erişim veren Change'i onaylayamaz.
- **Ayrıcalıklı gruplar:** `privileged: true` olan bir Group'u değiştiren Change, owner onayına ek olarak `platform-admin` onayı gerektirir.
- **AD üzerinden yapılan eklemeler:** Ayrıcalıklı bir Group'un içindeki bir takıma AD'den yeni biri eklenirse Gaia erişimi dağıtır, ama bunu ayrıca bildirir (ör. "prod sudo yetkisi olan gruba AD'den X eklendi"). Kararın yeri AD olduğu için Gaia engellemez, ama görünür kılar.

**Planla bağlantısı:** Kontratın planı erişimi de içerir. Her girdi için şunlar plandadır: gruba mı takıma mı verildiği, çözülmüş takım listesi, sudo, ayrıcalık ve grubun sahibi. Erişim vermek veya bir Group'un takımlarını değiştirmek planı, dolayısıyla plan hash'ini değiştirir ve bir Change olur. Takımların içindeki kişiler plana girmez; onların değişikliği AD'den gelir ve onaysız dağıtılır (14.5).

**Ne verildiği:** Gaia, Change'in planını kontratta en son uygulanan planla karşılaştırır. "Verilen erişim" sayılanlar:
- yeni bir girdi
- takım listesi genişleyen bir girdi
- tam sudo kazanan bir girdi
- sudo listesine yeni bir komut eklenen bir girdi

Erişimi kaldırmak veya daraltmak (bir komutu çıkarmak dahil) ek onay gerektirmez.

**Verilen erişimin onayları:**
- Ayrıcalıklı bir Group üzerinden: `platform-admin`
- Başka bir takımın sahibi olduğu Group üzerinden: Group'un sahibi (onaycı grubu varsa ondan)
- Erişim kazanan takımların üyeleri bu Change'i onaylayamaz.
- Bu onaylar ortamın `approval` ayarından bağımsızdır.

**Ayrıcalıklı gruba AD'den eklenme:** Takıma AD'de eklenen kişi ayrıcalıklı bir Group'un takımındaysa `access.privileged` bildirimi Group'un sahibine ve `platform-admin` takımlarına gider. Bir dizinin ilk senkronu bildirim üretmez.

### 14.4 Dağıtım

| Tüketici | Gaia nasıl dağıtır |
|---|---|
| Gaia arayüzü | `viewer` / `operator` / `platform-admin` rolleri Team üyeliklerinden gelir (bkz. 11.4). OIDC sadece kimlik doğrulama için kullanılır |
| Warpgate | API ile kullanıcıların rol ve hedef atamaları. Hedef tipi imajdan gelir: Linux için SSH, Windows için RDP |
| Linux | SSSD/AD entegrasyonu varsa sudo ve erişim kuralları AD gruplarına referans verir. Yoksa Ansible yerel hesap açar, SSH anahtarlarını dağıtır ve sudoers yazar (`/etc/sudoers.d/gaia-<kişi>`: tam sudo `ALL=(ALL) NOPASSWD: ALL`, sınırlı sudo `ALL=(root) NOPASSWD: <komutlar>`; `visudo` ile doğrulanır) |
| Zabbix | Kendi API'siyle kullanıcı grubu atamaları |

**SSH anahtarları:**
- Kişiler sunuculara giriş anahtarlarını Gaia profiline kaydeder (`/api/v1/me/login-keys`; eklemek için passkey/MFA yeniden istenir).
- Anahtarlar Git'te tutulmaz: Git'e kullanıcı adı girmez (14.2) ve imza anahtarlarında olduğu gibi anahtarın sahibi Gaia'daki hesaptır.
- AD'de etkin olmayan bir kullanıcının anahtarı dağıtılmaz.

**Host anahtarları:**
- Gaia bir sunucuya ilk ulaştığında (oluşturulduktan hemen sonra, kendi ağı üzerinden) sunucunun sunduğu SSH host anahtarını kaydeder. Sırayla ed25519, ecdsa, rsa denenir; giriş yapmadan, anahtar değişiminden sonra el sıkışma kesilir.
- O andan sonra Ansible host anahtarı kontrolüyle çalışır: `StrictHostKeyChecking=yes`, sadece Gaia'nın kaydettiği anahtarlar.
- Anahtarı değişen bir sunucu run'ı başarısız yapar.
- Bu sürümden önce kurulmuş sunucuların anahtarı ilk temasta kaydedilir.
- cloud-init'in `phone_home`'u ya da konsoldan okuma özel user-data veya hipervizöre özgü erişim gerektirdiği için tercih edilmedi.

**Linux'ta dağıtım:** Gaia, kendi içinde taşıdığı `gaia_access` Ansible rolünü run'ın configure adımında, kontratın baseline ve rollerinden sonra çalıştırır. Rol config reposunun parçası değildir.
- Erişimin takımlarındaki etkin üyeler `gaia-users` grubunda yerel hesap olarak açılır.
- `authorized_keys` tam olarak Gaia'daki anahtarlardır.
- sudo, `/etc/sudoers.d/gaia-<ad>` dosyasıyla verilir ve `visudo` ile doğrulanır.
- Listeden çıkan hesap silinmez, ev dizini kalır. Hesabın süresi doldurulur ve anahtarları ile sudo kuralı kaldırılır. Yeniden listelenince açılır.
- Bir kontratın erişimi tamamen kaldırılırsa, son uygulanan planda erişim olduğu için rol bir kez daha çalışır ve hesapları kapatır.
- Linux hesap adına uymayan kullanıcı adları atlanır ve run çıktısında görünür.
- Run kaydının `accessSnapshot`'ı her erişim girdisi için o anki takımları ve kişileri yazar.

**Warpgate'te dağıtım:**
- Platform'da `bastion: { provider: <warpgate> }` varsa, erişimi olan her kontratın sunucuları Warpgate'e hedef olarak eklenir. Erişimi olmayan kontratın hedefi olmaz; erişim kaldırılınca hedefler silinir.
- Linux sunucuda hedef SSH'tır: Warpgate sunucuya kendi istemci anahtarıyla `gaia-wg` hesabı olarak girer. Erişimde sudo varsa ikinci bir hedef (`<host>-sudo`) sudo yetkili `gaia-wg-sudo` hesabıyla girer. Bu iki hesabı `gaia_access` rolü açar; `authorized_keys` Warpgate'in anahtarlarıdır. Kimin girdiği Warpgate'in oturum kaydındadır.
- Windows sunucuda hedef RDP'dir ve sunucunun kendi oturum açma ekranını gösterir (`interactive_logon`): kişi kendi dizin hesabıyla girer, Warpgate parola tutmaz.
- Hedefler `register` değil `access` aşamasında, sunucu yapılandırıldıktan sonra yazılır: o anda sunucunun host anahtarı kayıtlıdır ve Warpgate'in gireceği hesaplar açılmıştır. Gaia'nın kaydettiği host anahtarı Warpgate'in known-hosts listesine yazılır, aynı adresin başka anahtarları silinir. Warpgate bilinmeyen anahtarı reddedecek şekilde (`auto-reject`) çalışmalıdır.
- Hedefler sunucunun açıklamasında `gaia.uid` taşır ve kaydedilen ID'leriyle bulunur. Aynı adla başkasına ait bir hedef run'ı durdurur.
- Roller `gaia/<kontrat>` ve `gaia/<kontrat>/sudo`'dur. Rollerin üyeleri erişimin takımlarındaki etkin kişilerdir (sudo rolü sudo'lu erişimdekiler). Gaia Warpgate kullanıcısı açmaz (kullanıcılar Warpgate'in SSO'sundan ya da LDAP'ından gelir); kullanıcısı olmayanlar run çıktısında görünür. Gaia sadece `gaia/` önekli rollere dokunur.
- Hedefler ve onay: erişim Gaia'da onaylandığı için Gaia'nın hedeflerinde ticket ve oturum onayı kapalıdır.
- Decommission'da hedefler ve adresin known-hosts kayıtları silinir.
- Warpgate 0.29.2 ile doğrulandı: istemci gerçek bir Warpgate'e karşı test edilir; Warpgate üzerinden `gaia-wg` hesabına SSH çalıştı, yanlış host anahtarında bağlantı reddedildi.

**Zabbix'te dağıtım:**
- Gaia, izlenen her sunucuyu Platform'un host grupları dışında kontratın kendi grubuna da koyar: `Gaia/contract/<kontrat>`. Bu grupta başka kontratın sunucusu olmaz.
- Erişimi olan her takım için `Gaia/<takım>` adında bir kullanıcı grubu vardır. Hakları, takımın erişebildiği bütün kontratların kendi gruplarıdır: sudo varsa okuma-yazma, yoksa okuma. Ortam ve site gibi ortak gruplara hak verilmez.
- Üyeler, takımın Zabbix'te aynı kullanıcı adıyla hesabı olan etkin kişileridir. Gaia Zabbix kullanıcısı açmaz; hesabı olmayanlar run çıktısında görünür.
- Gaia sadece `Gaia/` önekli kullanıcı gruplarına dokunur. Takım kontrattan çıkınca grubu güncellenir ve o kontratın hakkını kaybeder.

### 14.5 Değişiklikler ve işten ayrılma

- **Üyelik değişiklikleri** AD'den 5 dakika içinde görülür ve **iki yönde de otomatik** dağıtılır: erişim eklenir veya kaldırılır. Erişimin kendisi zaten Git'te onaylanmıştır; değişen sadece takımda kimin olduğudur.
- **Hesabı devre dışı bırakılan veya silinen kişi:** Erişimi her yerden kaldırılır ve Gaia oturumları ile CLI token'ları iptal edilir. Bu her ortamda otomatik yapılır, çünkü erişim kaldırmak onay beklememelidir.
- **Erişim run'ı:** Bir takımın kişileri değiştiğinde (AD senkronu: giren, çıkan, kapanan) ya da biri giriş anahtarı ekleyip sildiğinde, son uygulanan erişiminde o takım bulunan kontratlar için "erişim run'ı" kuyruğa girer.
  - Run, kontratın **son uygulanan** planını ve görüntüsünü kullanır. Onay bekleyen bir değişiklik bu yoldan dağıtılmaz.
  - Sunucu açmaz, kapatmaz, baseline ve rolleri çalıştırmaz. Sadece canlı sunucularda erişim play'ini çalıştırır.
  - `requestedBy` alanı `gaia: access`'tir. Run kaydı yeni `accessSnapshot`'ı yazar.
  - Kontrat için bekleyen bir erişim run'ı varsa yenisi eklenmez.
- **Erişim drift'i:** Bu kontroller drift'in "erişim" alanıdır (bkz. 7.5).

### 14.6 AD'ye yazma

Kullanıcı ve grup tarafında Gaia AD'ye yazmaz; ihtiyacı sadece okuma yetkisidir. Sunucu tarafında AD'ye yazma gerekebilir: Windows sunucuların domain'e alınması ve bilgisayar hesaplarının silinmesi, ya da DNS AD üzerindeyse A/PTR kayıtları. Bu ihtiyaç varsa yazma yetkisi sadece bilgisayar hesaplarının OU'su ve DNS zone'u ile sınırlıdır. Kullanıcı ve grup nesnelerini kapsamaz.

- **Ayrı hesap:** `activedirectory` provider'ının `credentials`'ı okuma hesabıdır. Yazma `writeCredentials` ile verilen ayrı bir hesapla yapılır; yazma gereken bir Network'ün provider'ında bu alan yoksa katalog reddedilir.
- **DNS kayıtları:**
  - Network'te `dns: { provider: corp-ad, zone: corp.local, reverse: true, ttl: 3600 }` varsa Gaia her sunucunun A kaydını, `reverse` ile PTR kaydını yazar. TTL verilmezse 3600 saniyedir. Plan bunları sunucu satırında ve özette (`dns +N =M`) gösterir.
  - Kayıtlar AD-integrated zone'lardaki `dnsNode` nesneleridir (MS-DNSP 2.3.2.2). Gaia bunları LDAP ile yazar; DNS sunucuları dizinden okur, replikasyon AD'nin işidir. Hesabın zone'larda yazma yetkisi (ör. DnsAdmins ya da zone üzerinde yetki devri) yeterlidir.
  - PTR, adresi kapsayan en uzun `in-addr.arpa` zone'una yazılır. `reverse: true` iken böyle bir zone yoksa run durur.
  - Yazılan isimler sunucunun dış kimlikleri olarak tutulur (`dns-a`, `dns-ptr`). Gaia'ya ait bir ismin kaydı değişmişse düzeltilir. İsim başka bir adresle başkasına aitse run durur (bkz. 9.2).
  - Kayıtlar `register` aşamasında, izlemeden önce yazılır. Decommission'da izlemeyle birlikte, VM silinmeden önce kaldırılır.
  - Samba AD ile test edilir: kayıtlar DC'nin kendi DNS sunucusundan `dig` ile doğrulanır.
- **Bilgisayar hesapları:**
  - Network'te `domain: { provider: corp-ad, ou: "OU=Servers,DC=corp,DC=local" }` varsa Gaia her sunucunun bilgisayar hesabını sunucu yapılandırılmadan önce (`register` aşaması) bu OU'da açar. Hesabın `info` alanına `gaia.uid` ve kontrat yazılır; objectGUID dış kimlik (`computer`) olarak tutulur. Plan hesabı sunucu satırında ve özette (`computers +N =M`) gösterir.
  - Hesap tek kullanımlık, rastgele bir parolayla açılır. Sunucu, `configure` aşamasında baseline'dan önce Gaia'nın `gaia_domain_join` rolüyle bu parolayla katılır (`realm join --one-time-password`). Katılırken hesabın parolası değişir, yani parola bir kez işe yarar. Gaia'nın hesabı domain'e katılma yetkisi gerekmez; OU'da bilgisayar nesnesi açma, silme ve parola atama yetkisi yeterlidir.
  - Katılım, hostun hesaba yazdığı `dNSHostName`'den anlaşılır (Gaia bu alanı yazmaz). Henüz kimsenin katılmadığı hesaba her run'da yeni parola verilir, çünkü önceki parola saklanmaz. Katılınmış bir hesaba parola verilmez; sunucu domain'de değilse rol hata verir.
  - Host adı NetBIOS sınırı yüzünden en çok 15 karakter olabilir; daha uzun isim plan aşamasında reddedilir. Aynı isimde, sunucunun olmayan bir hesap varsa run durur (bkz. 9.2).
  - Decommission'da hesap GUID'iyle bulunur ve altındaki nesnelerle birlikte silinir (tree delete). Kapatma aşamasında devre dışı bırakma (9.8) Faz 10 ile gelir.
  - Windows sunucular için hesap aynı şekilde açılır; parolayla katılma (unsecured join) Windows yapılandırma desteğiyle gelir.
  - Samba AD ve systemd'li bir Rocky 9 container'ında doğrulandı: rol katıldı, keytab yazıldı, AD kullanıcıları sssd ile çözüldü, ikinci çalıştırma değişiklik yapmadı.

## 15. Açık Konular

- [ ] **`proxmoxve` Pulumi provider'ının olgunluğu:** Topluluk provider'ı; PoC'de doğrulanıyor. Klon, cloud-init, SMBIOS UUID ve tag'ler lab'de çalıştı. Bilinen sorun (v8.6.0, Terraform provider v0.112.0): disk `speed` bloğunun varsayılanı sıfırlarla dolu, okumada ise hiç dönmüyor. Bu yüzden her apply'da diske etkisiz bir update gidiyor. Sağlayıcı bu değeri kendi hesapladığı için `ignoreChanges` onu gizleyemiyor (lab'de denendi); provisioning drift'i bu farkı (`disks[N].speed`) gürültü sayıp atlar. Upstream'e bildirilecek.

- [ ] **Pulp ve Zot'a insan erişimi:**
  - Gaia bu iki sistemde kullanıcı açmaz.
  - Okuma ve yazma hakları, iki sistemin kendi LDAP/OIDC bağlantılarıyla AD gruplarına verilir.
  - Hangi grubun hangi repoya yazacağı tanımlanınca, gerekirse Group'tan dağıtıma eklenir (bkz. 14.4).

## 16. Karar Kaydı

| Tarih | Karar |
|---|---|
| 2026-10-23 | Secret kasası olarak Vault yerine OpenBao |
| 2026-10-23 | Tek YAML kontratı; kontratta sadece referans; kaynaklar Gaia'da provider olarak tanımlı |
| 2026-10-23 | Provisioning Pulumi'de, konfigürasyon Ansible'da; tüm bileşenler açık kaynak |
| 2026-10-23 | Kaçış kapısı olarak sadece `params` şeması |
| 2026-10-23 | Mevcut sunucular için adopt akışı (observe → report → enforce) |
| 2026-10-23 | Audit için Katman 1 (run kaydı + imzalı commit) ve Katman 4 (servis hesabı kısıtları) yeterli |
| 2026-10-23 | Her katalog öğesi ayrı bir YAML objesi |
| 2026-10-23 | `Network` ve `AnsibleRole` üretilir (`NetworkSource`, `AnsibleRoleSource`); diğer kind'lar elle yazılır; elle ekleme ve patch mümkün |
| 2026-10-23 | Rol kind'larında `Ansible` öneki; kontrat alanı `ansibleRoles` |
| 2026-10-23 | İfade dili CEL |
| 2026-10-23 | Üretilen objeler bot PR'ı ile Git'e yazılır *(PR kısmı 2026-10-24'te değişti, bkz. 7.6)* |
| 2026-10-23 | Arayüz sadece görüntüleme ve debug için; OIDC + local; MFA veya passkey zorunlu; CLI tarayıcıda login olup süreli token alır |
| 2026-10-23 | Yetkisiz kontratlar diyagramlarda adıyla görünür, detayı kapalıdır |
| 2026-10-23 | Sunucu kimliği hostname değil, değişmez UID. Hostname tekrar kullanılabilir; çakışma kontrolü planda yapılır |
| 2026-10-23 | Silme her zaman açıkça istenir: `retire`, `lifecycle.state: decommissioned`; kontrat dosyasını silmek kaynak silmez |
| 2026-10-23 | Decommission'da bekleme süresi: prod 7 gün, test 1 gün; IP karantinası 14 gün; prod'da silme için owner + platform-admin onayı |
| 2026-10-23 | Zabbix'te geçmiş saklanmaz: host silinince geçmişiyle birlikte silinir |
| 2026-10-23 | Decommission tamamlanınca kontrat dosyası bot PR ile silinir *(PR kısmı 2026-10-24'te değişti, bkz. 7.6)* |
| 2026-10-23 | Gaia'nın veritabanı PostgreSQL; istenen durum Git'te, işletme durumu veritabanında |
| 2026-10-23 | PostgreSQL'in yapılandırması veritabanı yöneticisinin kararı; Gaia sadece gereksinimlerini tanımlar (12.2) |
| 2026-10-23 | IPAM ayrı servis ve ayrı veritabanı (aynı cluster'da olabilir); Zabbix ayrı cluster |
| 2026-10-23 | `run_events` 90 gün; ham loglar SeaweedFS'te süresiz |
| 2026-10-23 | `gaia reconcile` kapsamda |
| 2026-10-23 | Birincil dağıtım VM (deb/rpm + systemd); Helm chart da hazırlanır |
| 2026-10-23 | Harici runner yok: Ansible'ı Gaia ansible-runner ile çalıştırır; runner bir arayüzün arkasında soyutlanır |
| 2026-10-23 | Ansible execution environment container'ında çalışır: VM'de podman, Kubernetes'te run başına pod; şimdilik tek varsayılan EE |
| 2026-10-23 | Envanterde `inventory_hostname` sunucunun UID'si |
| 2026-10-23 | Ayrılmış segmentler için site worker'ı tasarımda var; ihtiyaç doğunca yazılır |
| 2026-10-23 | `params` formatı: argument_specs tabanı + `gaia` uzantıları + CEL `rules`; rollerde uzantılar `meta/gaia.yml` dosyasında |
| 2026-10-23 | Serbest tipler (`raw`, `json`, serbest `dict`/`list`) yasak; sadece `freeform` ve `platform-admin` onayıyla açılır |
| 2026-10-23 | Secret parametreler sadece `bao://` referansı; owner'a göre yol önekiyle sınırlı |
| 2026-10-23 | `gaia schema export` ile kontratlar için JSON Schema (editör desteği) ilk sürümde |
| 2026-10-23 | Drift alan bazında: kayıtlar 15 dk, versiyon 10 dk, provisioning 1 saat, konfigürasyon günlük gece penceresinde |
| 2026-10-23 | Drift düzeltme: kayıtlar her yerde otomatik; konfigürasyon prod dışında otomatik, prod'da PR; provisioning her zaman PR (`Operation reconcile`) *(PR kısmı 2026-10-24'te değişti, bkz. 7.6)* |
| 2026-10-23 | Roller check-mode uyumlu yazılır; uyumsuzlar `checkMode: unsupported` ile işaretlenir ve drift sonucu "bilinmiyor" olur |
| 2026-10-23 | Drift bildirimleri Zabbix trapper üzerinden |
| 2026-10-23 | Operation bir kez çalışır, merge sonrası değiştirilemez; `notAfter` ile süresi dolar; bot 30 gün sonra dosyayı temizler *(PR kısmı 2026-10-24'te değişti, bkz. 7.6)* |
| 2026-10-23 | Operation hedefi onay anında sabitlenir; batch stratejisi ve hata eşiği; standart adımları (bakım, snapshot, drain, sağlık) Gaia uygular |
| 2026-10-23 | Tekrarlayan işler için `OperationSchedule` (RRULE); onay `perInstance` veya `once` |
| 2026-10-23 | Aksiyonlar: patch, reboot, power, snapshot, revert, replace, reconcile, runbook; runbook sadece `runbook: true` işaretli rollerle |
| 2026-10-23 | Onaylar `gaia/approval` status check'i ile; birden fazla owner'ı etkileyen işlerde `platform-admin` onayı yeterli *(PR kısmı 2026-10-24'te değişti, bkz. 7.6)* |
| 2026-10-23 | `operator` çalışan run'ı arayüzden ve CLI'dan durdurabilir ve duraklatabilir; devam ettirmek PR ile *(PR kısmı 2026-10-24'te değişti, bkz. 7.6)* |
| 2026-10-23 | Kişi yaşam döngüsü Gaia dışında; kullanıcı ve grup tarafında Gaia AD'ye yazmaz, okur ve var olan grupların erişimini dağıtır |
| 2026-10-23 | `TeamSource` AD'den `Team` üretir (bot PR); `Group` elle yazılır ve Team'lere referans verir; `ServiceAccount` kind'ı yok *(PR kısmı 2026-10-24'te değişti, bkz. 7.6)* |
| 2026-10-23 | Takım üyelikleri Git'e girmez, AD'den veritabanına 5 dakikada bir senkronlanır; run kaydında `accessSnapshot` tutulur |
| 2026-10-23 | Onaylayanlar AD'de ayrı bir grup (`approversGroup`); `labels.owner` bir Team referansı; CODEOWNERS bot tarafından üretilir *(PR kısmı 2026-10-24'te değişti, bkz. 7.6)* |
| 2026-10-23 | Üyelik değişiklikleri iki yönde de otomatik dağıtılır; devre dışı hesapların erişimi her ortamda otomatik kaldırılır |
| 2026-10-23 | Gaia rolleri Team üyeliklerinden gelir; OIDC sadece kimlik doğrulama için; SSH anahtarlarının kaynağı Git |
| 2026-10-23 | PostgreSQL minimum 16; kural: River'ın test ettiği en eski ana sürüm |
| 2026-10-23 | Run kayıtlarının arşivi SeaweedFS'te COMPLIANCE modunda Object Lock'lu bucket; ham loglar ayrı bucket'ta |
| 2026-10-23 | Warpgate RDP'yi v0.27.0'dan beri destekliyor; Windows sunucular Warpgate'e RDP hedefi olarak eklenir |
| 2026-10-25 | Warpgate SSH hedefleri kişi başına hesap değil, paylaşılan `gaia-wg` / `gaia-wg-sudo` hesaplarıyla girer; kim girdiğini Warpgate'in oturum kaydı tutar. RDP hedefleri sunucunun kendi oturum açma ekranını kullanır |
| 2026-10-23 | Proxmox VE desteklenir; kontrat hipervizörden bağımsız (`Site.compute`, `Network.attachment`, `Image.templates`) |
| 2026-10-23 | Güç ve snapshot gibi day-2 işler Ansible yerine provider'ların Go API'si ile (ortak `Compute` arayüzü) |
| 2026-10-23 | Kontrat `topology` ile birden fazla site'a yayılabilir; kontrat ve site başına ayrı Pulumi stack'i; site site ilerleme |
| 2026-10-23 | İlk compute provider Proxmox; PoC Proxmox üzerinde |
| 2026-10-23 | Keşif üç düzeyde: hipervizör yapısı ve VM'ler/çevre sistemler API ile kendiliğinden; sunucu içi keşif `Operation discover` ile onaylı |
| 2026-10-23 | Keşif çıktıları: raporlar veritabanında; IPAM ilk doldurması arayüzde gözden geçirilerek; katalog ve adopt taslakları bot PR ile *(PR kısmı 2026-10-24'te değişti, bkz. 7.6)* |
| 2026-10-24 | Pulumi state'i PostgreSQL'de (DIY backend), ayrı veritabanında; SeaweedFS boşalan dizinleri obje olarak listelediği için state'e uygun değil |
| 2026-10-24 | Pulumi CLI sürümü Gaia'da sabit (3.272.0, SDK ile aynı); Gaia ilk çalıştırmada kendisi kurar |
| 2026-10-24 | Her `pulumi up` önce refresh yapar; apply, VM'in planlanan IP'yi guest agent üzerinden bildirdiğini doğrulamadan sunucuyu `active` yapmaz |
| 2026-10-24 | ansible-runner rootless podman'da `--userns keep-id` ile; çalışma dizini tmpfs'te; EE run'da digest'iyle kullanılır, run sırasında imaj çekilmez |
| 2026-10-24 | İzleme `Platform.monitoring` ile: host group'ları label'lardan CEL ile, şablonlar Image OS'una ve rollerin `monitoring` adlarına göre; Zabbix host'u `gaia.uid` tag'iyle bulunur, isimle değil |
| 2026-10-24 | Run kaydı gövdesi hash'lenen metin olarak saklanır (jsonb anahtarları yeniden sıralar); arşiv kopyası gövdeyi baytı baytına taşır; saklama süresi arşiv provider'ında (`retention`) |
| 2026-10-24 | Değişiklik akışı Argo CD modeli: Git istenen durumun geçmişi; değişiklik Gaia'da Change olarak görünür, gereksinimleri yazılır, onaylanınca Gaia uygular (7.6). PR, CODEOWNERS ve `gaia/approval` status check'i yerine geçer |
| 2026-10-24 | Change birimi kontrat; aynı commit'in Change'leri gruplu görünür ve çoklu owner'da `platform-admin` tek seferde onaylayabilir |
| 2026-10-24 | Onay Gaia'da, step-up passkey/MFA ile, plan hash'ine bağlı; onaycıların Git hesabı gerekmez |
| 2026-10-24 | Commit sahibi, Gaia'ya kayıtlı imza anahtarından (SSH/GPG) bulunur; imzasız veya tanınmayan commit uygulanmaz |
| 2026-10-24 | Geri alma Git revert'üyle; Gaia revert commit'ini yazabilir; Gaia izlenen branch'in son halinden başkasını uygulamaz |
| 2026-10-24 | Gaia'nın yazdıkları (üretilen objeler, temizlik, revert) doğrudan izlenen branch'e imzalı commit; bot PR'ı yok; Gaia doğrulamayan commit yazmaz |
| 2026-10-24 | Drift düzeltmesinde `pr` yerine `approval`: düzeltme Change'i onaya düşer |
| 2026-10-24 | Bildirimler: Slack, Microsoft Teams, e-posta (SMTP), webhook; Team ve kişi başına abonelik |
| 2026-10-24 | İlk Git sunucusu GitHub (webhook + fetch); diğerleri aynı arayüze eklenir |
| 2026-10-24 | API REST + JSON (`/api/v1`), OpenAPI 3.1 şeması repoda; arayüzün istemcisi şemadan üretilir; canlı olaylar SSE ile |
| 2026-10-24 | Yol haritası `docs/roadmap.md`'de; arayüz ekranları tasarım taslağı onaylanınca yazılır |
| 2026-10-24 | Run'lar River işi: kontrat başına bir iş, worker planı yeniden alır ve hash değiştiyse uygulamaz; işler tekrar denenmez |
| 2026-10-24 | Çıktı satırları ve Ansible olayları tek sıralı `run_events` akışında; SSE ile canlı, `LISTEN/NOTIFY` ile replikalar arası; ham olaylar `Platform.runLogs` bucket'ına kilitsiz arşivlenir |
| 2026-10-24 | Worker'lar OpenBao'ya AppRole ile girer (`GAIA_BAO_ADDR`, `GAIA_BAO_ROLE_ID`, secret_id dosyası veya systemd credential'ı) |
| 2026-10-24 | Local hesaplar CLI'dan (`gaia user add`) veya API'den açılır ve tek kullanımlık bir kayıt bağlantısıyla passkey kaydeder; 10 kurtarma kodu bir kez gösterilir. Passkey girişi kullanıcı adı sormaz (discoverable credential, user verification zorunlu) |
| 2026-10-24 | Oturumlar opak token, sadece hash'i tutulur; tarayıcı çerezi `SameSite=Strict` ve değişiklik isteklerinde `X-Gaia-Request` başlığı; hassas işlemler son 10 dakikada passkey ister; platform-admin, operator'ün yapabildiklerini kapsar |
| 2026-10-24 | `require: mfa`'da local hesaplar parola (argon2id, en az 12 karakter) + TOTP veya passkey ile girer; TOTP tohumu OpenBao transit anahtarı `gaia-auth` ile şifreli, her kod bir kez geçerli |
| 2026-10-24 | OIDC: yetkilendirme kodu + PKCE + nonce; MFA kanıtı sadece `trustIdp` ile ve `acceptAmr`/`acceptAcr` eşleşirse; yoksa Gaia passkey'i istenir (ilk girişte kaydedilir). Kişi `(provider, sub)` ile bulunur; takımlar her girişte `groupsClaim`'den yenilenir (AD senkronu gelene kadar); local hesapla çakışan kullanıcı adına sağlayıcı adı eklenir |
| 2026-10-24 | CLI girişi: tarayıcıda onay + loopback + PKCE (RFC 8252) ve `--device` (RFC 8628, isteğin IP'si onaydan önce gösterilir); refresh token + kısa access token, OS keychain'de (yoksa 0600 dosya); CLI token'ı CLI veya cihaz girişini onaylayamaz |
| 2026-10-24 | İmza anahtarları (SSH veya OpenPGP) kişinin profiline kaydedilir; parmak izi tekil; eklemek ve kaldırmak son 10 dakikada passkey ister |
| 2026-10-24 | İstenen durum bir anlık görüntü: platform ve kontrat repolarının commit'leri; config reposunun commit'i run için sabitlenir ama plan kimliğine girmez. Repolar bare ayna olarak tutulur; ssh'ta `known_hosts` zorunlu |
| 2026-10-25 | Plan hash'i commit etiketini içermez; plana dokunmayan commit Change'i ve onaylarını korur |
| 2026-10-25 | Başarısız Change aynı plan ve commit'lerle kendiliğinden tekrar açılmaz; yeni commit veya `retry` gerekir |
| 2026-10-25 | Onay ve red plan hash'iyle gönderilir, step-up passkey veya TOTP ile; red gerekçe ister; çoklu owner'da owner onayını platform-admin de verebilir |
| 2026-10-25 | Gaia'nın commit'leri `gaia` sistem hesabının anahtarıyla imzalanır, isteyen `Gaia-Requested-By` ile commit sahibi sayılır; Gaia yerel çalışma kopyasına yazmaz |
| 2026-10-25 | Team objesi TeamSource'tan önce elle yazılabilir; bildirim kanalları Provider (`slack`, `msteams`, `smtp`, `webhook`), abonelikler `Team.spec.notifications` |
| 2026-10-25 | AD üyelikleri veritabanında, hesaplar objectGUID ile; OIDC girişi `directory.claim` ile AD hesabına bağlanır; owner onayı onaycı grubundan; yöneticinin kapattığı hesabı AD açamaz |
| 2026-10-25 | Erişim planın parçası (çözülmüş takımlar); verilen erişim ayrıcalıklıysa platform-admin, başkasının Group'uysa sahibi onaylar, kazanan takımın üyesi onaylayamaz; ayrıcalıklı gruba AD eklemesi bildirilir |
| 2026-10-25 | SSH giriş anahtarları Git'te değil Gaia profilinde (kullanıcı adları Git'e girmez); Linux erişimi Gaia'nın gömülü `gaia_access` rolüyle; çıkarılan hesap silinmez, süresi doldurulur |
| 2026-10-25 | Zabbix'te erişim takım başına `Gaia/<takım>` kullanıcı grubu, sadece kontratların kendi host grubuna (`Gaia/contract/<ad>`) hak; Gaia Zabbix kullanıcısı açmaz |
| 2026-10-25 | Secret parametre değeri planda ve kayıtta değil, sadece run'da; referans `bao://<yol>#<alan>`; `no_log` parametreye kontratta düz değer yazılamaz |
| 2026-10-25 | Host anahtarları ilk temasta Gaia tarafından kaydedilir, sonra Ansible sıkı host key kontrolüyle çalışır (phone_home/konsol yerine) |
| 2026-10-25 | Artifact deposu Nexus yerine Pulp 3 (OS paketleri ve aynaları, Galaxy, PyPI, App dosyaları) ve Zot (OCI: EE, Gaia imajı, Helm chart). İkisi de OSI lisanslı, EULA'sız; depolama SeaweedFS (S3). `nexus` provider'ı mevcut Nexus'u olanlar için kalır; App artifact'ı provider'dan bağımsız `Resolve → {url, sha256}` ile çözülür |
| 2026-10-25 | Kontrattaki `access.sudo`, `true`/`false`'a ek olarak komut listesi alır: sadece o komutlar, root olarak. Komutlar mutlak yollu ve düz yazılır (ayraç, joker, `!`, `ALL`, `sudoedit` yok). Yeni bir komut "verilen erişim" sayılır; daraltmak onay istemez |
| 2026-10-25 | Git'te sadece Server, Operation, OperationSchedule, AnsibleRole, App, Agent ve Baseline kalır. Site, Network, Image, Size, Group, Team, Policy, Platform, AuthConfig, Provider ve Source tanımları Gaia'nın ayar deposuna geçer (bkz. 17) |
| 2026-10-25 | Ayar değişiklikleri öneri → doğrulama/etki → onay (öneren onaylayamaz; kritik ayarlar platform-admin) → yeni ayar sürümü olarak uygulanır; anlık görüntü ayar sürümünü taşır; etkilenen kontratlar için olağan Change'ler açılır |
| 2026-10-25 | Denetim kaydı: bütün yönetim işlemleri ve girişler, eklenir ama değiştirilmez/silinmez; dışa aktarılabilir |
| 2026-10-25 | Giriş LDAP bind + Gaia'nın ikinci faktörü (passkey; mfa'da TOTP da). Kullanıcı ve takımlar dizinden; yerel hesap sadece break-glass, dizin yoksa yerel kullanıcı/takım. OIDC şimdilik kullanılmıyor |

## 17. Git ve Gaia: neyin nerede tutulduğu

> 2026-10-25'te kararlaştırıldı. 2, 3, 4, 5, 11 ve 14. bölümlerde nesnelerin Git'te durduğunu söyleyen yerler, aşağıdaki türler için bu bölümle değişir. Geçiş yol haritasında Faz 12–18'dir.

### 17.1 İlke

Git'te sadece **sunucuların istenen durumu** ve **sunuculara ne kurulacağı** durur:
- `Server`, `Operation`, `OperationSchedule`
- `AnsibleRole`, `App`, `Agent`, `Baseline`

Geri kalan her şey **Gaia'nın ayarıdır**: `Site`, `Network`, `Image`, `Size`, `Group`, `Team`, `Policy`, `Platform`, `AuthConfig`, `Provider` (bildirim kanalları dahil) ve Source tanımları.

**Neden:** Onaycılar iki tarafta da aynıdır ve aynı kurallarla onaylar. Bir ayarın Git'te olması ona ek bir güvence katmıyor. Güvenceyi verenler şunlar:
- değişikliğin onaydan geçmesi
- öneren kişinin kendi değişikliğini onaylayamaması
- denetim kaydının eksiksiz ve değiştirilemez olması

Ayarları Git'ten çıkarmak, platform ekibinin YAML yazıp commit atmadan arayüzden çalışmasını sağlar. "Katalog" da yalnızca kurulabilecek şeylerin listesi olur.

### 17.2 Ayar deposu

- **Biçim:** Ayarlar veritabanında, Git'teki nesnelerle aynı biçimde durur: `apiVersion`, `kind`, `metadata`, `spec`. Doğrulama, referans grafı ve CEL kuralları aynı kodla çalışır. İstenen durum, Git'teki nesneler ile ayar nesnelerinin birlikte yüklenmesiyle elde edilir.
- **Sürüm:** Her uygulanan ayar değişikliği yeni bir **ayar sürümü** üretir (artan bir numara). Önceki sürümler silinmez.
- **Anlık görüntü:** Bir anlık görüntü artık şunlardan oluşur: platform ve kontrat repolarının commit'leri, config reposunun commit'i ve **ayar sürümü**. Plan hash'i, run kaydı ve Change'ler bu sürümü taşır. Böylece "bir plan, bir commit ve bir ayar sürümünden üretilebilir" ilkesi korunur.
- **Sırlar:** Ayarlar sır taşımaz, sadece OpenBao referansı (`bao://…`) taşır.
- **Dışa aktarma:** `gaia settings export` bir ayar sürümünü YAML dosyalarına yazar; `gaia settings import` dosyalardan bir ayar değişikliği önerir. Yedek, ortam kopyalama ve ilk kurulum için kullanılır.

### 17.3 Ayar değişiklikleri

Bir ayarı değiştirmek, bir **ayar değişikliği** önerisidir. Kontrat Change'leri gibi incelenir ve onaylanır.

1. **Öneri:** Kişi arayüzde (ya da API'de) ekleme, değiştirme veya silme önerir. Bir öneri birden fazla nesneyi birlikte değiştirebilir.
2. **Doğrulama ve etki:**
   - Gaia öneriyi uygulanmış gibi yükler: şema, referanslar, CEL kuralları.
   - Hangi kontratların planının değişeceğini hesaplar; örneğin bir ağın DNS'i değişince o ağdaki bütün kontratlar.
   - Hatalı bir öneri kaydedilmez. Etki listesi öneriyle birlikte gösterilir.
3. **Onay:**
   - Öneren kişi kendi önerisini onaylayamaz.
   - `Policy`, `AuthConfig`, `Platform`, `Group` ve ayrıcalıklı erişime dokunan öneriler `platform-admin` onayı ister.
   - Diğerlerini Policy'deki ayar onaycıları (`spec.settings.approvers`, takımlar) ya da bir `platform-admin` onaylar.
   - Ayrıcalıklı erişime dokunmak: ayrıcalıklı bir Group'un içindeki bir takımı değiştirmek.
   - Onay ve red, kontrat Change'lerinde olduğu gibi passkey/TOTP doğrulaması ister ve önerinin içeriğinin hash'ine bağlıdır.
4. **Uygulama:**
   - Onaylanan öneri yeni bir ayar sürümü olarak uygulanır.
   - Gaia istenen durumu yeniden yükler. Planı değişen her kontrat için olağan bir Change açılır; o Change kendi onay kurallarıyla ilerler.
   - Ayar değişikliği kendi başına hiçbir sunucuya dokunmaz.
5. **Red ve eskime:** Red gerekçe ister. Önerinin dayandığı sürümden sonra aynı nesneler başka bir öneriyle değiştiyse, bekleyen öneri eskir ve yeniden yapılmalıdır.

Ayar değişiklikleri Değişiklikler ekranında kontrat Change'leriyle birlikte, "ayar" türüyle listelenir. Bildirimler (`settings.pending`, `settings.applied`, `settings.rejected`) aynı kanallardan gider.

### 17.4 Denetim kaydı

- **Ne yazılır:** Gaia'da yapılan her yönetim işlemi denetim kaydına yazılır:
  - ayar önerileri, onaylar, redler ve uygulamalar (önce/sonra farkıyla)
  - kontrat Change'lerinin onay ve redleri
  - kullanıcı işlemleri (oluşturma, devre dışı bırakma, yeniden kayıt)
  - oturum kapatma
  - başarılı ve başarısız girişler
  - adım adım doğrulamalar (step-up)
  - run iptali, keşif ve sync tetikleme
  - anahtar ekleme ve silme
- **Her kayıtta:** kim, hangi oturumdan ve adresten, ne zaman, hangi işlem, hangi nesne, sonuç.
- **Değiştirilemezlik:** Kayıt eklenir, değiştirilmez ve silinmez. Uygulamanın veritabanı rolü (`gaia_app`) bu tabloya sadece ekleyebilir; `run_records` ile aynı kural.
- **Ekran ve dışa aktarma:** Ayarlar → Denetim kaydı ekranında kişiye, işleme ve nesneye göre süzülür. Dışa aktarılabilir (JSON satırları), böylece bir SIEM'e gönderilebilir.

### 17.5 Kimlik: LDAP

- **Giriş LDAP iledir:**
  - Kişi kullanıcı adı ve dizin parolasıyla girer. Gaia, dizinde kişinin hesabını bulur ve onunla bind ederek parolayı doğrular. Parola Gaia'da saklanmaz.
  - Ardından Gaia'nın ikinci faktörü istenir: `require: passkey` ise passkey; `require: mfa` ise passkey ya da TOTP.
  - İlk girişte ikinci faktör orada kaydedilir.
  - Dizinde devre dışı bırakılan hesap giremez; açık oturumları sonraki eşitlemede kapanır.
- **Kullanıcılar ve takımlar dizinden gelir:**
  - `TeamSource` dizin gruplarından takımları ve üyelerini eşitler.
  - Üçüncü taraf kullanıcılar da kurumun dizinindedir; dizin varken Gaia'da dizin dışı kullanıcı açılmaz.
- **Yerel hesaplar:** Sadece **break-glass** içindir; dizin ya da giriş sorunluyken içeri girmek için bir iki platform-admin hesabı.
- **Dizin yoksa:** Lab ve küçük kurulumlar için yerel kullanıcılar ve elle yazılan takımlar kullanılır. Bu yol, dizin tanımlıyken kapalıdır.
- **OIDC şimdilik kullanılmıyor.** Kodu duruyor ama ayarlarda ve arayüzde sunulmaz. Kurumların SSO'ya geçmesi gündeme gelince yeniden açılır.
- **Ayar:** AuthConfig'te `type: ldap` bir sağlayıcı, bir `activedirectory` Provider'ını gösterir:
  ```yaml
  providers:
    - { name: corp, type: ldap, directory: { provider: corp-ad } }
    - { name: local, type: local, purpose: break-glass }
  ```
  Gaia hesabı kendi okuma hesabıyla bulur, aynı TLS bağlantısında kişinin DN'iyle bind eder. Boş parola reddedilir (anonim bind'e dönmesin diye). Kişinin Gaia'da hesabı olması, yani bildiği bir takımda olması gerekir; hesaplar ve takımlar üyelik eşitlemesiyle gelir.
- **Dizin uyumluluğu:** Bugün Active Directory alanları okunuyor (`objectGUID`, `sAMAccountName`, `userAccountControl`). OpenLDAP veya FreeIPA gerekirse alan eşlemesi ayarlanabilir yapılır.

### 17.6 Source'lar

- Source tanımları Gaia ayarıdır.
- **Çıktıları hedefin sahibine yazılır:**
  - `NetworkSource` ve `TeamSource` ayar deposuna yazar. Yazdıkları, denetim kaydında `source/<ad>` olarak görünen bir ayar sürümüdür.
  - `AnsibleRoleSource` Git'e yazmaya devam eder, çünkü `AnsibleRole` Git'tedir.
- Source çıktıları kendisi onay beklemez. Asıl veri dizinde ya da IPAM'dadır ve orada değişmiştir. Çıktının etkilediği kontratların Change'leri yine onaydan geçer.

### 17.7 Geçiş

- **Var olan kurulumlar:**
  - Gaia ilk açılışta ayar deposu boşsa, Git'teki ayar türlerini (17.1) olduğu gibi içeri alır. Bu ilk ayar sürümü denetim kaydında "Git'ten içeri alındı, commit X" diye görünür.
  - Bundan sonra Git'te kalan bu türdeki nesneler yok sayılır ve Sync ekranında uyarı olarak listelenir.
- **Yeni kurulumlar:** İlk ayarlar `gaia settings import <dizin>` ile yüklenir. İlk platform-admin `gaia user add` ile açılır (break-glass).

### 17.8 Ekran yapısı

```
Genel bakış
Değişiklikler        kontrat Change'leri ve ayar değişiklikleri
Kontratlar           Server
Operasyonlar         Operation, OperationSchedule
Run'lar
Katalog              ne kurulabilir: roller, uygulamalar, ajanlar, baseline'lar
Altyapı              nereye ve neyle: site, ağ, imaj, boyut
Erişim               Group'lar, takımlar, kim hangi kontrata nasıl erişiyor
Ayarlar
  Provider'lar  Kaynaklar (Source ve Sync)  Keşif  Kullanıcılar
  Kimlik doğrulama  Bildirim kanalları  Platform  Policy  Denetim kaydı
```

- Katalog kartlarla gösterilir: ad, açıklama, işletim sistemi, kategori, kaç kontratta kullanıldığı.
- Ayarlarda her sekme bir listedir. Ekleme ve düzenleme sağ üstteki butonla açılan modalda yapılır; modalda form ve YAML görünümü vardır.
- Kaydetmek bir ayar değişikliği önerir; etki listesi ve doğrulama hataları modalda gösterilir.
