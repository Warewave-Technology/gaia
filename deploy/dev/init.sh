#!/bin/sh
# Yerel ortamın başlangıç ayarları; tekrar çalıştırılabilir.
set -eu
apk add --no-cache -q curl aws-cli >/dev/null

BAO=http://openbao:8200
H="X-Vault-Token: dev-root"
until curl -sf $BAO/v1/sys/health >/dev/null; do sleep 1; done

bao() { curl -sf -H "$H" -X "$1" "$BAO/v1/$2" ${3:+-d "$3"} >/dev/null || true; }
bao POST sys/mounts/kv '{"type":"kv","options":{"version":"2"}}'
bao POST sys/mounts/transit '{"type":"transit"}'
bao POST transit/keys/pulumi '{}'
bao POST kv/data/platform/postgres '{"data":{"username":"gaia_owner","password":"dev"}}'
bao POST kv/data/platform/pulumi-state '{"data":{"username":"gaia_owner","password":"dev"}}'
bao POST kv/data/platform/seaweedfs '{"data":{"access_key":"gaia-dev","secret_key":"gaia-dev-secret"}}'
bao POST kv/data/platform/ipam '{"data":{"token":"dev-ipam-token"}}'

export AWS_ACCESS_KEY_ID=gaia-dev AWS_SECRET_ACCESS_KEY=gaia-dev-secret AWS_DEFAULT_REGION=us-east-1
S3="aws --endpoint-url http://seaweedfs:8333 s3api"
until $S3 list-buckets >/dev/null 2>&1; do sleep 1; done
$S3 create-bucket --bucket gaia-run-records --object-lock-enabled-for-bucket >/dev/null 2>&1 || true
$S3 create-bucket --bucket gaia-run-logs >/dev/null 2>&1 || true
echo "gaia-dev: hazır"
