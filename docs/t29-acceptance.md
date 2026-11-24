# T29 — M3 kabul kontrol listesi

Bu liste Yiğit içindir. T23 sunucunu (`gaia-t23-b`) `0.2.0`'a **yalnız kılavuzdan** (`docs/install.md` adım 12, "To
0.2.0") yükseltirsin ve lab Keycloak'ına karşı SSO'yu açarsın. İkinci, temiz bir kurulum (`gaia-t24-scale`) T24'ün
5,000 VM'lik kataloğunu taşır. Sonra aşağıdaki satırları sırayla uygular ve sonunda iki soruyu kendi sözlerinle
cevaplarsın.

- Her satırda bir komut var. Çıktısını satırın altındaki boş bloğa yapıştır (T23'ün dersi: "beklendiği gibi" diye
  işaretlenen her satırın arkasında yapıştırılmış bir çıktı olmalı).
- Ekranda gördüğün bir şey listede yazdığından farklıysa, ya da kılavuzda bir adım belirsizse, "Not" satırına yaz.
- Komutlar kopyala-yapıştır içindir; yalnız `<köşeli parantez>` içini değiştir.

## Ortam

| Ne | Nerede | Kim hazırladı |
|---|---|---|
| Release | `0.2.0` (imzalı), Mac'te `releases/0.2.0/` | Hazır |
| Senin sunucun | `gaia-t23-b` (VMID 9107, 192.168.1.122), şimdi `0.1.2`; korunan VM'ler gaia-lab'de `gaia-t23-linux2`, `-uefi`, `-win`; host kullanıcısı `gaia-t23-backup` | Sen (T23) |
| Sunucunun adı | `gaia-t23-b.lab.test` (WebAuthn bir ad ister, IP adresi olmaz; ADR-008 §3) | Sen (adım 1) |
| Bildirim alıcısı | gaia-lab'de `lab/lab-vm/t23-sink.sh` (T23'teki gibi) | Hazır |
| Kimlik sağlayıcı | gaia-lab'de Keycloak, `https://gaia-lab.test:8444/realms/gaia` (`lab/lab-vm/t26-keycloak.sh`) | Hazır (adım 2'de başlatırsın) |
| Ölçek kurulumu | Proxmox'ta `gaia-t24-scale` (VMID 9108, 192.168.1.123, 4 vCPU 8 GiB); `0.2.0` paketi ve 5,000 VM, 100 host, 3,000,000 nokta | Sen (adım 3) |

Sunucu B'ye (9105, `gaia-t17-b`), `gaia-t17-web`'e ve gaia-lab'deki `gaia-backup` kullanıcısına **dokunma**.

## 0. Release'i doğrula (Mac'te)

```bash
cd ~/Desktop/Works/Warewave/projects/gaia/releases/0.2.0 && awk '/Gaia.s release key is this one line/ { f = 1 } f && /^```$/ { if (b) exit; b = 1; next } b' ../../docs/install.md >allowed_signers && ssh-keygen -Y verify -f allowed_signers -I release@gaia -n gaia-release -s SHA256SUMS.sig <SHA256SUMS && shasum -a 256 -c SHA256SUMS
```

- [x] İlk satır `Good "gaia-release" signature for release@gaia`, her dosya `OK`.

```
Good "gaia-release" signature for release@gaia with ED25519 key SHA256:Nqb0MoKtSc+AZaFGJkfOpgELaRqyYLlkz9e2VjvKanc
gaia_0.2.0_amd64.deb: OK
gaia-0.2.0-darwin-amd64.tar.gz: OK
gaia-0.2.0-darwin-arm64.tar.gz: OK
gaia-0.2.0-linux-amd64.tar.gz: OK
gaia-0.2.0-linux-arm64.tar.gz: OK
BUILDINFO.txt: OK
```

## 1. Yükseltme: kılavuz adım 12, "To 0.2.0"

Adın çözülmesi: Mac'te ve gaia-lab'de `/etc/hosts`'a bir satır, sunucuda Keycloak'ın adı.

```bash
echo '192.168.1.122 gaia-t23-b.lab.test' | sudo tee -a /etc/hosts
```

gaia-lab'de (root) ve `gaia-t23-b`'de (root) sırasıyla:

```bash
echo '192.168.1.122 gaia-t23-b.lab.test' >> /etc/hosts
```

```bash
echo '192.168.1.30 gaia-lab.test' >> /etc/hosts
```

Paketi kopyala (Mac'ten) ve kılavuzu uygula: adım 12'nin 1–3. maddeleri, sonra "To 0.2.0" maddeleri (`public_url`
`https://gaia-t23-b.lab.test:8443`; `gaia server tls -hostname gaia-t23-b.lab.test,192.168.1.122`; sertifikayı
Keychain'de güvenilir yap; tarayıcıda Touch ID ile giriş; `gaia login` yeni sha256'yı sorar; `gaia kit regenerate && gaia kit save`).

```bash
scp ~/Desktop/Works/Warewave/projects/gaia/releases/0.2.0/gaia_0.2.0_amd64.deb ~/Desktop/Works/Warewave/projects/gaia/releases/0.2.0/SHA256SUMS gaia@192.168.1.122:/tmp/
```

Bitince Mac'te:

```bash
gaia system -json | jq -c '{version, schema_version, signin_problem}' && gaia kit status -json | jq -c '.summary | {state, generation}'
```

- [x] `version` `0.2.0 (...)`, `schema_version` `16`, `signin_problem` yok; kit `confirmed`.
- [x] Yükseltmeden önceki `gaia login` oturumun çalışmadı (kılavuz: "Every session ends at the upgrade").

```
{"version":"0.2.0 (4901ec4da20d)","schema_version":16,"signin_problem":null}
{"state":"confirmed","generation":3}
```

Not:

## 2. SSO: kılavuz adım 9b, lab Keycloak'ı

gaia-lab'de (root): Keycloak'ı başlat, sunucunun adresini istemciye ekle, kendine bir admin kullanıcısı aç:

```bash
/root/gaia/lab/lab-vm/t26-keycloak.sh start && /root/gaia/lab/lab-vm/t26-keycloak.sh redirect https://gaia-t23-b.lab.test:8443 && /root/gaia/lab/lab-vm/t26-keycloak.sh user yigit gaia-admins
```

İlk parolan `/root/t26-keycloak/secrets.json` içinde (`.users.yigit.password`); ilk girişte değiştirir ve OTP'yi
telefonuna eklersin. İstemcinin secret'ı aynı dosyada (`.client_secret`). Bunları ben görmüyorum.

İstemcinin secret'ını tırnaksız ve satır sonu olmadan panoya almak için Mac'te şunu çalıştır, sonra secret alanına yapıştır
(`jq` `-r` olmadan tırnaklarıyla verir; Keycloak o zaman `invalid_client_credentials` der, Gaia `idp_error`):

```bash
ssh gaia@192.168.1.30 sudo jq -r .client_secret /root/t26-keycloak/secrets.json | tr -d '\n' | pbcopy
```

Keycloak'ın sertifikası öz-imzalı; `gaia-t23-b` ona güvenmeli. gaia-lab'den sunucuya kopyala, sunucuda (root) ekle:

```bash
scp /root/t26-keycloak/certs/kc.crt gaia@192.168.1.122:/tmp/gaia-lab-idp.crt
```

```bash
install -m 0644 /tmp/gaia-lab-idp.crt /usr/local/share/ca-certificates/gaia-lab-idp.crt && update-ca-certificates && systemctl restart gaia
```

Sonra tarayıcıda **Inventory → Security → Single sign-on**: issuer `https://gaia-lab.test:8444/realms/gaia`, client
`gaia`, secret, gruplar `gaia-admins` → admin, `gaia-operators` → operator, `internal-audit` → auditor, ikinci
faktör `acr` = `mfa`. Kaydet (Touch ID ister). Çıkış yap; giriş sayfasında **Continue with …** ile `yigit` olarak gir.

```bash
gaia api GET /api/v1/settings/signin | jq -c '{oidc: (.oidc | {enabled, issuer, client_id, roles}), secret_set: .oidc_client_secret_set}' && gaia users list -json | jq -c '[.[] | {username, role, source: (.source // .kind), break_glass}]'
```

- [x] SSO açık, roller eşlenmiş; `yigit` SSO kullanıcısı olarak listede, rolü `admin`.

```
{"oidc":{"enabled":true,"issuer":"https://gaia-lab.test:8444/realms/gaia","client_id":"gaia","roles":{"gaia-admins":"admin","gaia-operators":"operator","internal-audit":"auditor"}},"secret_set":true}
[{"username":"admin","role":"admin","source":"local","break_glass":true}]
```

Not:

## 3. Ölçek kurulumu: `gaia-t24-scale`

VM'i Proxmox'ta başlat (kapalıysa). Kılavuzun 2–4. adımlarını `0.2.0` ile uygula (`gaia server init` admin
parolanı sorar; `-hostname gaia-t24-scale.lab.test,192.168.1.123`), **5. adımdan önce** kataloğu yükle. Mac'te:

```bash
cd ~/Desktop/Works/Warewave/projects/gaia && lab/scale/t29-catalog.sh make /tmp/t29c && scp -r /tmp/t29c root@192.168.1.123:/root/
```

`gaia-t24-scale`'de (root):

```bash
/root/t29c/t29-catalog.sh fill
```

Mac'te `/etc/hosts`'a `192.168.1.123 gaia-t24-scale.lab.test`, sertifikayı Keychain'e ekle, tarayıcıda gir (Touch ID).

- [x] `fill` kataloğu yazdı (`"vms": 5000, "hosts": 100`) ve `status table recomputed` satırını gösterdi.

```
catalog generated in 359 s
{"now": "2026-11-23T17:42:34.238853728Z", "vms": 5000, "hosts": 100, "points": 2931617, "present_points": 85636, "jobs": 3778509, "tests": 845967}
Created symlink /etc/systemd/system/multi-user.target.wants/gaia-sandbox.service → /lib/systemd/system/gaia-sandbox.service.
time=2026-11-23T17:48:34.930Z level=INFO msg="gaia server is listening" address=[::]:8443 version="0.2.0 (4901ec4da20d)" schema=16
time=2026-11-23T17:48:43.471Z level=INFO msg="status table recomputed" reason=start vms=5000 changed=4996 seconds=6.17167813 errors=0
```

## 4. Kabul satırları

### 4.1 Erişilemeyen bir host, bütün VM'leriyle **tek** grup

Senin sunucun (`gaia-t23-b`) VM'leri gaia-lab'den SSH ile yedekliyor. Hostu erişilemez yapmak için **gaia-lab'de, root
olarak** sunucunun gaia-lab'e SSH'ını kes (yalnız 192.168.1.122'den gelen 22. port; başka hiçbir şey etkilenmez):

```bash
iptables -I INPUT -s 192.168.1.122 -p tcp --dport 22 -j DROP
```

Bekleme: ilk yedek denemesi en geç politikanın aralığında (30 dk) başarısız olur, ama VM'ler RPO hedefi (T23
politikalarında 2 saat) son başarılı yedekten itibaren dolana kadar `OK` kalır. `host_unreachable` grubu o zaman çıkar.
Kuralı en fazla 2,5 saat açık bırak. 4.2'nin 6 saatlik ölçümüyle aynı anda yapma (yedekler durur).

Grup çıkınca Overview → **Needs attention**'a bak, sonra **Mac'te** (CLI oturumunla, `gaia-t23-b`'ye):

```bash
gaia api GET '/api/v1/views/attention?limit=50' | jq -c '.groups[] | {status, code, scope: .scope.name, count, reach}'
```

Bekleneni: `{"status":"CRITICAL","code":"host_unreachable","scope":"gaia-lab","count":3,"reach":{"host_vms":3,"elsewhere":0,"ok":0}}`
gibi tek satır (üç VM'in de hostu gaia-lab).

Sonra **gaia-lab'de, root olarak** kuralı kaldır (bir sonraki yedek geçince grup kaybolur):

```bash
iptables -D INPUT -s 192.168.1.122 -p tcp --dport 22 -j DROP
```

Ölçek kurulumunda beklemeye gerek yok: katalogda erişilemeyen hostlar zaten var. Aynı komutu ölçek kurulumunun oturumuyla çalıştır; orada bir hostun grubu "of 50" gibi, hostun bütün VM sayısını da gösterir.

- [x] Ekranda host için **bir** grup (`host_unreachable`, `gaia-lab`); "Show these VMs" hostun bütün VM'lerini
  listeliyor. Başka nedenle listelenen VM varsa grup "affects N VMs · … here, … under other causes" diyor.

```
{"status":"CRITICAL","code":"host_unreachable","scope":"gaia-lab","count":3,"reach":{"host_vms":3,"elsewhere":0,"ok":0}}
```

Not:

### 4.2 Proof capacity: 1 slot ve 4 slot, 6 saatte olanla aynı

Önce tek slot. Sunucuda (root), kılavuz adım 10b:

```bash
mkdir -p /etc/systemd/system/gaia-sandbox.service.d && printf '[Service]\nExecStart=\nExecStart=/usr/bin/gaia sandbox serve -slots 1\n' >/etc/systemd/system/gaia-sandbox.service.d/slots.conf && systemctl daemon-reload && systemctl restart gaia-sandbox
```

Mac'te (depo dizininde) ölçümü başlat; 6 saat sonra kontrol et:

```bash
lab/lab-vm/t29-capacity.sh start ~/t29-capacity-1
```

```bash
lab/lab-vm/t29-capacity.sh check ~/t29-capacity-1
```

`check`'i 6 saat dolduktan sonraki **20 dakika içinde** çalıştır (`start` saati söyler; bir hatırlatıcı kur). Kanıtı
süresi dolan VM'lerin listesi `check` anında okunuyor, pencere bitiminden çok sonra okunursa başka bir anı ölçer; betik
o zaman `UNKNOWN` der ve `start`'ı yeniden çalıştırmak gerekir.

Sonra aynısını `-slots 4` ile (`~/t29-capacity-4`). Betik, ekrandaki sayıları (Capacity: tests in 24 h, coverage, "will
expire") o 6 saatte biten testlerle ve süresi gerçekten dolan kanıtlarla karşılaştırıyor: test sayısı %20 (ya da 2
test) içinde, süresi dolacağı söylenen VM'ler de dolanlarla aynı.

- [x] 1 slot: `"verdict": "OK"`.
- [ ] 4 slot: `"verdict": "OK"`.

```
{
  "started_at": "2026-11-23T20:48:37Z",
  "slots": 1,
  "slots_at_end": 1,
  "coverage": 1,
  "tests": {
    "expected": 18,
    "finished": 18,
    "by_verdict": {
      "OK": 18
    },
    "match": true
  },
  "expiries": {
    "said": [
      "gaia-t23-uefi"
    ],
    "happened": [
      "gaia-t23-uefi"
    ],
    "proof_too_old_at_start": [],
    "proof_too_old_at_end": [],
    "match": true
  },
  "verdict": "OK"
}
```

```
{
  "started_at": "2026-11-24T05:36:35Z",
  "slots": 4,
  "slots_at_end": 4,
  "coverage": 1,
  "tests": {
    "expected": 18,
    "finished": 18,
    "by_verdict": {
      "OK": 12,
      "UNKNOWN": 6
    },
    "match": true
  },
  "expiries": {
    "said": [],
    "happened": [
      "gaia-t23-uefi"
    ],
    "proof_too_old_at_start": [],
    "proof_too_old_at_end": [
      "gaia-t23-uefi"
    ],
    "match": false
  },
  "verdict": "CRITICAL"
}
```

Not (Claude, 2026-11-25): iki `check` de 2026-11-24 17:55 UTC'de çalıştı, pencereler 02:48 ve 11:36'da bitmişti.
Test sayıları pencereye göre süzüldüğü için doğru; ama `proof_too_old` listesi 17:55'teki durum. Bu yüzden 1 slottaki
`expiries` eşleşmesi tesadüf, ikisi de yeniden ölçülmeli. 4 slottaki 6 `UNKNOWN` test gerçek bir bulgu: Windows
(32 GiB) ve `gaia-t23-uefi` (6 GiB) aynı dakikada başlıyor; sandbox images dizininde 36 GB boş olduğu için ikincisi
beklemek yerine "not tested" (`UNKNOWN`) oluyor. Kapasite görünümü diski hesaba katmadığı için bunu öngörmüyor.

### 4.3 Yeni bir tarayıcıdan giriş bildiriliyor

Önce gaia-lab'de (root) bildirim alıcısının çalıştığına bak. Alıcı `systemd-run` ile açılan geçici bir birim, gaia-lab
yeniden başlayınca kapanıyor (2026-11-19 22:39'dan 2026-11-24 21:09'a kadar kapalıydı; ilk denemede bu yüzden bir şey
gelmedi). `active` demiyorsa başlat:

```bash
systemctl is-active gaia-t23-sink || /root/gaia/lab/lab-vm/t23-sink.sh start 192.168.1.30
```

Sonra başka bir tarayıcıda (ya da gizli pencerede) admin olarak gir. gaia-lab'de (root):

```bash
/root/gaia/lab/lab-vm/t23-sink.sh list 10
```

- [ ] `new_device_signin` olayı kanala geldi. - Kanala bişey gelmedi

```
2026-11-18T23:32:19Z    mail    status  Gaia gaia-t23-a: gaia-t23-win: WARNING -> OK
2026-11-18T23:32:19Z    webhook status  Gaia gaia-t23-a: gaia-t23-win: WARNING -> OK
2026-11-19T07:00:04Z    mail    daily_report    Gaia gaia-t23-a: daily report 2026-11-19: 3 OK
2026-11-19T07:00:04Z    webhook daily_report    Gaia gaia-t23-a: daily report 2026-11-19: 3 OK
2026-11-19T13:49:14Z    mail    daily_report    Gaia gaia-t23-a: daily report 2026-11-19: 3 OK
2026-11-19T13:49:14Z    webhook daily_report    Gaia gaia-t23-a: daily report 2026-11-19: 3 OK
2026-11-19T13:51:14Z    mail    daily_report    Gaia gaia-t23-a: daily report 2026-11-19: 3 OK
2026-11-19T13:51:14Z    webhook daily_report    Gaia gaia-t23-a: daily report 2026-11-19: 3 OK
2026-11-19T15:36:54Z    mail    daily_report    Gaia gaia-t23-b: daily report 2026-11-19: 3 OK
2026-11-19T15:36:54Z    webhook daily_report    Gaia gaia-t23-b: daily report 2026-11-19: 3 OK
```

### 4.4 Boşta kalan oturum bitiyor

Bir tarayıcıda gir. Mac'te oturumları listele, sonra sekmeyi açık bırakıp 15 dakika hiçbir şeye dokunma (açık sekme
arka planda istek atsa da oturum uzamamalı; ADR-008 §5), ve aynı komutu yeniden çalıştır. Sonra tarayıcıda bir sayfaya
tıkla.

```bash
gaia api GET /api/v1/sessions | jq -c '.[] | {id, username, kind, method, last_active_at, last_seen_at, user_agent: .user_agent[0:40]}'
```

- [x] İkinci listede tarayıcı oturumu yok (`last_seen_at` arka plan isteklerinde ilerlese de `last_active_at`
  ilerlemedi); tarayıcı giriş sayfasına döndü.

```
{"id":17,"username":"admin","kind":"cli","method":"webauthn","last_active_at":"2026-11-24T19:46:56.422562016Z","last_seen_at":"2026-11-24T19:46:56.422562016Z","user_agent":"Go-http-client/1.1"}
{"id":16,"username":"admin","kind":"browser","method":"webauthn","last_active_at":"2026-11-24T19:46:49.719980694Z","last_seen_at":"2026-11-24T19:46:49.719980694Z","user_agent":"Mozilla/5.0 (Macintosh; Intel Mac OS X 1"}
```

### 4.5 Step-up'sız yıkıcı bir işlem reddediliyor

Önce kılavuzun 9. adımıyla bir deneme kullanıcısı ekle (`gaia users add username=t29-silinecek role=auditor`), id'sini al.
Sonra step-up istemeyen bir istemciyle (`curl`, CLI oturumunun token'ıyla; `gaia` komutu step-up'ı kendisi istediği
için burada kullanılmıyor) onu kapatmayı dene. Gaia kullanıcı silmez, kapatır (`disabled`); ilk sürümdeki `DELETE`
komutu yanlıştı (`no such endpoint`):

```bash
curl -sk -H "Authorization: Bearer $(jq -r .token ~/Library/Application\ Support/gaia/cli.json)" -H 'Content-Type: application/json' -X PATCH -d '{"disabled":true}' https://gaia-t23-b.lab.test:8443/api/v1/users/<id> ; echo; gaia users list -json | jq -c '[.[] | {username, disabled}]'
```

- [ ] Cevap `step_up_required`; `t29-silinecek` listede `"disabled":false` duruyor. (Sonra `gaia users set <id>
  disabled=true` ile kapatabilirsin; `gaia` step-up'ı tarayıcıda onaylatır.) - Çıktıdaki hatayı verdi

```
warewave-tech:gaia yigit.basalma$ curl -sk -H "Authorization: Bearer $(jq -r .token ~/Library/Application\ Support/gaia/cli.json)" -X DELETE https://gaia-t23-b.lab.test:8443/api/v1/users/3 ; echo; gaia users list -j
son | jq -c '[.[].username]'
{
  "error": "no such endpoint: DELETE /api/v1/users/3",
  "code": "not_found"
}

["admin","yigit","t29-silinecek"]
```

### 4.6 Keycloak durunca break-glass çalışıyor ve bildiriliyor

Önce 4.3'teki gibi alıcının çalıştığına bak. gaia-lab'de (root): `/root/gaia/lab/lab-vm/t26-keycloak.sh stop`.

**10 dakika bekle.** Gaia bir SSO oturumunu Keycloak'a 10 dakikada bir soruyor (ADR-008 §2); Keycloak'ın cevap
vermediğini ancak bu sorudan sonra biliyor ve oturumu `idp_unreachable` diye işaretliyor. Bundan önce step-up penceresi
seni Keycloak'a yönlendirmeye çalışır; Keycloak kapalı olduğu için o bağlantı açılmaz (ilk denemede gördüğün buydu).
10 dakika sonra **Inventory → Security → Sessions**'ta SSO oturumunun yanında `IDP UNREACHABLE` işareti çıkar.

Sonra SSO kullanıcısıyla açık oturumda yıkıcı bir işlem dene (örneğin `t29-silinecek`'i kapat). Pencere işlemi
`idp_unreachable` nedeniyle reddettiğini söylemeli. Sonra yerel admin (break-glass) olarak gir. gaia-lab'de:

```bash
/root/gaia/lab/lab-vm/t23-sink.sh list 10
```

Sonra Keycloak'ı yeniden başlat: `/root/gaia/lab/lab-vm/t26-keycloak.sh start`.

- [ ] SSO oturumunda yıkıcı işlem `idp_unreachable` ile reddedildi; break-glass girişi çalıştı; `breakglass_signin`
  kanala geldi.

```
Kullanıcıyı disable etmeye çalıştım, confirm ekranı açıldı, sign in again linki de görünüyor ama tıklayınca gitmiyor. idp_unreachable şeklinde bir uyarı/hata hiçbir yerde yok.
```

### 4.7 5,000 VM'lik kurulumun Overview'u küçük olan kadar hızlı

İki kurulumun Overview'unu aç, aynı soruları sor (en kötü durumdaki VM, nedeni, ne zamandan beri). Mac'te, her
kurulumun oturumuyla:

```bash
for i in 1 2 3; do /usr/bin/time -p gaia api GET '/api/v1/views/fleet' >/dev/null; done 2>&1 | grep real
```

- [x] İkisi de ekranda 2 saniye içinde etkileşimli; sorularının cevabını aynı hızla buldun.

```
Küçük olan
real 0.11
real 0.11
real 0.11

Büyük olan
real 0.21
real 0.22
real 0.21
```

Not:

### 4.8 Windows migration check'i (T28, ADR-005 F)

**Ne yapıyor:** ESXi'deki Windows Server 2022 test VM'ini (`gaia-t11-win`) KVM'e dönüştürüp açıyor ve "taşınırsa
çalışır mı" kontrolünü yapıyor. Bu kontrol iki kez çalışıyor: bir kez olduğu gibi, bir kez de smbus sürücüsü
dışarıda bırakılarak. Yanında Linux, UEFI ve bozuk disk kontrolleri de çalışıyor. Hepsi gaia-lab'de **kendi geçici
Gaia sunucusunu** (`gaia-t21-server`) kuruyor ve sonunda siliyor. Senin sunucuna (`gaia-t23-b`) ve ölçek kurulumuna
dokunmuyor. ESXi'ye yalnız okuma yapıyor.

**Nerede:** gaia-lab'de (192.168.1.30), root olarak. ESXi hostu (192.168.1.29) açık olmalı. Mac'te bir şey
çalıştırmıyorsun.

**Süre:** yaklaşık 1 saat (T29 hazırlığındaki çalıştırma 11:50 → 12:52). 4.2'nin 6 saatiyle aynı anda yapma; ikisi
de gaia-lab'in belleğini ve CPU'sunu kullanıyor.

1. gaia-lab'de, root olarak başlat. Komut arka planda bir systemd birimi olarak çalışıyor ve günlüğü izliyor:

```bash
cd /root/gaia && systemd-run --unit=gaia-t21 --collect -E HOME=/root lab/lab-vm/t21-migration.sh 192.168.1.29 && journalctl -fu gaia-t21
```

   `Ctrl-C` yalnız izlemeyi bırakır, çalıştırma devam eder. Tekrar izlemek için: `journalctl -fu gaia-t21`

2. Bitince günlüğün sonunda iki satır var: `{"verdict":...}` ile başlayan gate satırı ve `out: /root/t21-...`.
   Kaçırdıysan, gaia-lab'de root olarak:

```bash
d=$(ls -d /root/t21-* | tail -1); jq -c '{verdict, a: .a_t8_linux.pass, b: .b_t11_windows.pass, c: .c_uefi.pass, d: .d_zero_writes.pass, e: .e_reports.pass, f: .f_converter.pass}' $d/gate.json; jq -c '.b_t11_windows | {as_is: {readiness: .as_is.readiness, code: .as_is.code}, without_smbus: {readiness: .without_smbus.readiness, first_boot_restarts_s: .without_smbus.first_boot_restarts_s, warnings: [.without_smbus.steps[] | select(.verdict != "OK") | .name]}}' $d/gate.json
```

- [x] Beklenen: ilk satır `{"verdict":"OK","a":true,"b":true,"c":true,"d":true,"e":true,"f":true}`. İkinci satırda
  `as_is` için `CRITICAL` `driver_install_loop`, `without_smbus` için `WARNING`. `first_boot_restarts_s` ilk
  açılıştaki yeniden başlatmaları saniye olarak listeler, `warnings` de `["drivers_excluded"]` olur.

```
{"verdict":"OK","a":true,"b":true,"c":true,"d":true,"e":true,"f":true}
{"as_is":{"readiness":"CRITICAL","code":"driver_install_loop"},"without_smbus":{"readiness":"WARNING","first_boot_restarts_s":[60.6,91,136,170.8],"warnings":["drivers_excluded"]}}
```

## 5. İki soru

> Binlerce VM arasında sorunlu olanı ve nedenini hemen bulabiliyor muyum?

Cevap: Evet, needs attention kısmı muhteşem çalışıyor.

> Giriş çıkışların yeterince sıkı olduğuna güveniyor muyum?

Cevap: Evet, bence epeyce güzel olmuş
