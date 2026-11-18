# T23 — MVP kabul kontrol listesi

Bu liste Yiğit içindir. Gaia `0.1.1`'i temiz bir sunucuya **yalnız kılavuzdan** (`docs/install.md`) kurarsın, üç VM'i
en az 24 saat korursun, beş senaryoyu uygularsın ve sonunda iki soruyu kendi sözlerinle cevaplarsın.

- Her kutunun yanına saati ve gördüğünü yaz. Bir şey listede yazdığından farklıysa, ya da kılavuzda bir adım belirsizse,
  "Not" satırına yaz: rapor oradan yazılacak.
- Senaryoların her biri hem **ekranda** (Overview ve VM sayfası) hem **bildirimde** görünmeli, doğru durum ve neden
  metniyle. İkisinden biri eksikse o madde geçmez.
- Komutlar kopyala-yapıştır içindir; yalnız `<köşeli parantez>` içini değiştir.

## Ortam

| Ne | Nerede | Kim hazırladı |
|---|---|---|
| Gaia sunucusu A | Proxmox'ta yeni VM `gaia-t23-a` (VMID 9106): Debian 12, 4 vCPU, 8 GiB, 40 GiB sistem diski ve ayrı 80 GiB repository diski | Sen (adım 0) |
| KVM host | `gaia-lab` (192.168.1.30) | Hazır |
| Korunan VM'ler | gaia-lab'de, `lab/lab-vm/t23-guests.sh` ile kuruldu: `gaia-t23-linux2` (Debian 12, BIOS, iki disk), `gaia-t23-uefi` (Debian 12, yalnız UEFI), `gaia-t23-win` (T11'in Windows Server 2022'si, virt-v2v ile KVM'e dönüştürülmüş) | Hazır |
| Bildirim alıcısı | gaia-lab'de test SMTP sunucusu (192.168.1.30:2587) ve webhook alıcısı (https://192.168.1.30:2443), `lab/lab-vm/t23-sink.sh` | Hazır (adım 2'de başlatırsın) |
| Release | `0.1.1` (imzalı), Mac'te `releases/0.1.1/` | Hazır |
| Kayıp sunucu yerine B | Proxmox'ta `gaia-t23-b` (VMID 9107), senaryo 5'te | Sen |

Sunucu B'ye (9105, `gaia-t17-b`) ve gaia-lab'deki `gaia-backup` kullanıcısına **dokunma**: onlar M2'nin yükseltme yolu.
Bu yüzden kılavuzdaki `gaia-backup` yerine host kullanıcısı burada **`gaia-t23-backup`**.

## 0. Sunucu VM'i (Proxmox'ta, root)

Mac'ten fixture betiğini Proxmox'a kopyala:

```bash
scp ~/Desktop/Works/Warewave/projects/gaia/lab/proxmox/t17-servers.sh ~/Desktop/Works/Warewave/projects/gaia/lab/proxmox/find-ip.sh root@192.168.1.99:/root/
```

Proxmox'ta (root) VM'i oluştur, başlat ve adresini al:

```bash
CORES=4 MEMORY=8192 ROOT_GB=40 REPO_GB=80 /root/t17-servers.sh create 9106 gaia-t23-a /root/.ssh/authorized_keys repo && /root/t17-servers.sh start 9106
```

- [ ] Sunucu A'nın adresi: `________________` (aşağıda `<A>`)
- [ ] `ssh gaia@<A>` ile girebiliyorsun.

Repository diskini (seri numarası `gaia-repo`) `/srv/gaia`'ya bağla. Bu kılavuzda yok: repository'nin sunucudan ayrı
bir diskte olması, senaryo 5'te diskin yeni sunucuya taşınabilmesi için. A'da, root olarak:

```bash
dev=/dev/$(lsblk -dno NAME,SERIAL | awk '$2 == "gaia-repo" { print $1 }') && mkfs.ext4 -q -L gaia-repo "$dev" && mkdir -p /srv/gaia && echo 'LABEL=gaia-repo /srv/gaia ext4 defaults,nofail 0 2' >> /etc/fstab && systemctl daemon-reload && mount /srv/gaia && df -h /srv/gaia
```

- [ ] `df` 80 GiB'lık bir diski `/srv/gaia`'da gösteriyor.

## 1. Kurulum: kılavuz adım 1–8b

Kılavuzu baştan uygula. Release dosyaları Mac'te `releases/0.1.1/` altında; adım 2'deki "indir" yerine o dizini kullan.

- [ ] **Adım 2:** `ssh-keygen -Y verify` `Good "gaia-release" signature ...` dedi ve her dosya `OK`.
- [ ] **Adım 3:** paket kuruldu; `gaia version` `gaia 0.1.1 (...)` dedi.
- [ ] **Adım 4:** `gaia server init`'te `-hostname <A>` kullandın. TLS sha256'yı ve trust anchor'ı yazdın; admin parolası
  ve kit passphrase'i şifre yöneticinde.
- [ ] **Adım 5:** `gaia-sandbox` ve `gaia` `active (running)`.
- [ ] **Adım 6:** Mac'e `gaia`'yı kurdun; `gaia login` sha256'yı sordu, adım 4'tekiyle aynıydı; `gaia system` şema `8`.
- [ ] **Adım 7:** `gaia repositories add name=main location=/srv/gaia/main` (kit passphrase'ini sordu).
- [ ] **Adım 8:** `gaia state-backups run` `ok`.
- [ ] **Adım 8b:** kit Mac'te kaydedildi (`gaia kit save`), `gaia kit status` `confirmed`.

Not: ______________________________________________

## 2. Bildirimler: kılavuz adım 8c

gaia-lab'de, root olarak, alıcıyı başlat:

```bash
/root/gaia/lab/lab-vm/t23-sink.sh start 192.168.1.30
```

Alıcının CA sertifikasını (gaia-lab'de `/tmp/t23-sink-ca.crt`) Mac üzerinden sunucuya kopyala:

```bash
scp gaia@192.168.1.30:/tmp/t23-sink-ca.crt /tmp/ && scp /tmp/t23-sink-ca.crt gaia@<A>:/tmp/
```

A'da, root olarak: `install -m 0644 /tmp/t23-sink-ca.crt /etc/gaia/t23-sink-ca.crt`.

Kanalları Mac'ten ekle (kılavuz 8c). SMTP parolası gaia-lab'de `sudo /root/gaia/lab/lab-vm/t23-sink.sh password`:

```bash
GAIA_SMTP_PASSWORD='<parola>' gaia channels add name=mail kind=smtp ca_file=/etc/gaia/t23-sink-ca.crt smtp='{"host":"192.168.1.30","port":2587,"username":"gaia","from":"Gaia T23 <gaia@lab.test>","to":["ops@lab.test"]}'
gaia channels add name=hook kind=webhook ca_file=/etc/gaia/t23-sink-ca.crt webhook='{"url":"https://192.168.1.30:2443/gaia"}'
gaia channels test 1 && gaia channels test 2
```

Gelenleri gaia-lab'de görürsün:

```bash
sudo /root/gaia/lab/lab-vm/t23-sink.sh list
```

- [ ] İki test mesajı listede (`mail` ve `webhook`).
- [ ] İstersen gerçek bir posta kutusuna da bir kanal ekle (kılavuz 8c); zorunlu değil.

## 3. Koruma: kılavuz adım 10 ve 10b

Host gaia-lab, kullanıcısı **`gaia-t23-backup`**: kılavuz 10'daki komutlarda `gaia-backup` yerine onu yaz. `address` 192.168.1.30.

Politikalar, 24 saatte senaryolara yetecek sıklıkta:

```bash
gaia policies add name=t23-linux repository_id=1 backup_interval_s=1800 test_interval_s=3600 keep_last=24 keep_daily=7 keep_weekly=0 rpo_target_s=7200 rto_target_s=900 max_proof_age_s=5400 check_template=linux-boot
gaia policies add name=t23-windows repository_id=1 backup_interval_s=1800 test_interval_s=3600 keep_last=24 keep_daily=7 keep_weekly=0 rpo_target_s=7200 rto_target_s=900 max_proof_age_s=5400 check_template=windows-basic
gaia vms add host_id=1 name=gaia-t23-linux2 policy_id=1
gaia vms add host_id=1 name=gaia-t23-uefi policy_id=1
gaia vms add host_id=1 name=gaia-t23-win policy_id=2
```

- [ ] Üç VM `OK` oldu (Overview ekranı; ilk yedek ve ilk test birkaç dakika sürer, Windows'unki en uzun).
- [ ] VM sayfalarında: noktalar (`gaia-t23-linux2`'de iki disk `vda`, `vdb`), restore testleri ekran görüntüleriyle,
  bundle'lar "signed".
- [ ] `gaia-t23-uefi`'nin testinde makine `efi`, `gaia-t23-win`'inkinde WinRM kontrolü geçti.
- [ ] Her VM için bir `status` bildirimi geldi (`(new) -> UNKNOWN`, sonra `UNKNOWN -> OK`).

Başlangıç saati (24 saat buradan sayılır): `________________`

## 4. Senaryolar 1–4

Her senaryodan sonra durum yeniden `OK` olsun, sonra bir sonrakine geç. gaia-lab komutları root olarak, `gaia`
komutları Mac'ten.

### 4.1 Önyüklenemez bir misafir

gaia-lab'de: `/root/gaia/lab/lab-vm/t23-guests.sh break-boot gaia-t23-linux2` (çalışan misafirde MBR'nin önyükleme
kodu sıfırlanır; misafir çalışmaya devam eder). Sonra `gaia-t23-linux2`'nin sayfasında **Back up now**, yedek bitince
**Test a restore now**.

| Beklenen | Gördüğün |
|---|---|
| Test `CRITICAL`, ekran görüntüsünde boot yok, kontrol: seri konsolda `login:` yok | |
| VM `CRITICAL`, neden: `the latest restore test (N, point M) is CRITICAL: boot ...` | |
| Bildirim (mail + webhook): `gaia-t23-linux2: OK -> CRITICAL`, nedenle | |

Geri al: `/root/gaia/lab/lab-vm/t23-guests.sh fix-boot gaia-t23-linux2`, sonra yine **Back up now** ve **Test a restore now**.

- [ ] VM `OK`, bildirim `gaia-t23-linux2: CRITICAL -> OK`.

### 4.2 Repository'de hasarlı bir içerik

A'da, root olarak, repository'nin en büyük pack dosyasının ortasında bir baytı değiştir:

```bash
f=$(find /srv/gaia/main -path '*/p*' -type f -size +1M -printf '%s %p\n' | sort -n | tail -1 | cut -d' ' -f2) && printf '\xff' | dd of="$f" bs=1 seek=$(( $(stat -c %s "$f") / 2 )) conv=notrunc status=none && echo "$f"
```

Mac'ten tam doğrulama: `gaia repositories verify 1 mode=full`, sonra `gaia verifications list`.

| Beklenen | Gördüğün |
|---|---|
| Doğrulama `critical`, etkilenen nokta(lar) ve içerik ID'leri | |
| Etkilenen VM(ler) `CRITICAL`, neden: `integrity check failed for point(s) [...]` | |
| Bildirimler: `integrity` (`verification N found damage ...`), `quarantine`, ve her etkilenen VM için `status` `OK -> CRITICAL` | |
| `gaia quarantine list` içeriği gösteriyor | |

Etkilenen her VM için **Back up now**:

- [ ] Yedek full, nedeni `full: repository verification N quarantined damaged contents ...`; `full_backup` bildirimi geldi.
- [ ] **Test a restore now** `OK`. VM yine de `CRITICAL` (ADR-007 B4.3: hasarlı nokta durdukça).
- [ ] Hasarlı noktayı unut: `gaia points list vm_id=<id>` ile `failed` noktayı bul, sonra
  `gaia points forget <point id> reason="T23 4.2: storage damage, the next backup is proven"`. VM `OK`, bildirim `CRITICAL -> OK`.

### 4.3 Sandbox durdu

A'da: `systemctl stop gaia-sandbox`. Sonra `gaia-t23-linux2` için **Test a restore now**.

| Beklenen | Gördüğün |
|---|---|
| Test işi `failed`, hata `sandbox helper ...`; bildirim `job_failed`: `restore_test job N of gaia-t23-linux2 failed` | |
| Overview'da VM'in altında not: `restore test N (point M) could not run: ...` (durum hâlâ `OK`) | |
| Kanıt 1,5 saati geçince (`max_proof_age_s`) VM'ler `WARNING`, neden `the proof (test N) is ... old, over the maximum 1h30m0s`; `status` bildirimi | |

`WARNING`'i görmek için 1,5 saate kadar beklemen gerekebilir (testler saatte bir; durdurmadan önceki son kanıt kadar). Sonra A'da: `systemctl start gaia-sandbox`.

- [ ] Sonraki testler `OK`, VM'ler `OK`, bildirim `WARNING -> OK`.

### 4.4 Silinen checkpoint

gaia-lab'de: `/root/gaia/lab/lab-vm/t23-guests.sh delete-checkpoint gaia-t23-linux2`, sonra **Back up now**.

| Beklenen | Gördüğün |
|---|---|
| Yeni nokta `full`, nedeni `full: checkpoint ... of point N is not on the host` | |
| Overview'da not: `the newest backup (point M) is a full backup: checkpoint ... is not on the host`; durum `OK` kalıyor (veri korunuyor) | |
| Bildirim `full_backup`: `gaia-t23-linux2: the backup was full (checkpoint ... is not on the host)` | |

## 5. 24 saat

Hiçbir şeye dokunmadan bırak (senaryolar 1–4 bu sürenin içinde olabilir).

- [ ] Ertesi sabah 07:00'de (sunucunun saati) günlük rapor geldi: her VM'in durumu, Proven RPO / RTO, ledger head.
  Ekini (`gaia-daily-report-<n>.json`) gaia-lab'den sakla: senaryo 5'te dış çapa olacak.
- [ ] 24 saat dolduğunda üç VM `OK`. `gaia jobs list` içinde açıklanamayan `failed` iş yok (senaryoların bilerek
  bozdukları dışında).
- [ ] Overview'da, bakar bakmaz: hangi noktaya dönebileceğin (Proven RPO) ve bunun ne zaman kanıtlandığı.

## 6. Senaryo 5: kaybolan sunucu, recovery kit'ten geri

Kaybetmeden hemen önce Mac'ten bir günlük rapor iste ve ekini sakla (`gaia daily-reports run`;
`t23-sink.sh list`'te görünür).

Proxmox'ta, root olarak: A'yı kapat (güç kesintisi gibi), yeni B'yi oluştur, repository diskini B'ye taşı, A'yı yok et ve B'yi başlat:

```bash
qm stop 9106 --skiplock 1 && CORES=4 MEMORY=8192 ROOT_GB=40 /root/t17-servers.sh create 9107 gaia-t23-b /root/.ssh/authorized_keys && /root/t17-servers.sh move-disk 9106 9107 && /root/t17-servers.sh lose 9106 && /root/t17-servers.sh start 9107
```

B'nin adresi: `________________` (aşağıda `<B>`). Sonra B'de **kılavuz adım 13**'ü uygula: kit Mac'te, passphrase
şifre yöneticinde, repository diski B'de. Diski bağla: adım 0'daki komutu **`mkfs.ext4` olmadan** çalıştır:

```bash
mkdir -p /srv/gaia && echo 'LABEL=gaia-repo /srv/gaia ext4 defaults,nofail 0 2' >> /etc/fstab && systemctl daemon-reload && mount /srv/gaia && ls /srv/gaia/main | head -3
```

B'nin adresi A'nınkinden farklıysa `restore-state`'e `-hostname <B>` ekle (kılavuz söylüyor). gaia-lab ve
alıcıda değişiklik gerekmez.

| Beklenen | Gördüğün |
|---|---|
| `restore-state` `OK` (ya da `WARNING` ve son dakikaların listesi) | |
| Login'den sonra Overview: üç VM, kayıptan önceki durumlarıyla | |
| Ledger, kayıptan önceki günlük rapora karşı doğrulanıyor (kılavuz 13, adım 6) | |
| Kanallar sırları olmadan geldi; kapatıp yeni adla ekledin (kılavuz 13, adım 6); `gaia daily-reports run` sonrası gelen raporda `REBUILT: this server was rebuilt ...` satırı | |
| Sonraki yedekler devam ediyor: incremental, ya da nedeni yazılı bir full | |
| Sonraki restore testleri `OK` | |

## 7. İki soru

Kendi sözlerinle, Türkçe:

1. **"Ekrana baktığımda, o an hangi noktaya dönebileceğimi biliyor muyum?"**

   ______________________________________________________________

2. **"Bunun çalışacağından emin miyim, ve bunu bir denetçiye kanıtlayabilir miyim?"**

   ______________________________________________________________

Bir denetçi gözüyle: `ui-auditor` gibi bir auditor kullanıcısıyla giriş yap ve **Evidence** ekranından export al
(kılavuz 10c), ya da `gaia evidence export-all`. Export'un README'si çevrimdışı doğrulamayı anlatıyor.

## Notlar

| Saat | Adım | Ne oldu |
|---|---|---|
| | | |
