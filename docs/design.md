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
- **Kimlik:** Bir objenin kimliği `kind + name` ikilisidir ve global olarak tekildir. Aynı ikiliden iki tane varsa yükleme hata verir. Bunun tek istisnası bölüm 5.4'teki patch objeleri.
- **Sahiplik:** Kimin hangi dizini onaylayacağı CODEOWNERS ile belirlenir.

## 3. Kind Listesi

| Kind | Grup | Elle / üretilen | Not |
|---|---|---|---|
| `Platform` | Sistem | Elle | Tekil. Engine ayarları |
| `AuthConfig` | Sistem | Elle | Kimlik doğrulama, bkz. bölüm 10 |
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

Tanımlı provider tipleri: `openbao`, `vsphere`, `ipam`, `activedirectory`, `zabbix`, `warpgate`, `nexus`, `s3` (SeaweedFS), `runner` (Semaphore/AWX), `git`.

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
    configuration: { tool: ansible, runner: runner, source: config-repo }
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
    version: { type: str, required: true }
    heapMiB: { type: int, default: 2048 }
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
    extra: meta/gaia.yml                                 # opsiyonel: monitoring, conflicts…
  output: generated/ansible-roles/
  trigger: { onCommit: true }
```

- **İsimlendirme:** Roller birden çok collection'a dağıldığında aynı isimli roller çıkar. Bu yüzden isim `collection.role` biçiminde.
- **`meta/gaia.yml`:** Gaia'ya özgü alanlar (`monitoring`, `conflicts`) rolün yanında tutulur, böylece rol ve metadata'sı aynı PR'da birlikte değişir.

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

Kontrat Ansible'a serbest `vars` geçemez. Her katalog öğesi kendi `params` şemasını tanımlar ve kontrat sadece bu şemadaki parametreleri geçebilir.

- `AnsibleRole`'de şema `argument_specs` dosyasından üretilir.
- `App`'te şema elle yazılır.
- Şemada olmayan bir parametre veya tipi yanlış bir değer validasyonda reddedilir.

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
  action: patch                      # patch | snapshot | revert | power | …
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

## 9. Audit

İş Git'e dayandığı için iki katman yeterli bulundu.

### 9.1 Katman 1: Değiştirilemez run kaydı

```yaml
run: run-20261104-0042
requestedBy: yigit.basalma               # PR yazarı
approvedBy: [ayse.k]                     # PR onaylayanlar
source: { repo: platform/contracts, commit: 3f9c2e1, signed: true }
catalogCommit: 9a1b0c4
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

### 9.2 Katman 4: Servis hesaplarını insanlar kullanamasın

Bir servis hesabıyla yapılmış işlem, ancak o hesabı sadece Gaia'nın kullanabildiği kanıtlanabiliyorsa kanıt değeri taşır.

- Credential'lar sadece OpenBao'da durur ve onlara sadece Gaia'nın policy'si erişebilir.
- AD'de bu hesaplara interaktif oturum açma GPO ile yasaklanır.
- Hesap Gaia'nın IP'si dışından kullanılırsa Zabbix/SIEM'de alarm üretilir.

## 10. Arayüz ve CLI

### 10.1 Prensip

Arayüz ve CLI aynı API'nin istemcileridir; ikisinin yetkisi aynıdır. İnsan token'larıyla okuma ve `plan` yapılabilir, apply yapılamaz. Git'in tetiklediği apply işleri ayrı bir makine kimliğiyle (CI'ın OIDC token'ı, workload identity) çalışır.

Arayüzün amacı diyagramları görmek, logları takip etmek ve hataları debug etmektir.

### 10.2 Kimlik doğrulama

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

### 10.3 CLI login

```
$ gaia login                      # loopback redirect + PKCE (RFC 8252)
$ gaia login --device             # device authorization grant (RFC 8628), tarayıcısız makineler için
```

- **Login sonrası:** CLI tarayıcıyı açar, kullanıcı giriş yapar ve CLI **süreli bir token** alır.
- **Device akışı:** Phishing'e açık olduğu için onay ekranında isteğin geldiği makine ve IP gösterilir, kullanıcı açıkça onaylar.
- **Token'lar:** Gaia'nın kendi opak token'larıdır, IdP token'ı CLI'a verilmez. Sunucu sadece hash'lerini tutar ve token'lar anında iptal edilebilir. CLI tarafında OS keychain'de saklanır.
- **Oturumlar:** "Oturumlarım" ekranında aktif UI ve CLI oturumları görülüp tek tek kapatılabilir.

### 10.4 Yetkiler ve görünürlük

Roller OIDC grup claim'inden türetilir.

| Rol | Görebildikleri |
|---|---|
| `viewer` | Kendi takımının kontratları (`labels.owner`) |
| `operator` | Tüm kontratlar, loglar, `plan` |
| `platform-admin` | Ek olarak provider sağlığı, oturum yönetimi, kullanıcılar |

**Görünürlük kuralı:** Bir kullanıcının yetkisi olmayan bir kontrat, ilişki diyagramlarında veya "kullanan kontratlar" listelerinde (ör. aynı ağı kullananlar) **adıyla görünür, detayı kapalıdır.** YAML'ı, sunucuları, logları ve run kayıtları açılmaz.

### 10.5 Ekranlar

1. **Genel bakış.** Son run'lar, başarısız olanlar, drift sayıları, bekleyen bot PR'ları ve süresi dolmak üzere olan istisnalar.
2. **Kontratlar.** YAML ve çözümlenmiş hali, ilişki diyagramı (kontrat → katalog objeleri → provider'lar → gerçek kaynaklar), sunucular ve yönetim seviyeleri.
3. **Run'lar.** Aşama zaman çizelgesi, host × task matrisi, canlı log, Pulumi diff ve run kaydı. Hata ilgili provider'a ve onun credential referansına bağlanır.
4. **Katalog.** Kind'a göre gezinme ve label filtreleri. Her obje için nereden üretildiği, patch'leri ve onu kullanan kontratlar.
5. **Provider'lar.** Bağlantı durumu, son başarılı çağrı, credential'ın geçerlilik durumu.
6. **Sync.** Source'ların son sync zamanı, CEL hataları ve açık bot PR'ları.
7. **Keşif.** Adopt raporları ve eşleşmeyen kaynaklar.

### 10.6 Loglar

- **Ansible:** Kendi callback plugin'imiz her task ve host sonucunu yapısal bir olay olarak Gaia'ya gönderir. Host × task matrisi bu olaylardan oluşur.
- **Pulumi:** Automation API'nin event stream'i kullanılır.
- **Maskeleme:** Run sırasında OpenBao'dan alınan her değer loglarda maskelenir. Ansible'daki `no_log` ayarına güvenilmez.
- **Saklama:** Canlı loglar SSE ile akar, run bitince SeaweedFS'e arşivlenir.

### 10.7 Teknoloji

- **Backend:** Go. API, SSE ve statik dosyalar tek binary'de; frontend `go:embed` ile içine gömülür.
- **Frontend:** React + TypeScript. Diyagramlar için React Flow ve otomatik yerleşim için ELK.js.
- **Kütüphaneler:** `go-webauthn/webauthn` (passkey), `coreos/go-oidc` (OIDC), `google/cel-go` (CEL), Pulumi Automation API (Go).

## 11. Açık Konular

- [ ] **Silme semantiği:** `replicas` azaltıldığında hangi sunucu silinir (en yüksek sıra numaralı mı)? Kontrat dosyası silinirse ne olur?
- [ ] **Gaia'nın kendi veritabanı:** Run kayıtları, oturumlar, sunucu–IP eşleşmeleri. PostgreSQL mi?
- [ ] **Runner seçimi:** Semaphore UI mı, AWX mı? AWX'in sürümleri 2024'ten beri duraklatılmış durumda; bu yüzden Semaphore UI değerlendiriliyor.
- [ ] **`params` şema formatı:** App için de Ansible `argument_specs` formatını kullanmak her yerde tek format sağlar. Bu yeterli mi?
- [ ] **Drift kontrolü:** Ne sıklıkla yapılacak? Sonuç arayüzde nasıl gösterilecek?
- [ ] **`Operation` kind'ı:** Desteklenecek aksiyon listesi ve onay kuralları.
- [ ] **Çoklu vCenter / site:** Bir kontratın birden fazla site'a yayılması isteniyor mu?
- [ ] **SeaweedFS object lock** desteğinin doğrulanması.
- [ ] **Warpgate RDP** desteğinin doğrulanması. Desteğin durumu Warpgate sürüm notlarından teyit edilmeli.

## 12. Karar Kaydı

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
