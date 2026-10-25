VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/Warewave-Technology/gaia/internal/version.Version=$(VERSION)

.PHONY: build web web-check ui-demo test itest generate lint ee package image dev-up dev-down dev-oidc dev-git dev-mail dev-ad dev-zabbix dev-warpgate

build: web
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test ./...

# The user interface (web/), built into internal/web/dist and embedded in
# the binary.
web/node_modules/.package-lock.json: web/package-lock.json
	cd web && npm ci

web: web/node_modules/.package-lock.json
	cd web && npm run build

web-check: web/node_modules/.package-lock.json
	cd web && npm run typecheck && npm run lint && npm test

# A whole server for trying the UI against a real API: the lab catalog in
# a test repository, a change waiting for approval, sign-in with password
# and TOTP, runs on fake systems. It prints the enrolment link.
ui-demo: web
	GAIA_INTEGRATION=1 GAIA_UI_DEMO=$${GAIA_UI_DEMO:-1h} go test ./internal/server -run TestUIDemo -v -count=1 -timeout 0

# Integration tests start containers (docker).
itest:
	GAIA_INTEGRATION=1 go test ./...

generate:
	cd internal/store && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate

lint:
	go vet ./...
	golangci-lint run

# The container image (design 13.3).
IMAGE ?= localhost/gaia/gaia:$(VERSION)
image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

# .deb and .rpm for linux/amd64 and linux/arm64 in dist/ (design 13.2).
NFPM := go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0
PKG_VERSION ?= $(shell sh deploy/package/version.sh $(VERSION))
package: web
	for arch in amd64 arm64; do \
	  rm -rf dist/stage && CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/stage/ ./cmd/... || exit 1; \
	  for fmt in deb rpm; do \
	    GOARCH=$$arch VERSION=$(PKG_VERSION) $(NFPM) package -f deploy/package/nfpm.yaml -p $$fmt -t dist/ || exit 1; \
	  done; \
	done; \
	rm -rf dist/stage

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
	# Samba'nın kendi ürettiği sertifikanın seri numarası bazen negatif çıkıyor; Go bunu
	# reddeder. Pozitif çıkana kadar yeniden ürettirilir.
	until docker compose -f deploy/dev/compose.yaml exec -T samba sh -c 'test -s /var/lib/samba/private/tls/ca.pem' 2>/dev/null; do sleep 2; done
	until docker compose -f deploy/dev/compose.yaml exec -T samba cat /var/lib/samba/private/tls/ca.pem | openssl x509 -noout -serial | grep -q '=[0-7]'; do \
	  docker compose -f deploy/dev/compose.yaml exec -T samba sh -c 'rm -f /var/lib/samba/private/tls/*.pem'; \
	  docker compose -f deploy/dev/compose.yaml restart samba; \
	  until docker compose -f deploy/dev/compose.yaml exec -T samba sh -c 'test -s /var/lib/samba/private/tls/ca.pem' 2>/dev/null; do sleep 2; done; \
	done
	docker compose -f deploy/dev/compose.yaml cp deploy/dev/ad-seed.sh samba:/tmp/ad-seed.sh
	docker compose -f deploy/dev/compose.yaml exec -T samba sh /tmp/ad-seed.sh
	docker compose -f deploy/dev/compose.yaml exec -T openbao env BAO_TOKEN=dev-root BAO_ADDR=http://127.0.0.1:8200 \
	  bao kv put -mount=kv platform/ad/gaia-dev username=gaia-reader@gaia.dev password='Dev-gaia-reader-Password-1!' \
	  server_name=dc1.gaia.dev ca="$$(docker compose -f deploy/dev/compose.yaml exec -T samba cat /var/lib/samba/private/tls/ca.pem)"
	docker compose -f deploy/dev/compose.yaml exec -T openbao env BAO_TOKEN=dev-root BAO_ADDR=http://127.0.0.1:8200 \
	  bao kv put -mount=kv platform/ad/gaia-dev-writer username=gaia-writer@gaia.dev password='Dev-gaia-writer-Password-1!' \
	  server_name=dc1.gaia.dev ca="$$(docker compose -f deploy/dev/compose.yaml exec -T samba cat /var/lib/samba/private/tls/ca.pem)"

dev-zabbix:
	docker compose -f deploy/dev/compose.yaml --profile zabbix up -d zabbix-db zabbix-server zabbix-web

# Gaia'nın admin API token'ı OpenBao'ya (kv/platform/warpgate/gaia-dev) yazılır.
dev-warpgate:
	docker compose -f deploy/dev/compose.yaml --profile warpgate up -d warpgate
	sh deploy/dev/warpgate-token.sh

dev-down:
	docker compose -f deploy/dev/compose.yaml --profile oidc --profile git --profile mail --profile ad --profile zabbix --profile warpgate down -v
