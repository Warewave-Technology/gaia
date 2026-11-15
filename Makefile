.PHONY: build test vet fmt shellcheck check clean

build:
	go build -o bin/imgtool ./cmd/imgtool
	go build -o bin/vsphere-probe ./cmd/vsphere-probe
	go build -o bin/gaia-backup ./cmd/gaia-backup

test:
	go test ./...

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run: gofmt -w ."; exit 1; }

shellcheck:
	shellcheck scripts/*.sh spikes/*/*.sh lab/*/*.sh

check: fmt vet test build shellcheck

clean:
	rm -rf bin
