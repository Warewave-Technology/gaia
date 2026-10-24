# Gaia

Gaia, altyapıyı tek bir YAML kontratıyla yöneten bir orkestrasyon servisidir.
- Kontratta yalnızca katalog referansları bulunur. Gaia bu referansları çözer.
- Provisioning'i Pulumi ile, konfigürasyonu Ansible ile yapar.
- Diğer açık kaynak bileşenlerle (OpenBao, Zabbix, SeaweedFS, Nexus, Warpgate, IPAM, Active Directory) doğrudan API üzerinden konuşur.

Git doğru kaynaktır: değişiklikler yalnızca Git'ten uygulanır. Arayüz ve CLI görüntüleme, `plan` ve debug için kullanılır.

## Durum

Proje **PoC aşamasında**. Repo iskeleti hazır; ilk adım katalog motoru ve `gaia plan`. Plan ve adımlar: [docs/poc.md](docs/poc.md).

## Yapı

```
cmd/
  gaia/                CLI (ileride server)
  gaia-ipam/           IPAM servisi; Gaia ona bir provider üzerinden bağlanır
internal/
  catalog/             objeler, yükleme, referans grafı, validasyon
  cel/                 CEL ortamı ve Gaia'nın fonksiyonları
  plan/                kontrat + katalog → plan
  engine/pulumi/       provisioning (Automation API)
  engine/ansible/      konfigürasyon (ansible-runner + EE)
  provider/            proxmox, zabbix, ipam, openbao
  ipam/                gaia-ipam'ın sunucu tarafı
  store/               PostgreSQL erişimi
  runrecord/           değiştirilemez run kaydı
  cli/                 komut ağacı
ee/                    Ansible execution environment tanımı
examples/lab/          Proxmox lab'i için platform reposu ve kontrat
docs/                  tasarım ve PoC planı
```

## Geliştirme

Go 1.27 gerekir.

```bash
make build     # bin/gaia, bin/gaia-ipam
make test
make lint      # go vet + golangci-lint
make ee        # EE imajı (ansible-builder)
```

## Dokümanlar

- [Tasarım dokümanı](docs/design.md): kavramlar, kind listesi, kontrat yapısı, çözümleme ve yürütme, adopt, silme, audit, arayüz/CLI, veritabanı, dağıtım, erişim yönetimi ve karar kaydı.
- [PoC planı](docs/poc.md): sınanacak varsayımlar, başarı kriterleri, kapsam, lab ve adımlar.
