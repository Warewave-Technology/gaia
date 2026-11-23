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

- [ ] İlk satır `Good "gaia-release" signature for release@gaia`, her dosya `OK`.

```
(çıktıyı buraya yapıştır)
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

- [ ] `version` `0.2.0 (...)`, `schema_version` `16`, `signin_problem` yok; kit `confirmed`.
- [ ] Yükseltmeden önceki `gaia login` oturumun çalışmadı (kılavuz: "Every session ends at the upgrade").

```
(çıktıyı buraya yapıştır)
```

Not:

## 2. SSO: kılavuz adım 9b, lab Keycloak'ı

gaia-lab'de (root): Keycloak'ı başlat, sunucunun adresini istemciye ekle, kendine bir admin kullanıcısı aç:

```bash
/root/gaia/lab/lab-vm/t26-keycloak.sh start && /root/gaia/lab/lab-vm/t26-keycloak.sh redirect https://gaia-t23-b.lab.test:8443 && /root/gaia/lab/lab-vm/t26-keycloak.sh user yigit gaia-admins
```

İlk parolan `/root/t26-keycloak/secrets.json` içinde (`.users.yigit.password`); ilk girişte değiştirir ve OTP'yi
telefonuna eklersin. İstemcinin secret'ı aynı dosyada (`.client_secret`). Bunları ben görmüyorum.

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

- [ ] SSO açık, roller eşlenmiş; `yigit` SSO kullanıcısı olarak listede, rolü `admin`.

```
(çıktıyı buraya yapıştır)
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

- [ ] `fill` kataloğu yazdı (`"vms": 5000, "hosts": 100`) ve `status table recomputed` satırını gösterdi.

```
(çıktıyı buraya yapıştır)
```

## 4. Kabul satırları

### 4.1 Erişilemeyen bir host, bütün VM'leriyle **tek** grup

Senin sunucunda, gaia-lab'den (root) sunucunun SSH'ını kes:

```bash
iptables -I INPUT -s 192.168.1.122 -p tcp --dport 22 -j DROP
```

Bir sonraki yedek denemesinden sonra (en geç politikanın aralığı kadar) Overview → **Needs attention**'a bak. Mac'te:

```bash
gaia api GET '/api/v1/views/attention?limit=50' | jq -c '.groups[] | {status, code, scope: .scope.name, count, reach}'
```

Sonra kuralı kaldır:

```bash
iptables -D INPUT -s 192.168.1.122 -p tcp --dport 22 -j DROP
```

Ölçek kurulumunda aynısı: katalogdaki erişilemeyen hostlar (aynı komut, ölçek kurulumunun oturumuyla).

- [ ] Ekranda host için **bir** grup (`host_unreachable`, `gaia-lab`); "Show these VMs" hostun bütün VM'lerini
  listeliyor. Başka nedenle listelenen VM varsa grup "affects N VMs · … here, … under other causes" diyor.

```
(çıktıyı buraya yapıştır)
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

Sonra aynısını `-slots 4` ile (`~/t29-capacity-4`). Betik, ekrandaki sayıları (Capacity: tests in 24 h, coverage, "will
expire") o 6 saatte biten testlerle ve süresi gerçekten dolan kanıtlarla karşılaştırıyor: test sayısı %20 (ya da 2
test) içinde, süresi dolacağı söylenen VM'ler de dolanlarla aynı.

- [ ] 1 slot: `"verdict": "OK"`.
- [ ] 4 slot: `"verdict": "OK"`.

```
(1 slot check çıktısı)
```

```
(4 slot check çıktısı)
```

Not:

### 4.3 Yeni bir tarayıcıdan giriş bildiriliyor

Başka bir tarayıcıda (ya da gizli pencerede) admin olarak gir. gaia-lab'de (root):

```bash
/root/gaia/lab/lab-vm/t23-sink.sh list 10
```

- [ ] `new_device_signin` olayı kanala geldi.

```
(çıktıyı buraya yapıştır)
```

### 4.4 Boşta kalan oturum bitiyor

Bir tarayıcıda gir. Mac'te oturumları listele, sonra sekmeyi açık bırakıp 15 dakika hiçbir şeye dokunma (açık sekme
arka planda istek atsa da oturum uzamamalı; ADR-008 §5), ve aynı komutu yeniden çalıştır. Sonra tarayıcıda bir sayfaya
tıkla.

```bash
gaia api GET /api/v1/sessions | jq -c '.[] | {id, username, kind, method, last_active_at, last_seen_at, user_agent: .user_agent[0:40]}'
```

- [ ] İkinci listede tarayıcı oturumu yok (`last_seen_at` arka plan isteklerinde ilerlese de `last_active_at`
  ilerlemedi); tarayıcı giriş sayfasına döndü.

```
(iki listeyi buraya yapıştır)
```

### 4.5 Step-up'sız yıkıcı bir işlem reddediliyor

Önce kılavuzun 9. adımıyla bir deneme kullanıcısı ekle (`gaia users add username=t29-silinecek role=auditor`), id'sini al.
Sonra adım istemeyen bir istemciyle (`curl`, CLI oturumunun token'ıyla; `gaia` komutu step-up'ı kendisi istediği için
burada kullanılmıyor) onu silmeyi dene:

```bash
curl -sk -H "Authorization: Bearer $(jq -r .token ~/Library/Application\ Support/gaia/cli.json)" -X DELETE https://gaia-t23-b.lab.test:8443/api/v1/users/<id> ; echo; gaia users list -json | jq -c '[.[].username]'
```

- [ ] Cevap `step_up_required`; kullanıcı listede duruyor. (Sonra `gaia users set <id> disabled=true` ile kapatabilirsin;
  `gaia` step-up'ı tarayıcıda onaylatır.)

```
(çıktıyı buraya yapıştır)
```

### 4.6 Keycloak durunca break-glass çalışıyor ve bildiriliyor

gaia-lab'de (root): `/root/gaia/lab/lab-vm/t26-keycloak.sh stop`. SSO kullanıcısıyla açık bir oturumda yıkıcı bir işlem
dene (reddedilmeli: `idp_unreachable`). Sonra yerel admin (break-glass) olarak gir. gaia-lab'de:

```bash
/root/gaia/lab/lab-vm/t23-sink.sh list 10
```

Sonra Keycloak'ı yeniden başlat: `/root/gaia/lab/lab-vm/t26-keycloak.sh start`.

- [ ] SSO oturumunda yıkıcı işlem `idp_unreachable` ile reddedildi; break-glass girişi çalıştı; `breakglass_signin`
  kanala geldi.

```
(çıktıyı buraya yapıştır)
```

### 4.7 5,000 VM'lik kurulumun Overview'u küçük olan kadar hızlı

İki kurulumun Overview'unu aç, aynı soruları sor (en kötü durumdaki VM, nedeni, ne zamandan beri). Mac'te, her
kurulumun oturumuyla:

```bash
for i in 1 2 3; do /usr/bin/time -p gaia api GET '/api/v1/views/fleet' >/dev/null; done 2>&1 | grep real
```

- [ ] İkisi de ekranda 2 saniye içinde etkileşimli; sorularının cevabını aynı hızla buldun.

```
(iki kurulumun çıktısı)
```

Not:

### 4.8 Windows migration check'i (T28, ADR-005 F)

gaia-lab'de (root), T29 hazırlığındaki runner'ı çalıştır (`/root/gaia`'nın `main`'i: `v0.2.0`'ın ürün kodu ve sonraki lab betikleri):

```bash
cd /root/gaia && systemd-run --unit=gaia-t21 --collect -E HOME=/root lab/lab-vm/t21-migration.sh 192.168.1.29 && journalctl -fu gaia-t21
```

- [ ] `gaia-t11-win` as-is: `CRITICAL` `driver_install_loop` (smbus); "without smbus": `WARNING`, ilk açılıştaki
  yeniden başlatmalar listeli. Gate `"verdict":"OK"`.

```
(gate satırını buraya yapıştır)
```

## 5. İki soru

> Binlerce VM arasında sorunlu olanı ve nedenini hemen bulabiliyor muyum?

Cevap:

> Giriş çıkışların yeterince sıkı olduğuna güveniyor muyum?

Cevap:
