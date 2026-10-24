VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/Warewave-Technology/gaia/internal/version.Version=$(VERSION)

.PHONY: build test itest generate lint ee

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
