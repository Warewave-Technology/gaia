# Gaia

Gaia, altyapıyı tek bir YAML kontratıyla yöneten bir orkestrasyon servisidir.
- Kontratta yalnızca katalog referansları bulunur. Gaia bu referansları çözer.
- Provisioning'i Pulumi ile, konfigürasyonu Ansible ile yapar.
- Diğer açık kaynak bileşenlerle (OpenBao, Zabbix, SeaweedFS, Nexus, Warpgate, IPAM, Active Directory) doğrudan API üzerinden konuşur.

Git doğru kaynaktır: değişiklikler yalnızca Git'ten uygulanır. Arayüz ve CLI görüntüleme, `plan` ve debug için kullanılır.

## Durum

PoC tamamlandı: bir kontrattan Proxmox'ta VM açılıyor, Zabbix'e kaydediliyor, baseline uygulanıyor, run kaydı hash zinciriyle tutuluyor ve `replicas: 0` ile her şey temizleniyor ([docs/poc.md](docs/poc.md)). Sıradaki işler: [docs/roadmap.md](docs/roadmap.md).

```bash
gaia validate --path examples/lab/platform --path examples/lab/contracts
gaia plan     --path examples/lab/platform --path examples/lab/contracts
```

## Yapı

```
cmd/
  gaia/                CLI ve gaia server
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
  server/              API ve iş kuyruğu (gaia server)
  web/                 arayüzün gömülü derlemesini sunar
web/                   arayüz (React + TypeScript)
ee/                    Ansible execution environment tanımı
examples/lab/          Proxmox lab'i için platform reposu ve kontrat
docs/                  tasarım, PoC planı ve yol haritası
```

## Geliştirme

Go 1.27 ve arayüz için Node.js 24 gerekir.

```bash
make build     # arayüz + bin/gaia, bin/gaia-ipam
make web       # sadece arayüz (web/ → internal/web/dist)
make web-check # arayüzün tip kontrolü, lint ve testleri
make ui-demo   # arayüzü gerçek API'yle denemek için demo sunucu (http://localhost:8090)
make test
make lint      # go vet + golangci-lint
make ee        # EE imajı (ansible-builder)
```

Arayüzü geliştirirken `gaia server` :8080'de çalışırken `cd web && npm run dev` Vite'ı açar; `/api` istekleri sunucuya gider. Passkey'ler AuthConfig'teki `url`'in origin'ine bağlı olduğundan, geliştirmede bu `url` Vite'ın adresi olmalıdır.

## Dokümanlar

- [Tasarım dokümanı](docs/design.md): kavramlar, kind listesi, kontrat yapısı, çözümleme ve yürütme, adopt, silme, audit, arayüz/CLI, veritabanı, dağıtım, erişim yönetimi ve karar kaydı.
- [PoC planı](docs/poc.md): sınanacak varsayımlar, başarı kriterleri, kapsam, lab, adımlar ve sonuç.
- [Yol haritası](docs/roadmap.md): PoC'den sonra ürünün tamamı, fazlar ve sırası.
