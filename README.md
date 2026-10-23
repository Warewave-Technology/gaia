# Gaia

Gaia, altyapıyı tek bir YAML kontratıyla yöneten bir orkestrasyon servisidir. Kontratta yalnızca katalog referansları bulunur. Gaia bu referansları çözer; provisioning'i Pulumi ile, konfigürasyonu Ansible ile yapar. Diğer açık kaynak bileşenlerle (OpenBao, Zabbix, SeaweedFS, Nexus, Warpgate, IPAM, Active Directory) doğrudan API üzerinden konuşur.

Git doğru kaynaktır: değişiklikler yalnızca Git'ten uygulanır. Arayüz ve CLI görüntüleme, `plan` ve debug için kullanılır.

## Durum

Proje **tasarım aşamasında**. Henüz çalışan bir kod yok.

## Dokümanlar

- [Tasarım dokümanı](docs/design.md): kavramlar, kind listesi, kontrat yapısı, çözümleme ve yürütme, adopt akışı, audit, arayüz/CLI ve karar kaydı.
