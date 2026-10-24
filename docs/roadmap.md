# Yol Haritası

> PoC'den sonra ürünün tamamı. Tasarım: [design.md](design.md). PoC: [poc.md](poc.md).
> Her faz kendi PR'larıyla ilerler; her PR'dan önce tüm testler ve lint çalışır.

## Sıra ve bağımlılıklar

| Faz | İçerik | Bağımlı olduğu | Durum |
|---|---|---|---|
| 1 | `gaia server`, API, iş kuyruğu | – | **Tamam** |
| 2 | Kimlik doğrulama ve yetki | 1 | **Tamam** |
| 3 | Change motoru ve GitHub izleyicisi | 1, 2 | **Tamam** (lab oturumu sonda) |
| 4 | Bildirimler | 3 | **Tamam** (lab oturumu sonda) |
| 5 | Arayüz | 1–4; tasarım taslağının onayı | – |
| 6 | Erişim yönetimi: Team, Group, AD | 2, 3 | **Tamam** (lab oturumu sonda) |
| 7 | Source'lar: NetworkSource, AnsibleRoleSource | 3 | – |
| 8 | Kontrat genişlemeleri: App, Agent, Operation | 3 | – |
| 9 | Provider'lar: vSphere, AD, DNS, Warpgate, Nexus | 1 | – |
| 10 | Yaşam döngüsü: drift, keşif, adopt, reconcile | 3, 8, 9 | – |
| 11 | Dağıtım: paketler, Ansible rolü, Helm | 1 | – |

Arayüzün ekranları tasarım taslağı onaylanınca yazılır. Taslak beklerken arka uç fazları ilerler.

## Test ortamı

- **Yerel:** Docker compose (`deploy/dev/`): PostgreSQL, OpenBao, SeaweedFS, Zabbix, Keycloak (OIDC) ve sahte compute. Fazların çoğu burada test edilir.
- **Proxmox lab'i:** Gerçek VM'lerle uçtan uca test gerektiğinde `lab/` ile kurulur, iş bitince kapatılır.
- **AD:** Lab'de Samba AD.
- **vSphere:** govmomi'nin simülatörü `vcsim`; gerçek vCenter doğrulaması ayrıca yapılır.

## Faz 1: `gaia server`

Gaia şu an bir CLI. Tasarımdaki tek binary'nin sunucu tarafı (13.1).

- `gaia server`: API, worker'lar ve zamanlayıcı aynı süreçte; `--role=api|worker` ile ayrılabilir.
- **İş kuyruğu:** River. PoC'de senkron olan apply bir iş olur; kontrat kilidi, `limits.concurrentRuns`, run iptali ve duraklatma.
- **API:** REST + JSON, `/api/v1`, OpenAPI 3.1 şeması repoda; arayüzün TypeScript istemcisi bu şemadan üretilir. Canlı olaylar SSE ile; replikalar arasında `LISTEN/NOTIFY`.
- **Run'lar:** `run_stages`; ham Ansible ve Pulumi çıktısı ayrı bir bucket'a arşivlenir (Object Lock'suz).
- **CLI:** `gaia plan` ve okuma komutları API'yi kullanır; yerel mod (veritabanına doğrudan) geliştirme için kalır.
- **Sağlamlaştırma:** OpenBao'ya AppRole ile bağlanma (root token yok); veritabanı rolleri `gaia_owner` (migration) ve `gaia_app` (çalışma, `run_records`'ta sadece insert); run olaylarının toplu yazılması.
- **Yerel geliştirme ortamı:** `deploy/dev/compose.yaml`.

**Bitti sayılır:** Bir kontrat API üzerinden kuyruğa girip worker'da uygulanıyor, canlı olaylar SSE'den akıyor, run iptal edilebiliyor.

**Sonuç (2026-10-24):**
- `gaia server --role all|api|worker` ve River kuyruğu.
  - Kontrat başına bir iş. Worker planı yeniden alır; hash değiştiyse uygulamaz. Meşgul kontratta iş bekler.
  - Run iptali: kuyrukta veya çalışırken.
- Aşamalar kontrat başına `run_stages`'te. Çıktının her satırı ve her Ansible olayı tek sıralı akışta (`run_events`, COPY ile toplu, `NOTIFY` ile duyurulur).
- API: `api/openapi.yaml` (testle route'larla eşit). SSE ile canlı izleme, `Last-Event-ID` ile devam. `gaia run list|show|watch|cancel`.
  - Kimlik doğrulama gelene kadar sunucu sadece loopback'te dinler.
- Sağlamlaştırma:
  - Worker'lar OpenBao'ya AppRole ile girer, token'ı yeniler ve gerekince yeniden girer.
  - `gaia migrate`, `gaia_app` rolüne sadece gerekenleri verir; run kayıtlarını değiştiremez ve silemez.
  - Ham olaylar `runLogs` bucket'ına (kilitsiz) arşivlenir; veritabanındaki olaylar 90 gün sonra silinir.
- `gaia apply --queue` CLI'dan kuyruğa iş koyar. Faz 3'te uygulamayı Change motoru başlatacak.

## Faz 2: Kimlik doğrulama ve yetki

Tasarım 11.2–11.4.

- `AuthConfig` kind'ı; local hesaplar (break-glass), passkey (`go-webauthn`), parola + TOTP, kurtarma kodları.
- OIDC (`go-oidc`): `amr`/`acr` ile MFA kanıtı, yoksa Gaia passkey'i. Yerelde Keycloak ile test edilir.
- Gaia'nın opak token'ları (sadece hash'i tutulur), oturumlar ve iptal; `gaia login` (loopback + PKCE) ve `gaia login --device`.
- Step-up: onay gibi işlemlerden önce passkey/MFA'nın yeniden istenmesi (Faz 3 kullanır).
- Roller: `viewer`, `operator`, `platform-admin`. Team'ler Faz 6'da gelene kadar roller Policy'deki `gaiaRoles` ile local kullanıcılara ve OIDC grup claim'lerine bağlanır.
- Görünürlük kuralı: yetkisiz kontrat adıyla görünür, detayı kapalı.
- İmza anahtarları: kullanıcı SSH/GPG anahtarını profiline kaydeder (Faz 3 kullanır).

**Bitti sayılır:** Arayüz ve CLI oturumla API'yi kullanıyor; roller ve görünürlük testlerle doğrulanmış.

**Sonuç (2026-10-24):**
- `AuthConfig` ve `Policy.gaiaRoles`.
- Local hesaplar: tek kullanımlık kayıt bağlantısı, passkey, kurtarma kodları; `require: mfa`'da parola + TOTP (tohum OpenBao transit ile şifreli).
- OIDC: PKCE + nonce. MFA kanıtı `amr`/`acr` ile; yoksa Gaia passkey'i. Takımlar groups claim'inden. Yerel Keycloak'la uçtan uca denendi.
- Oturumlar: opak token (hash), tarayıcı çerezi ve CSRF başlığı, boşta kalma süresi, step-up.
- CLI: `gaia login` (loopback + PKCE) ve `--device`; refresh/access token'ları keychain'de. `gaia logout`, `gaia whoami`.
- Roller ve görünürlük API'de; imza anahtarları kayıtlı (Faz 3 commit doğrulamasında kullanacak).
- Giriş sayfaları sade ve sıkı CSP'li; Faz 5'te arayüze taşınacak.

## Faz 3: Change motoru ve GitHub izleyicisi

Gaia'nın omurgası (tasarım 7.6).

- **Repolar:** `git` Provider'ları; uzak repoyu credential'la fetch (SSH anahtarı veya token); PoC'deki yerel klon kısıtı kalkar. İzlenen branch, webhook (GitHub) ve periyodik fetch.
- **Change:** kontrat başına; plan diff'i, YAML diff'i; supersede; yaşam döngüsü.
- **Gereksinimler:** validasyon, imza (kayıtlı anahtarlar), onay (Policy, Group, çoklu owner), pencere, kilit.
- **Onay ve red:** API, `gaia approve` / `gaia reject`, step-up; plan hash'ine bağlı onay; kendi kendini onaylama yasağı; gruplu onay.
- **Uygulama:** gereksinimler sağlanınca kuyruğa girer; uygulamadan önce plan hash'i yeniden kontrol edilir.
- **Gaia'nın commit'leri:** Gaia'nın imza anahtarı, doğrulanmadan yazılmayan commit'ler; revert.
- **Önizleme:** `gaia plan --ref`.
- **Kontrat durumu:** `Synced` / `OutOfSync` / `Drifted` ve sağlık.
- Run kaydına `requestedBy` (imzadan) ve `approvedBy` (onaylardan).

**Bitti sayılır:** Lab'de: commit → Change → onay → uygulama; supersede, red ve revert; imzasız commit'in uygulanmaması.

**Durum:**
- Tamam: repolar ve anlık görüntü; imza doğrulaması; Change motoru (gereksinimler, supersede, retry, sync durumu); onay ve red (API, onay sayfası, `gaia change approve`, step-up, toplu onay, çoklu owner); run kaydında `approvedBy`.
- Tamam: GitHub webhook'u; Gaia'nın imzalı commit'leri ve revert (`gaia revert`).
- Tamam: önizleme (`gaia preview`, `GET /api/v1/preview`).
- Tamam: gerçek bir Git sunucusuyla (Gitea, `make dev-git`) uçtan uca test:
  - ssh ile fetch ve push; kimlik bilgisi ve `known_hosts` OpenBao'dan
  - imzalı webhook'la anında Change
  - onay
  - kuyruktaki run'ın iptali ve Change'in kapanması
  - Gaia'nın ssh ile push'ladığı imzalı revert (`git verify-commit` doğruluyor)
  - isteyenin onaylayamaması
- Kalan: uygulamanın gerçek sunucularla lab'de denenmesi. Faz 4'le birlikte yapılacak: uygulanan bir Change bildirimleri de tetiklediği için tek bir lab oturumu ikisini birden doğrular.
- Faz 10'a taşındı: decommission sonrası kontrat dosyasının silinmesi (Gaia'nın commit yazıcısını kullanır).

## Faz 4: Bildirimler

- Provider tipleri: `slack`, `msteams`, `smtp`, `webhook`.
- Tetikleyiciler: onay bekliyor, onaylandı, reddedildi, uygulandı, başarısız, drift bulundu, onayın süresi doluyor.
- Abonelik: Team (patch ile `notifications`) ve kişi başına; gönderimler `notifications` tablosunda, tekrar denemeli.

**Bitti sayılır:** Dört kanal yerelde test edilmiş (Slack/Teams için webhook alıcısı, SMTP için MailHog).

**Durum:**
- Tamam:
  - katalog: Team, kanal Provider'ları, `Platform.spec.notifications`
  - motorun olayları (onay bekliyor, onaylandı, reddedildi, uygulandı, başarısız)
  - `notifications` tablosu ve tekrar denemeli teslim
  - dört kanal, sahte alıcılarla test edildi
- Tamam:
  - kişisel abonelikler (`/api/v1/me/notifications`, `gaia notifications`)
  - e-posta adresi: OIDC'nin doğrulanmış adresi, yerel hesapta kişinin kendisi
  - Change'in bildirim listesi
- Uçtan uca test: Gitea + Mailpit + yerel webhook alıcısı. İmzalı push'tan sonra şu olaylar dört kanala da ulaştı:
  - onay bekliyor
  - onaylandı
  - gerçek worker'da başlayıp hata veren run sonrası "başarısız" (hata ve onaylayan mesajda)
- Kalan: lab oturumu (Faz 3'ün uygulaması da burada).
- Sonraki fazlara bırakılan tetikleyiciler: `drift.found` (drift), `approval.expiring` (pencereler).

## Faz 5: Arayüz

- Önce tasarım taslağı; onaylanınca ekranlar.
- React + TypeScript, binary'ye `go:embed` ile gömülü; diyagramlar React Flow + ELK.js.
- Ekranlar (11.5): Genel bakış, Değişiklikler, Kontratlar, Run'lar, Katalog, Provider'lar, Profil; Sync ve Keşif ilgili fazlarla birlikte.

**Bitti sayılır:** Bir değişiklik arayüzde görülüp passkey ile onaylanıyor, run canlı izleniyor.

## Faz 6: Erişim yönetimi

Tasarım 14.

- `TeamSource` (AD'den `Team`), `Group`, kontrattaki `access`.
- Üyeliklerin AD'den veritabanına senkronu (5 dk), `accessSnapshot`.
- Dağıtım: Linux (Ansible ile kullanıcı ve sudo), Zabbix kullanıcı grupları; Warpgate Faz 9'da.
- Gaia rollerinin Team üyeliklerinden gelmesi; devre dışı hesapların erişiminin ve oturumlarının kaldırılması.

**Bitti sayılır:** Samba AD'de bir kişi takıma eklenince 5 dakika içinde sunucuya erişiyor, çıkarılınca erişimi gidiyor.

**Durum:**
- Tamam:
  - AD okuma, TeamSource ve üretilmiş objeler ile patch'ler
  - üyelik senkronu ve OIDC'nin AD hesabına bağlanması
  - Group ve kontrat erişimi ile onay kuralları
  - Linux dağıtımı (`gaia_access`), erişim run'ları ve Zabbix kullanıcı grupları
- Doğrulama:
  - AD tarafı gerçek Samba AD'ye karşı test edildi
  - Linux rolü Rocky 9 konteynerinde denendi
  - Zabbix grupları gerçek Zabbix 7.0'a karşı test edildi
- Kalan: "Bitti sayılır" senaryosu gerçek sunucularla lab oturumunda.
- Warpgate dağıtımı Faz 9'da.

## Faz 7: Source'lar

- `NetworkSource` (IPAM'dan `Network`), `AnsibleRoleSource` (rol reposundan `AnsibleRole`, `meta/gaia.yml`).
- Elle ekleme ve patch (5.5); sync raporu; Gaia'nın imzalı commit'leri.

## Faz 8: Kontrat genişlemeleri

- `App` ve `Agent` kind'ları; Policy'den zorunlu ajanlar.
- `Operation` ve `OperationSchedule` (RRULE): patch, reboot, snapshot, replace, runbook; batch stratejisi, pencere, `notAfter`.
- `no_log` / secret parametreler (`bao://` değerleri run sırasında çözülür ve maskelenir).
- Host key yönetimi: cloud-init'in ürettiği host anahtarları Gaia'ya kaydedilir, host key kontrolü açılır.

## Faz 9: Provider'lar

- vSphere compute provider'ı (`vsphere` Pulumi provider'ı, govmomi; `vcsim` ile test).
- Ortak `Compute` arayüzü ile day-2: power, snapshot.
- AD (bilgisayar hesapları), DNS (A/PTR), Warpgate (SSH/RDP hedefleri), Nexus.

## Faz 10: Yaşam döngüsü

- Drift kontrolü ve düzeltme Change'leri (7.5).
- Keşif (8.3), adopt (8.1–8.2), `gaia reconcile`.
- Tam decommission akışı: bekleme süreleri, bakım modu, önce kapatma sonra silme (9.8); prod koruması (9.9); replace (9.5).

## Faz 11: Dağıtım

- `nfpm` ile `.deb` / `.rpm`, systemd unit'i, `gaia.server` Ansible rolü.
- Container imajı ve Helm chart (13.3).

## Lab oturumu

Gerçek sunucularla yapılacak denemeler tek bir lab oturumunda toplanır: Faz 3–4'ün uygulama ayağı (commit → Change → onay → gerçek VM ve bildirimler) ve lab gerektiren sonraki fazlar.

- 192.168.1.25 artık Proxmox'taki başka bir VM'de (2026-10-25'te görüldü). Platform VM'i lab'e ayrılan aralıktaki boş bir adresle (192.168.1.26) kurulur.

## Dışarıya bağlı işler

- `proxmoxve` disk `speed` sorununun upstream'e bildirilmesi (dışarıya açık bir iş; ayrıca onay gerekir).
