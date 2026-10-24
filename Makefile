VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/Warewave-Technology/gaia/internal/version.Version=$(VERSION)

.PHONY: build test lint ee

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test ./...

lint:
	go vet ./...
	golangci-lint run

ee:
	ansible-builder build -f ee/execution-environment.yml -t localhost/gaia/ee-default:dev
