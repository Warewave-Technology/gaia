# PoC Planı

> Hedef: Tek bir Linux VM'i Proxmox üzerinde, bir kontrattan uçtan uca açmak ve kapatmak.
> Tasarım: [design.md](design.md). Örnek katalog ve kontrat: [examples/lab](../examples/lab).

## Sınanacak varsayımlar

1. **`proxmoxve` Pulumi provider'ı:** Şablondan klonlama, cloud-init ile IP verme, disk büyütme, tag ve not yazma güvenilir çalışıyor mu?
2. **Pulumi'nin Gaia içinde çalışması:** Automation API (Go), state SeaweedFS'te (S3), secret'lar OpenBao transit ile; birlikte düzgün çalışıyor mu?
3. **Ansible:** ansible-runner podman'daki EE içinde çalışırken JSON olay akışı Go'dan okunabiliyor mu? Envanterde UID ve secret maskeleme beklendiği gibi oluyor mu?
4. **Katalog motoru:** Graf, referans çözümleme ve CEL (isimlendirme, kurallar) pratikte kullanışlı mı?
5. **Kimlik ve yaşam döngüsü:** UID, isim kiralama ve run kaydı (hash zinciri) tutarlı mı? Aynı kontrat ikinci kez uygulandığında değişiklik üretmiyor mu?

## Başarı kriterleri

1. `gaia plan` katalogdan çözümlenmiş planı gösterir ve hiçbir sisteme dokunmaz.
2. `gaia apply` şunları yapar:
   - Proxmox'ta şablondan VM açar.
   - IP'yi IPAM'dan alır, hostname'i Policy'den üretir, UID'yi VM'e yazar.
   - State'i SeaweedFS'e kaydeder, credential'ları OpenBao'dan çeker.
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
| **M1 Katalog ve plan** | Repo iskeleti, kind şemaları, YAML yükleyici, referans grafı, CEL, `gaia plan` | – | Sürüyor: iskelet, obje modeli ve yükleyici hazır |
| **M2 Provisioning** | IPAM v0, isimlendirme, UID ve isim kiralama, Pulumi + `proxmoxve`, state, credential'lar, `gaia apply` | M0, M1 | – |
| **M3 Konfigürasyon** | EE imajı, ansible-runner sarmalayıcısı, baseline rolü, olayların DB'ye yazılması, maskeleme | M2 | – |
| **M4 Kayıt ve kapanış** | Zabbix provider'ı, run kaydı ve hash zinciri, `replicas: 0` ile decommission | M2, M3 | – |
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
4. **Graf:** Kontrattan kataloğa (`network → Network` vb.) ve katalog içindeki (`Network.site → Site → Provider`) referansları çözer. Hatalar tek seferde, dosya ve satır bilgisiyle raporlanır.
5. **CEL:** Policy `naming` ve `pad`; ifadeler yüklemede derlenir.
6. **`gaia validate` ve `gaia plan`:** `plan` çözümlemeyi ve yapılacak işleri gösterir, hiçbir sisteme dokunmaz. Sistemleri okumak (IPAM'da boş IP, Proxmox'ta mevcut VM) M2'nin işi.

## Kararlar

- Tetikleme CLI'dan, yerel Git checkout'undan; PR ve webhook akışı ek maddede.
- Platform bağımlılıkları tek VM'de podman compose ile.
- IPAM v0 bu repoda, ayrı binary olarak (`cmd/gaia-ipam`).
- Şablon Rocky Linux 9. GenericCloud imajında qemu-guest-agent kurulu geliyor.
- Envanter kararını sınamak için Zabbix PoC'de var.
