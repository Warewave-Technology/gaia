.PHONY: build test vet fmt shellcheck evidence limitations check dist release clean web web-test gen-api-ts web-drift

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
	shellcheck -s sh packaging/deb/postinst packaging/deb/prerm packaging/deb/postrm

# The evidence specification's conformance vectors, v0 evidence and a key rotation, through the CLI (T13).
evidence: build
	scripts/check-evidence.sh bin/gaia

# Every item of docs/limitations.md names why it waits (T22).
limitations:
	scripts/check-limitations.sh

check: fmt vet test build shellcheck evidence limitations

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

# The release packages for docs/install.md (T22, scripts/release.sh): the server's .deb, the command line for
# workstations, BUILDINFO.txt and SHA256SUMS in dist/. `release` also signs SHA256SUMS (the release key in ssh-agent);
# it refuses a version that is not HEAD's tag vVERSION on a clean tree.
dist:
	scripts/release.sh dist

release: dist
	scripts/release.sh sign

clean:
	rm -rf bin dist
