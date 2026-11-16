.PHONY: build test vet fmt shellcheck evidence check dist clean

build:
	go build -o bin/imgtool ./cmd/imgtool
	go build -o bin/vsphere-probe ./cmd/vsphere-probe
	go build -o bin/gaia-backup ./cmd/gaia-backup
	go build -o bin/gaia ./cmd/gaia

test:
	go test ./...

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run: gofmt -w ."; exit 1; }

shellcheck:
	shellcheck scripts/*.sh spikes/*/*.sh lab/*/*.sh

# The evidence specification's conformance vectors, v0 evidence and a key rotation, through the CLI (T13).
evidence: build
	scripts/check-evidence.sh bin/gaia

check: fmt vet test build shellcheck evidence

# A package for docs/install.md: a static linux gaia (ARCH=amd64 by default), the systemd unit, the guide and checksums.
VERSION := $(shell git describe --always --dirty 2>/dev/null || echo devel)
ARCH ?= amd64
PKG = gaia-$(VERSION)-linux-$(ARCH)
dist:
	rm -rf dist && mkdir -p dist/$(PKG) # only the newest package: an older one cannot be copied by mistake
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -trimpath -o dist/$(PKG)/gaia ./cmd/gaia
	cp packaging/systemd/gaia.service packaging/systemd/gaia-sandbox.service docs/install.md LICENSE dist/$(PKG)/
	cd dist/$(PKG) && (sha256sum gaia gaia.service gaia-sandbox.service install.md LICENSE 2>/dev/null || shasum -a 256 gaia gaia.service gaia-sandbox.service install.md LICENSE) > SHA256SUMS
	COPYFILE_DISABLE=1 tar --no-xattrs -C dist -czf dist/$(PKG).tar.gz $(PKG)
	@echo dist/$(PKG).tar.gz

clean:
	rm -rf bin dist
