# Gaia – Tasarım Taslağı

> Durum: **Taslak v0.1** (2026-10-23)
> Kapsam: Altyapı bileşenlerinin (Linux, Windows/AD, vCenter ve çevre sistemler) tamamını tek bir kontrat üzerinden yöneten servisin tasarımı.

## 0. Amaç ve Prensipler

Teknolojiler çeşitli olsa da hepsini **tek bir YAML kontratı** yönetir.

- **Kontrat sadece referans içerir.** Endpoint, credential ve altyapı detayı kontratta bulunmaz; bunlar Gaia'nın kendi objelerinde tanımlıdır.
- **Provisioning Pulumi'de, konfigürasyon Ansible'da.** Diğer sistemlerle (Zabbix, IPAM, AD, Warpgate, Nexus, OpenBao, SeaweedFS) doğrudan API üzerinden konuşulur.
- **Tüm bileşenler açık kaynak.**
- **Git, doğru kaynaktır.** Apply sadece Git'ten tetiklenir. Arayüz ve CLI ile okuma ve `plan` yapılabilir, ama değişiklik uygulanamaz.
- **Her verinin tek bir sahibi vardır.** Gaia veriyi kopyalamaz, sahibine referans verir.
- **Bir plan tamamen bir commit'ten üretilebilir.** Canlı bir kaynağı okuyarak plan değiştirilemez; kaynaklardan gelen veri önce Git'e yazılır.

## 1. Kavramlar

| Kavram | Ne | Kim yazar |
|---|---|---|
| **Provider** | Bir sisteme nasıl bağlanılacağı (endpoint, credential referansı) | Platform ekibi |
| **Katalog objesi** | Kontratın seçebileceği isimli seçenekler: ağ, imaj, boyut, rol, uygulama… | Platform ekibi veya Source (otomatik) |
| **Source** | Katalog objelerini asıl kaynağından üreten tanım | Platform ekibi |
| **Policy** | İsimlendirme, ortam kuralları, zorunlu etiketler | Platform ekibi |
| **Kontrat** | İstenen durum (`Server`) veya tek seferlik iş (`Operation`) | Uygulama ekipleri |
| **Run** | Bir kontratın bir commit'teki uygulaması ve bu uygulamanın değiştirilemez kaydı | Gaia |

Katalog, provider ile kontrat arasındaki katmandır. Kontrat doğrudan provider'a bağlansaydı kullanıcı cluster, datastore ve portgroup gibi detayları bilmek zorunda kalırdı. Katalogla birlikte tek bir isim (ör. `network: ank-prod-app`) birden çok sisteme yayılır: vCenter portgroup'u, IPAM havuzu, DNS zone'u ve Zabbix proxy'si aynı isimden çözülür.

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
- **Sahiplik:** Kimin hangi dizini onaylayacağı CODEOWNERS ile belirlenir.

## 3. Kind Listesi

| Kind | Grup | Elle / üretilen | Not |
|---|---|---|---|
| `Platform` | Sistem | Elle | Tekil. Engine ayarları |
| `AuthConfig` | Sistem | Elle | Kimlik doğrulama, bkz. bölüm 11 |
| `Provider` | Sistem | Elle | Her dış sistem için bir tane |
| `Policy` | Sistem | Elle | İsimlendirme, ortamlar, zorunlu etiketler |
| `Site` | Katalog | Elle | Lokasyon: vCenter yerleşimi, NTP, Zabbix proxy |
| `Network` | Katalog | **Üretilen** + elle | `NetworkSource` üretir |
| `NetworkSource` | Source | Elle | IPAM'dan Network üretir |
| `Image` | Katalog | Elle | VM şablonu |
| `Size` | Katalog | Elle | CPU/RAM/disk profili |
| `Baseline` | Katalog | Elle | Hardening playbook'u |
| `AnsibleRole` | Katalog | **Üretilen** + elle | `AnsibleRoleSource` üretir |
| `AnsibleRoleSource` | Source | Elle | Ansible reposundan AnsibleRole üretir |
| `App` | Katalog | Elle | Uygulama: artifact, playbook, parametreler |
| `Agent` | Katalog | Elle | BigFix, Kaspersky, Zabbix ajanı… |
| `Server` | Kontrat | Elle | İstenen durum |
| `Operation` | Kontrat | Elle | Tek seferlik iş: patch, snapshot… |

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

Tanımlı provider tipleri: `openbao`, `vsphere`, `ipam`, `activedirectory`, `zabbix`, `warpgate`, `nexus`, `s3` (SeaweedFS), `git`. Harici bir Ansible runner'ı yoktur (bkz. 7.4).

### 4.2 Platform

```yaml
apiVersion: gaia/v1alpha1
kind: Platform
metadata:
  name: platform
spec:
  secrets: bao
  engines:
    provisioning:  { tool: pulumi,  state: state }        # Pulumi Automation API, state SeaweedFS'te
    configuration: { tool: ansible, executor: ansible-runner, source: config-repo }   # ayrıntılar 7.4'te
  runArchive: state                                       # run logları ve kayıtları
```

### 4.3 Örnek katalog objeleri

```yaml
apiVersion: gaia/v1alpha1
kind: Site
metadata:
  name: ank
spec:
  vsphere: { provider: vc-ank, datacenter: DC-ANK, cluster: CL-PROD-01, folder: /Prod }
  ntp: [ntp1.corp.local, ntp2.corp.local]
  zabbixProxy: zbx-proxy-ank
---
apiVersion: gaia/v1alpha1
kind: Image
metadata:
  name: rhel9
spec:
  os: linux
  template: tpl-rhel9-latest
  connection: ssh
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
  artifact: { provider: nexus, repository: releases, path: portal/portal-${version}.tar.gz }
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
  secrets:
    allowedPrefix: '"bao://kv/apps/" + labels.owner + "/"'          # CEL: secret parametreleri bu önekle sınırlı (6.2)
  environments:
    prod: { approval: required, deleteProtection: true }
    test: { approval: none }
```

## 5. Üretilen Objeler

`Network` ve `AnsibleRole` objeleri asıl kaynaklarından üretilir. Diğer bütün kind'lar elle yazılır.

### 5.1 İfade dili: CEL

Source'ların `map` ve `naming` alanlarında, ayrıca Policy'de ifade dili olarak **CEL** kullanılır.

- **Neden CEL:** Yan etkisi yok, tip kontrolü yapıyor ve Go kütüphanesi olgun. Go `text/template` koşul ve döngü desteklediği için kaynak objelerin içine zamanla mantık yazılmaya başlanır.
- **Özel fonksiyonlar:** Gaia ihtiyaç duyulan yardımcı fonksiyonları kendisi kaydeder, ör. `pad(n, width)`.
- **Hatalar:** Bir ifade değerlendirilemezse o obje üretilmez. Hata sync raporunda ve bot PR'ında görünür.

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
    portgroup: '"dvPG-VLAN" + string(subnet.vlan)'
    gateway: subnet.gateway
    ipam: '{"pool": subnet.cidr}'
    dns: '{"provider": "corp-ad", "zone": subnet.dnsZone, "reverse": true}'
    allowedEnvironments: '[subnet.env]'
    labels: '{"site": subnet.site, "env": subnet.env, "zone": subnet.zone}'
  output: generated/networks/ank/
  trigger: { schedule: "*/30 * * * *", webhook: true }
```

Örnek, subnet üzerinde site, env, zone, vlan, gateway, dnsZone ve tags alanlarının tanımlı olduğu bir IPAM varsayar.

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
- **`meta/gaia.yml`:** Gaia'ya özgü alanlar (`monitoring`, `conflicts`, parametre uzantıları ve `rules`) rolün yanında tutulur, böylece rol ve metadata'sı aynı PR'da birlikte değişir. Parametre formatı 6.2'de.

### 5.4 Bot PR akışı

1. Sync bileşeni Source'u değerlendirir ve objeleri `generated/` dizinine yazar.
2. Fark varsa **bot olarak PR açar.**
3. Gaia aynı PR'a **etki planını** yazar (bkz. bölüm 7.3).
4. PR normal onay sürecinden geçer.

Üretilen her objede şu annotation'lar bulunur:

```yaml
metadata:
  annotations:
    gaia/generated-by: AnsibleRoleSource/corp-roles
    gaia/source-revision: 3f9c2e1
    gaia/content-hash: sha256:9d1e…
```

`content-hash` tutmayan, yani elle düzenlenmiş bir obje CI'da ve yükleme sırasında reddedilir.

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
| Network | pool, gateway, site, DNS zone | labels, allowedEnvironments, description |
| AnsibleRole | source, revision, params, os | labels, monitoring, conflicts, allowedEnvironments |

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
    - group: SG-Portal-Admins
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
| `network` | Network (site da buradan gelir) |
| `baseline` | Baseline |
| `ansibleRoles` | AnsibleRole |
| `apps[].name` | App |
| `agents` | Agent (zorunlu ajanlar Policy'den kendiliğinden eklenir) |

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
    - rule: 'params.heapMiB <= size.memoryGiB * 1024 * 0.75'
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
  - rule: 'params.tomcat_heap_mib <= size.memoryGiB * 1024 * 0.75'
    message: "Heap, sunucu belleğinin %75'ini aşamaz"
```

`AnsibleRoleSource` bu iki dosyayı birleştirir ve katalogdaki `AnsibleRole` objesini App ile aynı formatta üretir. Kaynakta iki dosya olsa da katalogda tek format vardır.

#### Kurallar

1. **Kısıtlı tipler.** `raw`, `json`, `jsonarg`, `options`'ı olmayan `dict` ve `elements`'ı olmayan `list` reddedilir. Gerçekten serbest yapı gereken bir parametre için `gaia: { freeform: true }` yazılır. Bu, katalog PR'ında `platform-admin` onayı gerektirir.
2. **Secret parametreler.** `secret: true` olan bir parametre sadece `bao://` referansı kabul eder. Referans, Policy'de kontratın owner'ı için izin verilen yol önekiyle sınırlıdır (bkz. 4.4); bir takım başka bir takımın secret'ına referans veremez. Değer run sırasında çözülür ve loglarda maskelenir.
3. **Kurallar CEL ile.** `rules` ifadeleri `params`, `size`, `image`, `labels` ve `network` değerlerini görebilir. Kubernetes'in CRD'lerde CEL ile yaptığı validasyonla aynı yaklaşım.
4. **İki aşamalı doğrulama.** Gaia plan sırasında doğrular ve hata PR'da hemen görünür. Ansible çalışırken `argument_specs` ile bir kez daha doğrular. Roller için iki doğrulama aynı kaynaktan geldiği için birbiriyle çelişmez.
5. **Varsayılanlar planda görünür.** Kontratta yazılmayan parametrelerin varsayılan değerleri planda açıkça gösterilir. Böylece hangi değerle çalışıldığı run kaydında yer alır.
6. **Uyumluluk.** Bir rolden parametre kaldırılırsa ya da adı değişirse bot PR'ının etki planı o parametreyi kullanan kontratları listeler ve PR geçmez. Kaldırma önce `gaia: { deprecated: "yerine x kullanın" }` ile duyurulur.

#### Editör desteği

Gaia belirli bir commit'teki katalogdan kontrat şemasını JSON Schema olarak dışa verir (`gaia schema export`). Şema seçime göre daralır; örneğin `apps[].name: portal` seçildiğinde sadece portal'ın parametreleri önerilir. VS Code'un YAML eklentisi bu şemayla kontrat yazarken otomatik tamamlama yapar ve hataları anında gösterir. JSON Schema elle yazılmaz, Gaia üretir. İlk sürümün kapsamında.

### 6.3 Versiyon güncellemesi

`apps[].params.version` değerinin değişmesi planda rolling update olarak görünür: sunucu load balancer'dan çıkarılır, güncellenir, sağlık kontrolünden geçer ve geri alınır. Böylece uygulama güncellemesi ayrı bir iş olmaktan çıkıp kontrattaki tek bir alanın değişmesine dönüşür.

### 6.4 Operation

Kalıcı durum olmayan işler ayrı bir kind ile tanımlanır:

```yaml
apiVersion: gaia/v1alpha1
kind: Operation
metadata:
  name: patch-2026-11-w2
spec:
  action: patch                      # patch | snapshot | revert | power | replace | reconcile | …
  selector: 'labels.env == "prod" && spec.patch.wave == 2'
  options: { scope: security, reboot: auto, snapshot: true }
```

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
  warpgate  + 3 SSH hedefi
  ansible   ▸ linux-cis-l1 → web.nginx → web.tomcat → portal 2.4.1 → ajanlar
env=prod → onay gerekli
```

Aşama sırası: `validate → ipam → pulumi → register → ansible → verify`. Bir aşama başarısız olursa sonraki aşamalar çalışmaz ve run'ın hangi aşamada durduğu kayıtta görünür.

### 7.3 Katalog değişikliğinin etkisi

Katalog PR'larında Gaia, değişikliğin kaç kontratı ve kaç sunucuyu etkilediğini gösteren bir etki planı üretir. Değişikliğin mevcut sunuculara nasıl yayılacağı kind'a göre sabittir:

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

**Execution environment (EE):** Collection'ların ve Python bağımlılıklarının sürümleri sabitlenmiş bir container imajı. `ansible-builder` ile üretilir ve Nexus'ta tutulur.
- Şimdilik tek bir varsayılan EE var. Rollerin farklı EE seçmesi gerekirse bir `ExecutionEnvironment` kind'ı eklenir.
- EE imajının digest'i run kaydına yazılır. Böylece bir run tamamen tekrar üretilebilir: kontrat commit'i, katalog commit'i, config reposu commit'i ve EE digest'i.

```yaml
kind: Platform
spec:
  engines:
    configuration:
      tool: ansible
      executor: ansible-runner
      executionEnvironment: nexus.corp.local/gaia/ee-default@sha256:4c1e…
      source: config-repo
      limits: { concurrentRuns: 10, forks: 25, timeout: 2h }
```

**Gaia'nın sorumlulukları:** Run'ı iptal etmek, timeout'u uygulamak, eşzamanlılık limitlerini korumak ve container'ı temizlemek. Kalan işi ansible-runner yapar.

**Site worker'ı:** Ayrılmış bir ağ segmentindeki sunuculara merkezden SSH/WinRM ile erişilemiyorsa o segmentte bir site worker'ı çalışır (`gaia server --role=worker --site=dmz`).
- Site worker veritabanına bağlanmaz. Gaia API'sine mTLS ile, sadece dışarıya doğru bağlanır, iş alır ve olayları geri gönderir. Böylece segmentin içine doğru firewall izni açmak gerekmez.
- Pulumi işleri merkezde kalır; vCenter API'si oradan erişilebilir durumda.
- Site worker tasarımda var, ama gerçekten ihtiyaç doğana kadar yazılmaz.

**Runner soyutlaması:** Ansible'ı çalıştıran katman bir arayüzün arkasında durur. İleride AWX gibi bir runner zorunlu tutulursa bu arayüze yeni bir uygulama eklenebilir. Şimdilik tek uygulama ansible-runner.

### 7.5 Drift kontrolü

Drift, kontratın Git'teki istenen durumu ile sistemlerin gerçek durumu arasındaki farktır. Her alanın tespit yöntemi, maliyeti ve riski farklı olduğu için drift alan bazında ele alınır.

#### Alanlar

| Alan | Tespit yöntemi | Maliyet | Varsayılan sıklık |
|---|---|---|---|
| Kayıtlar (Zabbix, DNS, IPAM, Warpgate, AD, vCenter tag'leri) | API'den okuyup karşılaştırma | Düşük | 15 dk |
| Uygulama versiyonu | `App` objesindeki `versionProbe` | Düşük | 10 dk |
| Provisioning (VM boyutu, disk, network) | Pulumi `preview --refresh`; state'e yazmaz | Orta | 1 saat |
| Konfigürasyon (OS, roller, baseline) | Ansible `--check --diff`; aynı commit ve aynı EE ile | Yüksek: her sunucuya SSH/WinRM | Günde bir, gece penceresinde |
| Katalog sapması | Otomatik yayılmayan katalog değişiklikleri (ör. Size, bkz. 7.3) | Yok: plan sırasında çıkar | Her katalog değişikliğinde |
| Sahipsiz kaynaklar | `gaia.uid` taşıyıp veritabanında karşılığı olmayanlar; Gaia'nın yönettiği folder'larda UID'siz VM'ler | Orta (keşif motoru) | Günde bir |
| Süresi dolan istisnalar | Kontrattaki `exceptions` | Yok | Günde bir |

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

Düzeltme de "her değişiklik Git'ten geçer" kuralına uyar. İstenen durum değişmez, zaten onaylı bir commit'te durur. Değişen, gerçek durumdur.

- **`auto`:** Gaia aynı commit'e yeniden yakınsar. Run kaydında `requestedBy: gaia-drift` yazar; onay olarak orijinal commit'in onayına referans verir. Sadece düşük riskli sınıflarda kullanılır.
- **`pr`:** Gaia'nın botu bir `Operation` (`action: reconcile`) PR'ı açar ve drift raporunu PR açıklamasına yazar. PR merge edilince düzeltme çalışır. Prod ve yüksek riskli sınıflar için kullanılır.

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
      configuration: { default: auto, prod: pr }
      provisioning:  pr                           # VM boyutu, disk: hiçbir zaman otomatik değil
```

**Drift'i kabul etmek:** Bilinçli bir sapma kontrata `exceptions` kaydı eklenerek kabul edilir. Bu bir PR olduğu için karar Git'te kayıt altında kalır. Arayüzden drift kabul etmenin bir yolu yoktur.

**Çakışma:** Bir kontrat için apply çalışırken o kontratta drift taraması yapılmaz; kontrat başına alınan kilit bunu sağlar.

#### Kayıt, gösterim ve bildirim

- **Tekilleştirme:** `drift_reports` tablosunda her drift `(host_uid, alan, anahtar)` ile tekilleştirilir; aynı sapma her taramada yeni kayıt oluşturmaz. Kayıtta `first_seen`, `last_seen` ve `resolved_at` alanları vardır. Durum `open`, `resolved`, `accepted` (istisnaya dönüştü) veya `unknown` (check-mode desteklenmiyor) olabilir.
- **Arayüz:** Genel bakışta alan bazında açık drift sayıları görünür. Kontrat sayfasında sunucu ve alan bazında diff gösterilir (Pulumi diff veya Ansible diff, secret'lar maskeli). İlk görülme zamanı ve varsa düzeltme PR'ının linki de yanında yer alır.
- **Olası kaynak:** Drift'in ilk görüldüğü zaman aralığında aynı sunucuda bir Warpgate oturumu varsa, arayüz bunu "olası kaynak" olarak gösterir.
- **Bildirim:** Gaia drift sayılarını Zabbix'e trapper item olarak gönderir, alarmları Zabbix'teki trigger'lar üretir. Ayrı bir bildirim kanalı kurulmaz; müşterinin mevcut eskalasyon düzeni drift için de çalışır.

## 8. Adopt (Mevcut Sunucuları İçeri Alma)

### 8.1 Akış

1. **Keşif.** vCenter, Zabbix, AD, DNS ve IPAM taranır. Kayıtlar VM UUID'si, hostname ve IP üzerinden eşleştirilir. Çıktı bir rapordur: her yerde kaydı olanlar, eksik kayıtlar, sahipsiz IP'ler.
2. **Taslak kontrat.** Gözlenen değerler kataloğa ters yönde eşlenir (portgroup → `network`, CPU/RAM → `size`). Eşleşmeyenler açıkça işaretlenir.
3. **İnceleme.** Taslak PR olarak açılır. Keşfin bilemeyeceği alanları (owner, app, ortam) insan tamamlar.
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

## 9. Sunucu Kimliği ve Silme

Ana kural: **bir şeyin silinmesi her zaman açıkça istenir.** Dosya silmek ya da satır kaldırmak gibi örtük işlemler kaynak silmez.

### 9.1 Sunucu kimliği: UID

Hostname'in tekrar kullanılması engellenemez. Silinen bir sunucunun adı yeniden verilebilir, Gaia dışında da aynı isimle makine açılabilir. Bu yüzden Gaia bir sunucuyu hostname ile değil, oluşturulduğu anda atanan değişmez bir **UID** ile tanır. Hostname sadece sunucunun bir özelliğidir.

- **Dış sistemlerdeki kimlikler:** Gaia'nın veritabanı her UID için bu kimlikleri tutar: vCenter instance UUID, AD objectGUID, Zabbix hostid, Warpgate hedef ID'si, ajan konsollarındaki ID'ler, IPAM kaydı. Bu kayıtlar sonradan isimle aranmaz, bu ID'lerle bulunur.
- **İşaretleme:** UID dış sistemlere de yazılır: vCenter'da `gaia.uid` custom attribute'u, Zabbix'te `gaia.uid` tag'i, AD bilgisayar hesabında uygun bir attribute.
- **Pulumi kaynak adı UID'den türetilir** (`vm-<uid>`). Böylece aynı isimli eski sunucu henüz silinmemişken açılan yeni sunucu stack'te onunla çakışmaz.
- **Görüntüleme:** Run kayıtları, loglar ve arayüz sunucuyu hostname ve UID ile birlikte gösterir (`ankppor03 · 7f3k`). Aynı isimli farklı sunucuların geçmişi birbirine karışmaz.

### 9.2 İsim ayırma ve çakışma kontrolü

- **Sıra numarası:** En küçük boş numara verilir. Bir isim, onu taşıyan sunucu silinene kadar dolu sayılır; bekleme süresi de buna dahil. Silme tamamlanınca isim boşalır ve tekrar kullanılabilir.
- **Çakışma kontrolü:** Plandan önce yeni isim vCenter'da, AD'de (tüm DC'lerde), DNS'te, Zabbix'te, Warpgate'te ve ajan konsollarında aranır:
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

Silme tamamlandıktan sonra `retire` satırının bir etkisi kalmaz; bot bunu bir PR ile temizler. Bir sunucuyu `retire` listesine eklemeden sadece `Operation` ile silmek mümkün değildir. Aksi halde kontrat hâlâ aynı sayıda sunucu istediği için bir sonraki apply silinen sunucunun yerine yenisini kurar.

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

Replace işleminde bekleme süresi uygulanmaz, çünkü isim ve IP yeni sunucuya geçer. Güvenlik için eski sunucunun son snapshot'ı alınır.

### 9.6 Kontratın kaldırılması

```yaml
spec:
  lifecycle:
    state: active              # active | decommissioned
    deletionPolicy: delete     # delete | retain
```

- **Kontrat dosyasını silmek kaynakları silmez.** Yaşayan kaynağı olan bir kontratın dosyası silinirse CI bunu reddeder.
- **Silme iki adımda yapılır:**
  1. Kontratta `lifecycle.state: decommissioned` yapılır ve PR merge edilir. Sunucular decommission akışına girer.
  2. Akış tamamlanınca bot, kontrat dosyasını silen bir PR açar.
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

### 9.9 Prod koruması ve Policy

Prod'da silme iki onay gerektirir: kontratın owner'ı ve `platform-admin`. Bu CODEOWNERS ile zorunlu tutulur. Onaylanan plan ile uygulanan planın hash'i aynı olmalıdır.

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
- **Üretilen objeler:** IPAM'dan bir subnet kalkarsa bot PR ilgili `Network` objesini siler. Ağ kullanımdaysa aynı kural nedeniyle PR geçmez.
- **Kullanımdan kaldırma:** Objeye `gaia/deprecated: "true"` annotation'ı eklenir. Yeni kontratlar bu objeyi seçemez, mevcut kontratların planında uyarı çıkar.

## 10. Audit

İş Git'e dayandığı için iki katman yeterli bulundu.

### 10.1 Katman 1: Değiştirilemez run kaydı

```yaml
run: run-20261104-0042
requestedBy: mehmet.y                    # PR yazarı
approvedBy: [ayse.k]                     # PR onaylayanlar
source: { repo: platform/contracts, commit: 3f9c2e1, signed: true }
catalogCommit: 9a1b0c4
configCommit: 51e07aa                    # Ansible reposu
executionEnvironment: nexus.corp.local/gaia/ee-default@sha256:4c1e…
contract: portal-web
planHash: sha256:ab12…                   # onaylanan plan ile uygulanan plan aynı mı
executedAs: { vc-ank: svc-gaia-vm, corp-ad: svc-gaia-ad, linux: gaia }
startedAt: 2026-11-04T14:02:11Z
finishedAt: 2026-11-04T14:19:40Z
result: failed
changes: [...]
```

- **Commit imzası zorunlu, branch'ler korumalı.** "Kim istedi" sorusunun kanıtı imzalı commit'tir.
- **Kayıtlar hash zinciriyle bağlıdır** ve anında SIEM'e gönderilir. Silinen veya değiştirilen bir kayıt zinciri kırar.
- **Arşiv:** Kayıtların bir kopyası SeaweedFS'te tutulur. Object lock (WORM) desteği doğrulanmalı.

### 10.2 Katman 4: Servis hesaplarını insanlar kullanamasın

Bir servis hesabıyla yapılmış işlem, ancak o hesabı sadece Gaia'nın kullanabildiği kanıtlanabiliyorsa kanıt değeri taşır.

- Credential'lar sadece OpenBao'da durur ve onlara sadece Gaia'nın policy'si erişebilir.
- AD'de bu hesaplara interaktif oturum açma GPO ile yasaklanır.
- Hesap Gaia'nın IP'si dışından kullanılırsa Zabbix/SIEM'de alarm üretilir.

## 11. Arayüz ve CLI

### 11.1 Prensip

Arayüz ve CLI aynı API'nin istemcileridir; ikisinin yetkisi aynıdır. İnsan token'larıyla okuma ve `plan` yapılabilir, apply yapılamaz. Git'in tetiklediği apply işleri ayrı bir makine kimliğiyle (CI'ın OIDC token'ı, workload identity) çalışır.

Arayüzün amacı diyagramları görmek, logları takip etmek ve hataları debug etmektir.

### 11.2 Kimlik doğrulama

```yaml
apiVersion: gaia/v1alpha1
kind: AuthConfig
metadata:
  name: default
spec:
  require: passkey                    # mfa | passkey
  providers:
    - name: corp-sso
      type: oidc
      issuer: https://sso.corp.local/realms/corp
      clientSecret: bao://kv/platform/gaia/oidc
      groupsClaim: groups
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

Roller OIDC grup claim'inden türetilir.

| Rol | Görebildikleri |
|---|---|
| `viewer` | Kendi takımının kontratları (`labels.owner`) |
| `operator` | Tüm kontratlar, loglar, `plan` |
| `platform-admin` | Ek olarak provider sağlığı, oturum yönetimi, kullanıcılar |

**Görünürlük kuralı:** Bir kullanıcının yetkisi olmayan bir kontrat, ilişki diyagramlarında veya "kullanan kontratlar" listelerinde (ör. aynı ağı kullananlar) **adıyla görünür, detayı kapalıdır.** YAML'ı, sunucuları, logları ve run kayıtları açılmaz.

### 11.5 Ekranlar

1. **Genel bakış.** Son run'lar, başarısız olanlar, drift sayıları, bekleyen bot PR'ları ve süresi dolmak üzere olan istisnalar.
2. **Kontratlar.** YAML ve çözümlenmiş hali, ilişki diyagramı (kontrat → katalog objeleri → provider'lar → gerçek kaynaklar), sunucular, yönetim seviyeleri ve drift (sunucu ve alan bazında diff, olası kaynak, düzeltme PR'ı).
3. **Run'lar.** Aşama zaman çizelgesi, host × task matrisi, canlı log, Pulumi diff ve run kaydı. Hata ilgili provider'a ve onun credential referansına bağlanır.
4. **Katalog.** Kind'a göre gezinme ve label filtreleri. Her obje için nereden üretildiği, patch'leri ve onu kullanan kontratlar.
5. **Provider'lar.** Bağlantı durumu, son başarılı çağrı, credential'ın geçerlilik durumu.
6. **Sync.** Source'ların son sync zamanı, CEL hataları ve açık bot PR'ları.
7. **Keşif.** Adopt raporları ve eşleşmeyen kaynaklar.

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
| Sync durumu | Source'ların son revizyonu, açık bot PR'ları, CEL hataları | Sonraki sync'te yeniden oluşur |
| Keşif ve drift | Adopt raporları, drift sonuçları | Yeniden taranır |
| Kimlik doğrulama | Local kullanıcılar, passkey'ler, TOTP, kurtarma kodları, oturumlar, device kodları | Kullanıcılar yeniden kayıt olur, oturumlar düşer |
| Katalog indeksi | Yüklenen commit'in objeleri ve ters referansları | Git'ten yeniden üretilir (cache) |

IPAM bu veritabanında değildir. IPAM ayrı bir servis ve ayrı bir veritabanıdır; Gaia ona diğer sistemler gibi bir provider üzerinden bağlanır. Böylece provider modeli korunur ve gerekirse NetBox gibi bir alternatife geçmek için sadece provider değişir. Aynı PostgreSQL cluster'ında ayrı bir veritabanı olarak durabilir. Zabbix'in veritabanı ise yüksek yazma yükü nedeniyle ayrı bir cluster'da olmalıdır.

### 12.2 PostgreSQL ve Gaia'nın gereksinimleri

Veritabanı PostgreSQL'dir. PostgreSQL'in nasıl kurulacağı ve işletileceği (HA, replikasyon, yedek, izleme) **veritabanı yöneticisinin kararıdır**; Gaia buna bağımlı değildir. Gaia'nın beklediği şeyler şunlar:

- **Bağlantı:** Bir DSN, `bao://kv/platform/gaia/db` referansıyla OpenBao'dan okunur. TLS zorunludur (`sslmode=verify-full`).
- **Sürüm:** PostgreSQL 15 veya üstü önerilir. Kesin minimum sürüm, kullanılan kütüphanelere göre netleştirilecek.
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

-- Sync, keşif, drift
sync_runs     (source, revision, status, pr_url, errors jsonb, finished_at)
discoveries   (id, started_at, findings jsonb)
drift_reports (contract, host_uid, area, key, status, findings jsonb, first_seen, last_seen, resolved_at)   -- bkz. 7.5

-- Kimlik doğrulama
users                (id, username, provider, created_at, disabled_at)
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
- **`gaia reconcile`:** vCenter, Zabbix ve AD'yi tarar ve UID'leri `gaia.uid` işaretlerinden bularak `hosts` ile `host_external_ids` tablolarını yeniden kurar.

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

- **Paketleme:** `nfpm` ile `.deb` ve `.rpm` paketleri üretilir ve Nexus'taki apt/yum reposuna konur.
- **Servis:** systemd unit'i ile çalışır. Konfigürasyon `/etc/gaia/` altında durur; servis ayrı bir sistem kullanıcısıyla, systemd'nin hardening seçenekleri açık şekilde çalışır.
- **OpenBao kimliği:** AppRole. `secret_id` OpenBao Agent ile ya da systemd credentials üzerinden verilir.
- **Migration:** Güncelleme sırasında `gaia migrate`, `gaia_owner` rolüyle ayrı bir adım olarak çalıştırılır.
- **Ansible çalıştırma:** Worker sunucularında EE container'ları için `podman` kurulu olmalıdır. EE imajları Nexus'tan çekilir.
- **Kurulumun kendisi:** Gaia'yı VM'e kuran bir Ansible rolü (`gaia.server`) hazırlanır. Gaia'yı da aynı araçlarla kurmuş oluruz.

### 13.3 Kubernetes (Helm)

- **Chart içeriği:** Deployment, Service, Ingress, ConfigMap, ServiceAccount ve PodDisruptionBudget. HorizontalPodAutoscaler isteğe bağlı.
- **Migration:** `pre-install` / `pre-upgrade` hook'u olarak çalışan bir Job.
- **OpenBao kimliği:** Kubernetes auth method. Secret'lar OpenBao Agent Injector veya CSI provider ile pod'a gelir; chart Kubernetes Secret'ına düz metin yazmaz.
- **Rol ayrımı:** `values.yaml` ile API ve worker ayrı Deployment olarak açılabilir.
- **Ansible çalıştırma:** Her Ansible run'ı ayrı bir pod olarak çalışır. Worker'ın ServiceAccount'ı sadece kendi namespace'inde pod oluşturup silebilir.
- **Veritabanı chart'ın içinde yoktur.** PostgreSQL dışarıda durur ve DSN referansıyla verilir.
- **Dağıtım:** Container imajı ve chart Nexus'ta tutulur (Docker registry ve Helm hosted repo).

## 14. Açık Konular

- [ ] **`Operation` kind'ı:** Desteklenecek aksiyon listesi ve onay kuralları.
- [ ] **Çoklu vCenter / site:** Bir kontratın birden fazla site'a yayılması isteniyor mu?
- [ ] **SeaweedFS object lock** desteğinin doğrulanması.
- [ ] **PostgreSQL minimum sürümü:** Kullanılan kütüphanelerin (River, pgx) gereksinimlerine göre netleştirilecek.
- [ ] **Warpgate RDP** desteğinin doğrulanması. Desteğin durumu Warpgate sürüm notlarından teyit edilmeli.

## 15. Karar Kaydı

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
| 2026-10-23 | Üretilen objeler bot PR'ı ile Git'e yazılır |
| 2026-10-23 | Arayüz sadece görüntüleme ve debug için; OIDC + local; MFA veya passkey zorunlu; CLI tarayıcıda login olup süreli token alır |
| 2026-10-23 | Yetkisiz kontratlar diyagramlarda adıyla görünür, detayı kapalıdır |
| 2026-10-23 | Sunucu kimliği hostname değil, değişmez UID. Hostname tekrar kullanılabilir; çakışma kontrolü planda yapılır |
| 2026-10-23 | Silme her zaman açıkça istenir: `retire`, `lifecycle.state: decommissioned`; kontrat dosyasını silmek kaynak silmez |
| 2026-10-23 | Decommission'da bekleme süresi: prod 7 gün, test 1 gün; IP karantinası 14 gün; prod'da silme için owner + platform-admin onayı |
| 2026-10-23 | Zabbix'te geçmiş saklanmaz: host silinince geçmişiyle birlikte silinir |
| 2026-10-23 | Decommission tamamlanınca kontrat dosyası bot PR ile silinir |
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
| 2026-10-23 | Drift düzeltme: kayıtlar her yerde otomatik; konfigürasyon prod dışında otomatik, prod'da PR; provisioning her zaman PR (`Operation reconcile`) |
| 2026-10-23 | Roller check-mode uyumlu yazılır; uyumsuzlar `checkMode: unsupported` ile işaretlenir ve drift sonucu "bilinmiyor" olur |
| 2026-10-23 | Drift bildirimleri Zabbix trapper üzerinden |
