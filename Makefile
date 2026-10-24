VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/Warewave-Technology/gaia/internal/version.Version=$(VERSION)

.PHONY: build test itest generate lint ee dev-up dev-down dev-oidc dev-git dev-mail dev-ad

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test ./...

# Integration tests start containers (docker).
itest:
	GAIA_INTEGRATION=1 go test ./...

generate:
	cd internal/store && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate

lint:
	go vet ./...
	golangci-lint run

ee:
	ansible-builder build -f ee/execution-environment.yml -t localhost/gaia/ee-default:dev

dev-up:
	docker compose -f deploy/dev/compose.yaml up -d --wait postgres openbao seaweedfs
	docker compose -f deploy/dev/compose.yaml run --rm init

dev-oidc:
	docker compose -f deploy/dev/compose.yaml --profile oidc up -d keycloak

dev-git:
	docker compose -f deploy/dev/compose.yaml --profile git up -d --wait gitea
	docker compose -f deploy/dev/compose.yaml exec -u git gitea gitea admin user create --admin --username gaia-admin --password dev-git-password --email admin@gaia.dev --must-change-password=false || true

dev-mail:
	docker compose -f deploy/dev/compose.yaml --profile mail up -d mailpit

# Gaia'nın okuma hesabı ve DC'nin sertifikası OpenBao'ya (kv/platform/ad/gaia-dev) yazılır.
dev-ad:
	docker compose -f deploy/dev/compose.yaml --profile ad up -d samba
	until docker compose -f deploy/dev/compose.yaml exec -T samba samba-tool user list >/dev/null 2>&1; do sleep 2; done
	docker compose -f deploy/dev/compose.yaml cp deploy/dev/ad-seed.sh samba:/tmp/ad-seed.sh
	docker compose -f deploy/dev/compose.yaml exec -T samba sh /tmp/ad-seed.sh
	docker compose -f deploy/dev/compose.yaml exec -T openbao env BAO_TOKEN=dev-root BAO_ADDR=http://127.0.0.1:8200 \
	  bao kv put -mount=kv platform/ad/gaia-dev username=gaia-reader@gaia.dev password='Dev-gaia-reader-Password-1!' \
	  server_name=dc1.gaia.dev ca="$$(docker compose -f deploy/dev/compose.yaml exec -T samba cat /var/lib/samba/private/tls/ca.pem)"

dev-down:
	docker compose -f deploy/dev/compose.yaml --profile oidc --profile git --profile mail --profile ad down -v
