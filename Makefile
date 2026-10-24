VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/Warewave-Technology/gaia/internal/version.Version=$(VERSION)

.PHONY: build test itest generate lint ee dev-up dev-down dev-oidc dev-git dev-mail

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

dev-down:
	docker compose -f deploy/dev/compose.yaml --profile oidc --profile git --profile mail down -v
