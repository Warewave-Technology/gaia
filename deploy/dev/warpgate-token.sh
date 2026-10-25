#!/bin/sh
# Geliştirme Warpgate'inde Gaia için bir admin API token'ı açar ve
# OpenBao'ya (kv/platform/warpgate/gaia-dev) yazar. Tekrar çalıştırılabilir:
# her seferinde yeni bir token yazılır.
set -eu
B=https://127.0.0.1:18888/@warpgate
JAR=$(mktemp)
trap 'rm -f "$JAR"' EXIT
until curl -sfk -c "$JAR" "$B/api/auth/login" -H 'content-type: application/json' \
  -d '{"username":"admin","password":"Dev-Warpgate-1"}' >/dev/null; do sleep 2; done
TOKEN=$(curl -sfk -b "$JAR" "$B/api/profile/api-tokens" -H 'content-type: application/json' \
  -d '{"label":"gaia-dev","expiry":"2099-01-01T00:00:00Z"}' | sed -n 's/.*"secret":"\([^"]*\)".*/\1/p')
[ -n "$TOKEN" ] || { echo "warpgate: no token" >&2; exit 1; }
docker compose -f deploy/dev/compose.yaml exec -T openbao env BAO_TOKEN=dev-root BAO_ADDR=http://127.0.0.1:8200 \
  bao kv put -mount=kv platform/warpgate/gaia-dev token="$TOKEN" insecure=true >/dev/null
echo "Warpgate hazır: https://localhost:18888 (token kv/platform/warpgate/gaia-dev)"
