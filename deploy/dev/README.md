# Yerel geliştirme ortamı

```
make dev-up     # başlatır ve başlangıç ayarlarını yapar
make dev-down   # kapatır, verileri siler
```

| Servis | Adres | Not |
|---|---|---|
| PostgreSQL 17 | `127.0.0.1:15432` | `gaia_owner` / `dev`; veritabanları `gaia`, `ipam`, `pulumi` |
| OpenBao 2.7.1 | `http://127.0.0.1:18200` | Dev modu, root token `dev-root`; `kv/` ve `transit/keys/pulumi` hazır |
| SeaweedFS 4.48 | `http://127.0.0.1:18333` | `gaia-dev` / `gaia-dev-secret`; bucket'lar `gaia-run-records` (Object Lock), `gaia-run-logs` |

Gaia'yı bu ortamla çalıştırmak için:

```
export GAIA_DSN="postgres://gaia_owner:dev@127.0.0.1:15432/gaia?sslmode=disable"
export GAIA_BAO_TOKEN=dev-root
```

Portlar, makinedeki başka servislerle çakışmasın diye standart portların 10000 fazlasıdır. Ortam sadece yerel kullanım içindir; sırları sabittir ve bütün portlar `127.0.0.1`'e bağlıdır.
