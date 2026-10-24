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
bao POST transit/keys/gaia-auth '{}'
bao POST kv/data/platform/postgres '{"data":{"username":"gaia_owner","password":"dev"}}'
bao POST kv/data/platform/pulumi-state '{"data":{"username":"gaia_owner","password":"dev"}}'
bao POST kv/data/platform/seaweedfs '{"data":{"access_key":"gaia-dev","secret_key":"gaia-dev-secret"}}'
bao POST kv/data/platform/ipam '{"data":{"token":"dev-ipam-token"}}'
bao POST kv/data/platform/gaia/oidc '{"data":{"client_secret":"gaia-dev-oidc-secret"}}'

# Gaia's AppRole: reads the platform's secrets and uses the Pulumi key.
bao PUT sys/policies/acl/gaia '{"policy":"path \"kv/data/platform/*\" { capabilities = [\"read\"] }\npath \"transit/encrypt/pulumi\" { capabilities = [\"update\"] }\npath \"transit/decrypt/pulumi\" { capabilities = [\"update\"] }\npath \"transit/keys/pulumi\" { capabilities = [\"read\"] }\npath \"transit/encrypt/gaia-auth\" { capabilities = [\"update\"] }\npath \"transit/decrypt/gaia-auth\" { capabilities = [\"update\"] }"}'
bao POST sys/auth/approle '{"type":"approle"}'
bao POST auth/approle/role/gaia '{"token_policies":["gaia"],"token_ttl":"1h","token_max_ttl":"24h"}'
bao POST auth/approle/role/gaia/role-id '{"role_id":"gaia-dev"}'
bao POST auth/approle/role/gaia/custom-secret-id '{"secret_id":"gaia-dev-secret-id"}'

export AWS_ACCESS_KEY_ID=gaia-dev AWS_SECRET_ACCESS_KEY=gaia-dev-secret AWS_DEFAULT_REGION=us-east-1
S3="aws --endpoint-url http://seaweedfs:8333 s3api"
until $S3 list-buckets >/dev/null 2>&1; do sleep 1; done
$S3 create-bucket --bucket gaia-run-records --object-lock-enabled-for-bucket >/dev/null 2>&1 || true
$S3 create-bucket --bucket gaia-run-logs >/dev/null 2>&1 || true
echo "gaia-dev: hazır"
