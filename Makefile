.PHONY: build test vet fmt shellcheck evidence check dist clean web web-test gen-api-ts web-drift

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

# The web UI (T20, heimdall's stack): its build in web/dist is committed and embedded, so `build` and `dist` need no
# Node; these targets do (Node 22).
web:
	cd web && npm ci --no-audit --no-fund && npm run build

web-test:
	cd web && npm test

gen-api-ts:
	cd web && npm run gen-api

# CI: the TypeScript API types and the committed build are what the sources make.
web-drift: gen-api-ts web
	git diff --exit-code -- web/src/api/schema.d.ts web/dist
	@test -z "$$(git status --porcelain -- web/dist)" || { git status --porcelain -- web/dist; echo "web/dist is stale: make web and commit it"; exit 1; }

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
