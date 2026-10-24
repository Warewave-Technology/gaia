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
export GAIA_BAO_TOKEN=dev-root                 # gaia apply için
```

`gaia server`'ın worker'ları AppRole ile girer (`gaia` rolü, sadece `kv/platform/*` okuma ve Pulumi anahtarı):

```
unset GAIA_BAO_TOKEN
export GAIA_BAO_ADDR=http://127.0.0.1:18200 GAIA_BAO_ROLE_ID=gaia-dev GAIA_BAO_SECRET_ID=gaia-dev-secret-id
```

`make dev-oidc` ayrıca Keycloak'ı başlatır (`http://localhost:18081`, admin / dev): realm `gaia-dev`, istemci `gaia` (secret OpenBao'da `kv/platform/gaia/oidc`), kullanıcılar `ayse` / `ayse-dev-password` (team-platform) ve `mehmet` / `mehmet-dev-password` (team-web). AuthConfig'te sağlayıcı adı `dev-sso`, issuer `http://localhost:18081/realms/gaia-dev`.

Portlar, makinedeki başka servislerle çakışmasın diye standart portların 10000 fazlasıdır. Ortam sadece yerel kullanım içindir; sırları sabittir ve bütün portlar `127.0.0.1`'e bağlıdır.

`make dev-git` bir Git sunucusu (Gitea) başlatır: `http://localhost:13000` (`gaia-admin` / `dev-git-password`), ssh `127.0.0.1:12222`. GitHub'ın yerine uçtan uca test için kullanılır; webhook'ları GitHub'ınkiyle aynı `X-Hub-Signature-256` başlığını taşır. Konteynerden makinedeki Gaia'ya `http://host.docker.internal:8080` ile ulaşılır.

Uçtan uca senaryo (Faz 3):

1. Gitea'da bir repo, Gaia için yazma yetkili bir deploy key ve `push` webhook'u (`/api/v1/webhooks/github`, bir sır).
2. OpenBao'da:
   - `kv/platform/gaia/git`: deploy key'in `private_key`'i ve `ssh-keyscan -p 12222 127.0.0.1` çıktısı (`known_hosts`)
   - `kv/platform/gaia/signing-key`: Gaia'nın commit anahtarı (`private_key`)
   - `kv/platform/gaia/github-webhook`: webhook sırrı (`secret`)
3. Gaia:

   ```
   gaia server --repo ssh://git@127.0.0.1:12222/<sahip>/<repo>.git --ref main \
     --repo-credentials bao://kv/platform/gaia/git --signing-key bao://kv/platform/gaia/signing-key \
     --webhook-secret bao://kv/platform/gaia/github-webhook --refresh 1h
   ```

4. İmzalı bir commit push'lanır → webhook → Change. Onay, revert (`gaia revert`; Gaia ssh ile push'lar) → webhook → isteyenin onaylayamadığı yeni Change.
