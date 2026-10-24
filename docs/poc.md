# PoC Planı

> Hedef: Tek bir Linux VM'i Proxmox üzerinde, bir kontrattan uçtan uca açmak ve kapatmak.
> Tasarım: [design.md](design.md). Örnek katalog ve kontrat: [examples/lab](../examples/lab).

## Sonuç (2026-10-24)

PoC tamamlandı. Bir kontrattan Proxmox'ta VM açıldı, Zabbix'e kaydedildi, baseline'ı uygulandı, kayıt altına alındı ve `replicas: 0` ile kapatıldı.

| # | Kriter | Durum |
|---|---|---|
| 1 | `gaia plan` çözümlenmiş planı gösterir, hiçbir sisteme dokunmaz | Karşılandı (M1) |
| 2 | `gaia apply`: şablondan VM, IPAM'dan IP, Policy'den hostname, UID'nin VM'e yazılması, state, OpenBao'dan credential'lar | Karşılandı, bir sapmayla: state SeaweedFS yerine PostgreSQL'de (M2.5) |
| 3 | Baseline EE içinde ansible-runner ile; olaylar UID ile, secret'lar maskeli | Karşılandı (M3) |
| 4 | Zabbix'e doğru tag ve host group ile kayıt | Karşılandı (M4.1) |
| 5 | Run kaydı: commit, EE digest'i, plan hash'i, hash zinciri | Karşılandı (M4.2) |
| 6 | İkinci apply değişiklik yapmaz | Gaia, Zabbix ve Ansible için karşılandı. Pulumi her run'da diske etkisiz bir update gönderiyor (provider uyumsuzluğu, bkz. M2.5) |
| 7 | `replicas: 0`: VM silinir, IP serbest kalır, Zabbix host'u silinir, isim kiralaması biter | Karşılandı (M4.3) |

**Varsayımlar:**
1. **`proxmoxve`:** Klon, cloud-init, disk büyütme, tag ve not çalışıyor. İki pürüz çıktı:
   - Klon sırasında `Sys.Console` yetkisi gerekiyor.
   - Disk `speed` varsayılanı her run'da fark üretiyor.
2. **Pulumi Gaia içinde:** Automation API, OpenBao transit ve uzak state birlikte çalışıyor. Gerekenler:
   - CLI sürümü sabitlenmeli.
   - Stack ayarları Gaia'nın veritabanında tutulmalı.
   - State S3 yerine PostgreSQL'de durmalı (SeaweedFS'in liste davranışı).
3. **Ansible:** JSON olay akışı Go'dan okunuyor; UID envanteri ve maskeleme beklendiği gibi. Rootless podman'da `--userns keep-id` gerekiyor.
4. **Katalog motoru:** Graf, referans çözümleme ve CEL kullanışlı. CEL, isimlendirmenin yanında Zabbix host group'larında da kullanıldı.
5. **Kimlik ve yaşam döngüsü:** UID, isim kiralama ve hash zinciri tutarlı. Silinen bir sunucunun ismi ve VMID'i tekrar kullanılabiliyor, IP'si karantinada.

**Açık kalanlar (PoC dışı):**
- Onay akışı ve PR/webhook tetiklemesi
- Uzak config reposu
- Host key yönetimi
- `no_log` parametreli roller
- Zabbix ajanı (Agent kind'ı)
- Decommission bekleme süreleri
- `proxmoxve` disk `speed` sorununu upstream'e bildirmek

## Sınanacak varsayımlar

1. **`proxmoxve` Pulumi provider'ı:** Şablondan klonlama, cloud-init ile IP verme, disk büyütme, tag ve not yazma güvenilir çalışıyor mu?
2. **Pulumi'nin Gaia içinde çalışması:** Automation API (Go), state uzak bir backend'de, secret'lar OpenBao transit ile; birlikte düzgün çalışıyor mu? (State SeaweedFS'te planlanmıştı; PostgreSQL'e taşındı, bkz. M2.5.)
3. **Ansible:** ansible-runner podman'daki EE içinde çalışırken JSON olay akışı Go'dan okunabiliyor mu? Envanterde UID ve secret maskeleme beklendiği gibi oluyor mu?
4. **Katalog motoru:** Graf, referans çözümleme ve CEL (isimlendirme, kurallar) pratikte kullanışlı mı?
5. **Kimlik ve yaşam döngüsü:** UID, isim kiralama ve run kaydı (hash zinciri) tutarlı mı? Aynı kontrat ikinci kez uygulandığında değişiklik üretmiyor mu?

## Başarı kriterleri

1. `gaia plan` katalogdan çözümlenmiş planı gösterir ve hiçbir sisteme dokunmaz.
2. `gaia apply` şunları yapar:
   - Proxmox'ta şablondan VM açar.
   - IP'yi IPAM'dan alır, hostname'i Policy'den üretir, UID'yi VM'e yazar.
   - State'i kaydeder (PostgreSQL), credential'ları OpenBao'dan çeker.
3. Baseline rolü ansible-runner ile EE içinde çalışır. Olaylar `run_events` tablosuna UID ile yazılır, secret'lar maskelenir.
4. Sunucu Zabbix'e doğru tag ve host group ile kaydedilir.
5. Run kaydı yazılır: commit, EE digest'i, plan hash'i ve hash zinciri.
6. Aynı kontrat ikinci kez `gaia apply` ile uygulandığında hiçbir değişiklik olmaz.
7. `replicas: 0` yapılınca VM silinir, IP serbest kalır, Zabbix host'u silinir ve isim kiralaması biter. PoC'de bekleme süresi uygulanmaz.

## Kapsam

| PoC'de var | PoC'de yok |
|---|---|
| Katalog yükleme, validasyon, referans grafı, CEL | NetworkSource, AnsibleRoleSource, TeamSource, bot PR'ları |
| Kind'lar: Provider, Platform, Site, Network, Image, Size, Baseline, AnsibleRole (elle), Policy, Server | Operation, OperationSchedule, Team, Group |
| Proxmox compute provider'ı | vSphere |
| IPAM v0 (`gaia-ipam`): havuz, ayırma, serbest bırakma | DNS, AD, Warpgate, ajanlar |
| Zabbix kaydı | Drift, adopt, keşif, reconcile |
| ansible-runner + EE (podman) | Site worker'ı |
| PostgreSQL: hosts, name_leases, runs, run_events, run_records | River kuyruğu (PoC'de işler senkron) |
| CLI: `gaia plan`, `gaia apply` (yerel Git checkout'u, belirli bir commit) | Arayüz, kimlik doğrulama, PR/webhook akışı, `gaia/approval` |

- **Git kuralı PoC'de de geçerli.** CLI kontratı ve katalogu dosya sisteminden değil, yerel bir Git reposunun belirli bir commit'inden okur.
- **Owner kontrolü yok.** `labels.owner` alanının bir Team referansı olduğu kontrol edilmez.

## Lab

Proxmox lab'i bu reponun dışında kuruluyor.

| | |
|---|---|
| Lab ağı | SDN VNet `gaiapoc`, 10.50.0.0/24, gateway 10.50.0.1 |
| Gaia'nın IP aralığı | 10.50.0.100–199 |
| Pool | `gaia`; Gaia'nın token'ı `gaia@pve!poc` |
| Şablon | Rocky 9, VMID 9300 |
| Platform VM'i | 192.168.1.25 / 10.50.0.10: PostgreSQL 17, OpenBao 2.7.1, SeaweedFS 4.48, Zabbix 7.0 |

- **Credential'lar:** OpenBao'da, `kv/platform/{proxmox/lab, seaweedfs, postgres, zabbix}` yollarında.
- **Pulumi secret'ları:** `transit/keys/pulumi` anahtarıyla.

## Adımlar

| Adım | İçerik | Bağımlılık | Durum |
|---|---|---|---|
| **M0 Ortam** | Lab ağı, pool, Gaia kullanıcısı, şablon, platform VM'i ve servisleri | – | Tamam |
| **M1 Katalog ve plan** | Repo iskeleti, kind şemaları, YAML yükleyici, referans grafı, CEL, `gaia plan` | – | Tamam |
| **M2 Provisioning** | IPAM v0, isimlendirme, UID ve isim kiralama, Pulumi + `proxmoxve`, state, credential'lar, `gaia apply` | M0, M1 | **Tamam:** lab'de `gaia apply` ile uçtan uca VM açıldı |
| **M3 Konfigürasyon** | EE imajı, ansible-runner sarmalayıcısı, baseline rolü, olayların DB'ye yazılması, maskeleme | M2 | **Tamam:** baseline lab'de EE içinde çalıştı |
| **M4 Kayıt ve kapanış** | Zabbix provider'ı, run kaydı ve hash zinciri, `replicas: 0` ile decommission | M2, M3 | **Tamam:** Zabbix kaydı, run kaydı ve decommission lab'de çalıştı |
| **Ek** | Provisioning drift'i, `gaia reconcile`, `topology` ile ikinci VM, Forgejo/Gitea üzerinden PR ve plan yorumu | M4 | – |

## M1'in iş kırılımı

1. **Repo iskeleti:** Go modülü, `cmd/gaia` ve `cmd/gaia-ipam`, paket sınırları, örnek katalog, EE tanımı, CI. **Tamam.**
2. **Obje modeli:** `apiVersion` / `kind` / `metadata` / `spec`; PoC kind'larının Go tipleri ve şemaları. **Tamam** (`internal/catalog`).
   - **Katı çözümleme:** Bilinmeyen alanlar, eksik zorunlu alanlar ve tip hataları dosya:satır:sütun ve alan yoluyla raporlanır (ör. `spec.attachment: unknown field "vnett"`).
   - **Kind kuralları:** Örneğin Server'da `network` ya da `topology`'den sadece biri olabilir; Network'te tek bir `attachment` biçimi kullanılır; parametrelerde serbest tiplere izin verilmez (6.2).
   - **Patch objeleri:** Henüz desteklenmiyor. Üretilen objeler gelince eklenecek.
3. **Yükleyici:** Bir Git commit'indeki dosyaları okur. Çoklu doküman (`---`) desteklenir; kimlik `kind + name` ve tekildir. **Tamam** (`internal/gitsrc`, go-git).
   - **Çalışma dizini okunmaz:** Commit'lenmemiş değişiklikler yükleyiciye girmez.
   - **Tüm hatalar tek seferde:** Bir yükleme bütün sorunları birlikte raporlar.
   - **`gaia validate --repo . --ref HEAD --path <dizin>`:** Bugünkü hâliyle yükleme ve obje kontrollerini yapar; referans kontrolü 4. adımla eklenecek.
4. **Graf:** Kontrattan kataloğa (`network → Network` vb.) ve katalog içindeki (`Network.site → Site → Provider`) referansları çözer. Hatalar tek seferde, dosya ve satır bilgisiyle raporlanır. **Tamam** (`catalog.Resolve`).
   - **Referansın varlığı:** Bulunamayan bir referans için en yakın ad önerilir (`did you mean "small"?`). Graf ileri ve geri kenarları tutar; geri kenarlar arayüzdeki "kullanan kontratlar" için.
   - **Provider tipi:** Referans verilen provider alanla uyumlu olmalı (`state` → s3, `secrets` → openbao, `source` → git, `compute` → proxmox ya da vsphere).
   - **Hipervizöre özgü alanlar:** Proxmox'ta `nodes`, vSphere'de `datacenter` + `cluster` zorunlu. Ağ bağlantısı site'ın hipervizörüne uygun olmalı. vSphere şablonları adla verilir.
   - **Kontrat kuralları:**
     - İmajın, kontratın her site'ı için bir şablonu olmalı.
     - Ortam Network'te ve Policy'de tanımlı olmalı; Policy'deki zorunlu etiketler bulunmalı.
     - Baseline ve rollerin OS'i imajla uyumlu olmalı.
     - Rol çakışmalarına izin verilmez.
     - Rol parametrelerinin adları rolün şemasında olmalı; varsayılanı olmayan zorunlu parametreler verilmeli. Değer kontrolleri CEL adımıyla gelecek.
   - **Tekil objeler:** Tam bir Platform olmalı; en fazla bir Policy olabilir.
   - **`gaia validate`:** Yüklemeden sonra grafı da kontrol eder.
5. **CEL:** Policy `naming` ve `pad`; ifadeler yüklemede derlenir. **Tamam** (`internal/cel`, cel-go v0.32, modül yolu artık `cel.dev/cel-go`).
   - **İsimlendirme:** `naming` ifadesi `site`, `env`, `app`, `contract`, `labels` ve `seq` değerlerini görür. Policy'de yoksa varsayılan `contract + "-" + pad(seq, 2)` kullanılır. Grafta her kontrat ve site için örnek bir hostname (`seq` 1) üretilip geçerliliği kontrol edilir; `Graph.Hostname` plan adımında kullanılacak.
   - **Parametre değerleri:** Tip, liste elemanları, iç içe `options`, `choices`, `min`/`max` (sayı ya da uzunluk), `pattern`, `format` (semver, ipv4, cidr, hostname, fqdn, duration) ve `secret` (sadece `bao://`) kontrol edilir. Şema da yüklemede kontrol edilir: pattern derleniyor mu, format biliniyor mu, `min` ≤ `max` mı, varsayılan değer şemaya uyuyor mu.
   - **Rol kuralları:** `rules` yüklemede derlenir. Her kontratta varsayılanlar uygulanmış parametrelerle ve her ağ için çalıştırılır; `false` sonuç kuralın mesajıyla raporlanır.
   - **Ortam ayarları:** Ortak ortamda strings uzantısı (`substring`, `lowerAscii`, `replace`, `format`…) ve sayısal tipler arası karşılaştırma açık; değerlendirme maliyeti sınırlı.
   - **Hata biçimi:** CEL derleme hataları tek satır ve sütun numarasıyla verilir.
6. **`gaia validate` ve `gaia plan`:** `plan` çözümlemeyi ve yapılacak işleri gösterir, hiçbir sisteme dokunmaz. Sistemleri okumak (IPAM'da boş IP, Proxmox'ta mevcut VM) M2'nin işi. **Tamam** (`internal/plan`).
   - **Kontrat başına plan:** Sunucular hostname, `seq`, site, ağ, compute (provider, tip, şablon, node, pool, storage, ağ bağlantısı) ve IPAM havuzuyla listelenir. Ardından baseline ve roller gelir; rol parametreleri varsayılanları uygulanmış olarak gösterilir, `no_log` olanlar maskelenir. Policy'ye göre onay gerekip gerekmediği de yazılır.
   - **Kontrol:** Kontratlar arasında hostname çakışması plan hatasıdır.
   - **`decommissioned` kontratlar:** Sunucu planlanmaz. Silme planı M2'de, uygulanması M4.3'te geldi.
   - **Plan hash'i:** Kanonik JSON'un SHA-256'sı. Aynı girdi aynı hash'i, farklı girdi farklı hash'i verir. (2026-10-25'ten beri commit etiketi hash'e girmiyor; bkz. design 7.6.)
   - **Komut:** `gaia plan [kontrat...] --repo --ref --path [--json]`. `--json`'da stdout'a sadece plan yazılır.
   - **M1'de sistem durumu okunmaz:** Her sunucu `create` olarak görünür ve `seq` her site için 1'den başlar. M2'de mevcut sunucular, IP'ler ve isim kiralamaları okunacak.
   - **Başarı kriteri 1 karşılandı:** `gaia plan` çözümlenmiş planı gösteriyor ve hiçbir sisteme dokunmuyor.

## M2'nin iş kırılımı

1. **Store:** PostgreSQL bağlantısı, goose migration'ları, `internal/store` (pgx + sqlc). **Tamam.**
   - **Tablolar:** `hosts` (UID, kontrat, hostname, site, `seq`, durum), `host_external_ids`, `name_leases` ve `runs`.
   - **Sunucu kaydı:** Sunucu ve hostname kiralaması tek transaction'da yazılır. Kiralama çakışması `NameTakenError` olur; aynı kontrat ve site'ta `seq` tekildir. Silinen sunucunun ismi ve `seq`'i tekrar kullanılabilir.
   - **Kontrat kilidi:** Transaction seviyesinde advisory lock. İkinci bir run beklemeden `ErrLocked` alır.
   - **`gaia migrate --dsn` (`$GAIA_DSN`):** Lab veritabanına uygulandı.
   - **Testler:** Entegrasyon testleri Docker'da PostgreSQL 17 ile `GAIA_INTEGRATION=1` altında çalışır (`make itest`). CI'da da açık.
2. **IPAM v0:** `gaia-ipam`: havuz, IP ayırma, serbest bırakma; kendi veritabanı. Gaia tarafında provider istemcisi. **Tamam** (`internal/ipam`, `internal/provider/ipam`).
   - **IP ayırma:** Havuz satırı kilitlenir, aralıktaki en küçük boş adres verilir. Ağ adresi, broadcast, gateway, kullanımdaki ve karantinadaki adresler atlanır. Aynı sahip (UID) için tekrarlanan çağrı aynı adresi döndürür. Eşzamanlı çağrılar çakışmaz.
   - **Serbest bırakma:** Bırakılan adres karantinaya girer (varsayılan 14 gün, `--quarantine`).
   - **API:** HTTP + JSON, bearer token; `/healthz` token istemez.
   - **Komutlar:** `gaia-ipam serve | migrate | pool set CIDR --gateway --range start-end`.
3. **Duruma bakan plan:** Mevcut sunucular veritabanından okunur: var olanlar `keep`, eksikler `create` olur. UID üretilir, en küçük boş `seq` verilir. **Tamam** (`plan.State`, `apply.StoreState`).
   - **Mevcut sunucular:** Ağ ve site bazında tutulur (`keep`).
   - **Silinenler (`delete`):** Ölçek küçültmede `seq`'i en yüksek olan ya da `retire` listesindeki sunucu; artık kullanılmayan ağdaki sunucular; `decommissioned` kontratın bütün sunucuları.
   - **Yeni sunucular:** Site'taki en küçük boş `seq` verilir. Silinecek sunucular `seq`'lerini ve isimlerini silinene kadar tutar. Üretilen isim başka bir sunucuda kiralıysa bir sonraki numaraya geçilir. UID planda değil, apply sırasında üretilir; böylece aynı durum aynı hash'i verir.
   - **Metin çıktısı:** `+` create, `=` keep, `-` delete; özet sistem başına `+N =N` ve `-N` gösterir. Değişiklik yoksa "no changes to servers" yazar.
   - **Veritabanı:** `gaia plan --dsn` (ya da `$GAIA_DSN`) durumu veritabanından okur. Veritabanı verilmezse her sunucu yeni sayılır ve bu açıkça yazılır.
   - **Şema:** `hosts` tablosuna `network` eklendi (migration 0002).
4. **Provisioning:** OpenBao'dan credential okuma; Pulumi Automation API + `proxmoxve`, state PostgreSQL'de. **Tamam** (`internal/engine/pulumi`, `internal/provider/{openbao,proxmox}`). Gerçek ortamda deneme 5. adımda.
   - **OpenBao istemcisi:** `bao://<mount>/<path>` referanslarını KV v2'den okur.
   - **Proxmox istemcisi:** Kümede kullanılan VMID'leri okur. Gaia, Site'taki `compute.vmids` aralığında en küçük boş VMID'i seçer.
   - **Pulumi motoru:** Her kontrat ve site için bir stack açılır (`gaia` projesi); state PostgreSQL'de (`pulumi` veritabanı), secret'lar `hashivault://pulumi` ile şifreli tutulur. Proxmox credential'ı state'e secret olarak girer. Her sunucu `vm-<UID>` adlı bir `VmLegacy` kaynağıdır: şablondan tam klon, CPU, bellek, diskler, ağ bağlantısı, cloud-init (IP, gateway, DNS, kullanıcı ve SSH anahtarı), agent, tag'ler, açıklama ve SMBIOS UUID. Yeni `Vm` kaynağı kendi dokümantasyonunda deneysel olarak işaretli olduğu için kullanılmadı. Plugin sürümü SDK ile aynı (v8.6.0).
   - **Katalog:** Site'a `compute.vmids` eklendi (Proxmox'ta zorunlu); Platform'a `engines.configuration.sshKey` eklendi (`bao://`).
5. **`gaia apply`:** Bu adımları sırayla çalıştırır; lab'de uçtan uca bir VM açılır, ikinci apply değişiklik yapmaz. **Tamam** (`internal/apply`).
   - **Akış:** Planı yazdırır; `--yes` yoksa durur. Her kontrat kilit altında işlenir. Sırası: sunucu kaydı ve isim kiralaması, IPAM'dan IP, Proxmox'tan boş VMID, SMBIOS UUID, `pulumi up`. Her adımın sonucu `host_external_ids`'e yazılır (`ip`, `vm`, `smbios`).
   - **Doğrulama:** `pulumi up` önce refresh yapar. Apply, VM'in VMID'ini ve guest agent'ın planlanan IP'yi bildirdiğini doğrulamadan sunucuyu `active` yapmaz. Yarım kalan bir klon (cloud-init diski olmadan) bu yüzden başarılı sayılmadı.
   - **Silme reddedilir:** `delete` içeren plan M4'e kadar uygulanmadı (M4.3).
   - **Pulumi state'i PostgreSQL'de:** SeaweedFS boşalan dizinleri listede 0 baytlık obje olarak döndürüyor ve Pulumi'nin kilit kontrolü `unexpected end of JSON input` ile düşüyor. State Pulumi'nin `postgres://` DIY backend'ine taşındı (`state` provider'ı `type: postgres`, kullanıcı bilgisi `bao://kv/platform/pulumi-state`). SeaweedFS'te sadece run kayıtları kalır.
   - **Stack ayarları:** Automation API her run'da geçici bir çalışma dizini açtığı için şifreli anahtar kayboluyordu. Ayarlar `pulumi_stacks` tablosunda tutulur (migration 0003).
   - **Pulumi CLI sabit:** 3.272.0, SDK ile aynı. Gaia ilk çalıştırmada indirir (kullanıcının cache dizininde `gaia/`; Linux'ta `~/.cache`, macOS'ta `~/Library/Caches`). Eski CLI'lar S3 URL parametrelerini tanımıyordu.
   - **Proxmox yetkisi:** Klon sırasında provider seri konsolu okuduğu için `gaia@pve`'ye `/` üzerinde `Sys.Console` gerekiyor (lab'de `GaiaConsole` rolü).
   - **Lab sonucu:** `labtpoc01` (VM 9400, 10.50.0.100/24) açıldı. SMBIOS UUID ve tag'ler veritabanıyla aynı. Gaia'nın anahtarıyla platform VM üzerinden SSH ve `sudo` çalışıyor, internete çıkıyor.
   - **İkinci apply:** Gaia'nın planı `= labtpoc01`, "no changes to servers". Pulumi ise her run'da diske etkisiz bir update gönderiyor. Neden `proxmoxve` v8.6.0'daki (Terraform provider v0.112.0) bir uyumsuzluk: disk `speed` bloğunun varsayılanı sıfırlarla dolu, okumada ise blok hiç dönmüyor. `ignoreChanges` bunu bastırmıyor, çünkü fark girdiyle provider'ın kendi state'i arasında. VM konfigürasyonu değişmiyor. Upstream'e bildirilecek (design.md, Açık Konular).

## M3'ün iş kırılımı

1. **EE imajı ve baseline rolü.** **Tamam** (`ee/`, `ansible/`).
   - **EE:** ansible-builder v3, Rocky Linux 9 tabanı, Python 3.12, ansible-core 2.21, ansible-runner 2.4. Collection'lar sabit (`ansible.posix` 2.2.2, `community.general` 13.5.0). SSH için `openssh-clients` (`bindep.txt`).
   - **Lab'de imaj:** Platform VM'inde labadm kullanıcısıyla, rootless podman'da derlenir (`lab/ee.yml`): `localhost/gaia/ee-default:dev`. Nexus PoC'de yok.
   - **Config reposu:** Bu reponun `ansible/` dizini. `baseline/linux.yml` ve `gaia_linux_base` rolü: hostname, saat dilimi, chrony (Site'ın NTP'si), SSH sıkılaştırma, `/etc/ansible/facts.d/gaia.fact` (UID, kontrat, label'lar) ve motd. Gaia'nın verdiği değişkenler `ansible/README.md`'de.
2. **ansible-runner sarmalayıcısı ve configure aşaması.** **Tamam** (`internal/engine/ansible`, `internal/apply/configure.go`).
   - **Çalışma dizini:** Her iş için tmpfs'te (`/dev/shm`) açılır: envanter, `env/ssh_key`, `env/cmdline` (`--forks`) ve proje. Proje config reposunun ilgili commit'inden çıkarılır; Gaia içine bir `.gaia/site.yml` yazar (baseline'ı import eder, sonra kontratın rolleri). İş bitince dizin silinir.
   - **Container:** `podman run --rm --userns keep-id ... ansible-runner run /runner -p .gaia/site.yml -j`. EE imajının kullanıcısı (1000) rootless podman'da çağıranın kullanıcısına eşlenir; yoksa `/runner`'a yazamaz. Root olarak çalışınca `--user 0`.
   - **Timeout ve iptal:** `limits.timeout` aşılırsa container `podman rm -f` ile kaldırılır, süreç grubu öldürülür.
   - **Olaylar:** JSON akışı satır satır okunur, her olay `run_events`'e yazılır (migration 0004): aşama, UID, task, `changed`, `failed`, stdout ve event_data. Event_data 64 KiB ile sınırlı.
   - **Maskeleme:** SSH anahtarı ve verilen secret'lar (tamamı ve çok satırlıların her uzun satırı) stdout'ta ve event_data'nın bütün string'lerinde `********` olur. Event_data önce çözülür, çünkü Python ASCII dışı karakterleri `\uXXXX` olarak yazıyor ve ham metinde eşleşme kaçabiliyor.
   - **Run'da kayıt:** Config reposunun commit'i ve EE imajının digest'li referansı (`runs.config_commit`, `runs.ee_image`). İş bu digest'le çalışır; Gaia run sırasında imaj çekmez.
   - **Hata:** Başarısız veya erişilemeyen bir sunucu run'ı başarısız yapar. Sunucu `active` kalır; bir sonraki apply konfigürasyonu tekrar dener.
   - **PoC kısıtları:** Config reposu yerel bir klondan okunur. Host key kontrolü kapalı (yeni sunucuların anahtarlarını Gaia henüz bilmiyor). `no_log` parametreli roller reddedilir. Olaylar tek tek yazılır.
   - **Lab sonucu:** `gaia apply` platform VM'inden çalıştı (podman ve lab ağı orada): provisioning, sonra baseline `labtpoc01`'de `ok=11`. İkinci apply'da `changed=0`. 72 olayın hiçbirinde anahtar yok; çalışma dizini ve container kalmadı.
   - **Başarı kriteri 3 karşılandı.**

## M4'ün iş kırılımı

1. **Zabbix provider'ı.** **Tamam** (`internal/provider/zabbix`, `internal/apply/monitor.go`).
   - **Katalog:** `Platform.monitoring`: provider, host group'ları (CEL, naming ile aynı değişkenler; boş sonuç grup eklemez) ve şablonlar (Image OS'una ve rollerin `monitoring` adlarına göre). Şablonu tanımlı olmayan bir `monitoring` adı plan hatasıdır.
   - **Plan:** Her sunucunun host group'ları planda; özet `monitoring +N =N (zabbix)` gösterir.
   - **Aşama:** Provisioning doğrulandıktan sonra, konfigürasyondan önce (tasarımdaki sıra: `pulumi → register → ansible`).
   - **Kayıt:** Host, kayıtlı `hostid` ile, yoksa `gaia.uid` tag'iyle bulunur; isimle eşleştirilmez. Kayıtlı ID başka bir UID'nin host'unu gösteriyorsa ona dokunulmaz. Eksik host group'ları oluşturulur; şablonlar var olmalı. Tag'ler: `gaia.uid`, `gaia.contract` ve label'lar. Site'ta `zabbixProxy` varsa host proxy üzerinden izlenir. Değişiklik yoksa `host.update` çağrılmaz. Host ID'si `host_external_ids`'e yazılır (`zabbix/host`).
   - **API:** Zabbix 7.0 JSON-RPC, API token'ı Bearer header'ında.
   - **Lab sonucu:** `labtpoc01` Zabbix'e dört host group, beş tag ve `ICMP Ping` şablonuyla kaydedildi. İkinci apply'da `same`. Zabbix VM'e ping atıyor (`icmpping=1`). Lab'de Zabbix ajanı yok; Agent kind'ı PoC dışında.
   - **Lab düzeltmeleri:** Zabbix server container'ına `NET_RAW` (fping için). EE derlemesinde galaxy'nin yanıt cache'i kapatıldı: ansible-core 2.21'de sabit sürümlü collection'lar "Missing expected 'results'" hatasına düşüyor.
   - **Başarı kriteri 4 karşılandı.**
2. **Run kaydı ve hash zinciri.** **Tamam** (`internal/runrecord`, migration 0005).
   - **İçerik:** Tasarım 10.1'deki alanlar:
     - `requestedBy`: PoC'de CLI'ı çalıştıran yerel hesap
     - `approvedBy`: PoC'de boş
     - kaynak repo ve commit; commit'in imzalı olup olmadığı
     - katalog commit'i, config reposu commit'i, EE digest'i
     - kontratlar, plan hash'i
     - `executedAs`: provider başına kimlik
     - zamanlar, sonuç, hata
     - `changes`: aşama başına, UID ile
   - **Zincir:** Kayıt run bitince bir kez yazılır, başarısız run'lar dahil. Hash'i, önceki kaydın hash'i ile gövdenin SHA-256'sı. Gövde, hash'lenen metin olarak saklanır. Yazarken tablo kilitlenir, böylece zincir dallanmaz.
   - **Değiştirilemezlik:** Trigger `UPDATE`, `DELETE` ve `TRUNCATE`'i reddeder. Tek istisna, arşiv adresinin bir kez yazılması. Trigger'ı kapatabilen biri gövdeyi değiştirse ya da bir kaydı silse bile `gaia record verify` kırılan yeri gösterir.
   - **Arşiv:** `Platform.runArchive`'daki S3 bucket'ı, COMPLIANCE modunda Object Lock ile. Saklama süresi provider'da: `retention`, lab'de `1d`. Kopya sıkıştırılmış JSON'dur ve gövdeyi baytı baytına taşır, böylece arşivdeki kopya tek başına doğrulanabilir. Anahtar: `records/<yıl>/<ay>/<run>.json`. Arşivleme başarısız olursa run hata döner; kayıt veritabanında kalır.
   - **Komutlar:**
     - `gaia record verify`: zinciri baştan sona kontrol eder
     - `gaia record show RUN`: bir run'ın kaydını gösterir
   - **Lab'de doğrulandı:** SeaweedFS COMPLIANCE kilitli bir versiyonun silinmesini reddediyor (`TestS3Lab`).
   - **Başarı kriteri 5 karşılandı.**
3. **Decommission.** **Tamam** (`internal/apply/decommission.go`).
   - **Tetik:** Planın `delete`'leri. Üç kaynağı var: ölçek küçültme (`replicas`), `retire` ve `lifecycle.state: decommissioned`. PoC'de bekleme süresi yok.
   - **Sıra:** Sunucu `decommissioning` olur. Önce Zabbix host'u geçmişiyle silinir, böylece kapanan VM alarm üretmez. Sonra `pulumi up` sadece kalan VM'lerle çalışır ve silinenleri yok eder; site'ta hiç sunucu kalmasa da stack çalışır. VM'in Proxmox'ta kalmadığı doğrulanır. IP IPAM'a geri verilir (14 gün karantina). En son host `deleted` olur; isim kiralaması biter ve dış sistem ID'leri silinir, çünkü VMID ve IP tekrar dağıtılabilir.
   - **Kimlikler:** Silinecek sunucunun VM'i, IP'si ve Zabbix host'u kayıtlı ID'lerinden bulunur, isimden ya da katalogdan değil.
   - **Onay:** Planı sunucu değiştiren ve onay isteyen (`approval: required`) kontratlar reddedilir; PoC'de onay akışı yok.
   - **Lab sonucu:**
     - `replicas` 1 → 2: `labtpoc02` açıldı, Zabbix'e kaydedildi, baseline `changed=8`.
     - 2 → 1: `labtpoc02` silindi: Zabbix host'u, VM 9401, IP 10.50.0.101 ve isim.
     - 1 → 0: `labtpoc01` aynı yoldan silindi.
     - Doğrudan kontrol: Proxmox'ta 9400–9499 aralığı boş, Zabbix'te `gaia.uid` tag'li host yok, IPAM'da iki adres karantinada, açık isim kiralaması ve dış ID kalmadı.
     - Dört run'ın kaydı arşivlendi; `gaia record verify` zinciri sağlam buldu.
   - **Başarı kriteri 7 karşılandı.**

## Kararlar

- Tetikleme CLI'dan, yerel Git checkout'undan; PR ve webhook akışı ek maddede.
- Platform bağımlılıkları tek VM'de podman compose ile.
- IPAM v0 bu repoda, ayrı binary olarak (`cmd/gaia-ipam`).
- Şablon Rocky Linux 9. GenericCloud imajında qemu-guest-agent kurulu geliyor.
- Envanter kararını sınamak için Zabbix PoC'de var.
- Lab'de `gaia apply` platform VM'inden, labadm kullanıcısıyla çalışır (`~/.config/gaia/env`); Mac'ten lab ağına doğrudan erişim yok.
