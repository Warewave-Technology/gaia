#!/usr/bin/env bash
# The release (T22): versioned packages, their checksums and a signature, made from this repository by one script so
# that anyone can make the same bytes again (scripts/mkpkg writes reproducible archives; the Go build is -trimpath with
# no build ID).
#
# Usage:
#   scripts/release.sh dist [OUT]        the packages and SHA256SUMS in OUT (default dist/), unsigned. Version: the one
#                                        in VERSION when HEAD is its tag vVERSION and the tree is clean, otherwise
#                                        VERSION~dev.<commit time>.g<commit> (".dirty" for a tree with changes), which
#                                        sorts below VERSION for dpkg
#   scripts/release.sh sign [OUT]        signs OUT/SHA256SUMS as SHA256SUMS.sig with the release key (refuses a dev
#                                        version). GAIA_RELEASE_KEY is the key's public half (default
#                                        ~/.ssh/gaia-release-signing.pub); its private half is in ssh-agent (ssh-add)
#   scripts/release.sh verify DIR        what install.md step 2 does: the signature against
#                                        packaging/release/allowed_signers, then every checksum
#   scripts/release.sh reproduce DIR     builds DIR's commit (BUILDINFO.txt) again in a fresh clone of this repository
#                                        and compares the checksums: the same commit and Go give the same bytes
#
# Packages:
#   gaia_<v>_amd64.deb                   the server: /usr/bin/gaia, the units gaia, gaia-sandbox, gaia-convert, the
#                                        converter's Containerfile, the guide (install.md)
#   gaia-<v>-<os>-<arch>.tar.gz          the gaia command alone (the CLI) for linux amd64/arm64 and darwin amd64/arm64
#   BUILDINFO.txt                        version, commit, Go version, SOURCE_DATE_EPOCH
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$here/.." && pwd)"
cd "$repo_root"
NAMESPACE=gaia-release
SIGNER=release@gaia
SIGNERS=packaging/release/allowed_signers

sha256() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

version() {
	local base tag t v
	base=$(tr -d ' \n' <VERSION)
	[[ "$base" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "VERSION: want MAJOR.MINOR.PATCH, have '$base'" >&2; exit 3; }
	tag=$(git describe --tags --exact-match HEAD 2>/dev/null || true)
	if [[ "$tag" == "v$base" && -z "$(git status --porcelain --untracked-files=no)" ]]; then
		echo "$base"
		return
	fi
	t=$(TZ=UTC git log -1 --date=format-local:%Y%m%d%H%M%S --format=%cd)
	v="$base~dev.$t.g$(git rev-parse --short=12 HEAD)"
	[[ -z "$(git status --porcelain --untracked-files=no)" ]] || v+=".dirty"
	echo "$v"
}

maintainer() {
	# The control file's Maintainer: GAIA_MAINTAINER, else the package's own address (Debian wants "Name <address>").
	echo "${GAIA_MAINTAINER:-Gaia maintainers <https://github.com/Warewave-Technology/gaia/issues>}"
}

cmd_dist() {
	local out=${1:-dist} v epoch work
	v=$(version)
	epoch=$(git log -1 --format=%ct)
	rm -rf "$out" && mkdir -p "$out" # only this version's packages: an older one cannot be copied by mistake
	out=$(cd "$out" && pwd)
	work=$(mktemp -d)
	trap 'rm -rf "$work"' RETURN
	# A tree without changes to tracked files is built from a fresh clone of its commit: files git does not track
	# cannot get into the packages, and Go's vcs.modified (which counts untracked files too) stays false.
	if [[ "$v" != *.dirty ]]; then
		git clone -q --no-local "$repo_root" "$work/src"
		git -C "$work/src" checkout -q "$(git rev-parse HEAD)"
		cd "$work/src"
	fi
	local ldflags="-s -w -buildid= -X github.com/Warewave-Technology/gaia/internal/buildinfo.Version=$v"
	go build -trimpath -o "$work/mkpkg" ./scripts/mkpkg

	# The server package.
	local root="$work/deb"
	install -d "$root/usr/bin" "$root/lib/systemd/system" "$root/usr/share/gaia/converter" "$root/usr/share/doc/gaia"
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$ldflags" -o "$root/usr/bin/gaia" ./cmd/gaia
	install -m 0644 packaging/systemd/gaia.service packaging/systemd/gaia-sandbox.service packaging/systemd/gaia-convert.service "$root/lib/systemd/system/"
	install -m 0644 packaging/converter/Containerfile "$root/usr/share/gaia/converter/"
	install -m 0644 docs/install.md "$root/usr/share/doc/gaia/"
	install -m 0644 LICENSE "$root/usr/share/doc/gaia/copyright"
	sed -e "s|@VERSION@|$v|" -e "s|@ARCH@|amd64|" -e "s|@MAINTAINER@|$(maintainer)|" packaging/deb/control.in >"$work/control"
	"$work/mkpkg" deb -root "$root" -control "$work/control" -scripts packaging/deb -mtime "$epoch" -o "$out/gaia_${v}_amd64.deb"

	# The command line for workstations.
	local os arch name
	for os_arch in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
		os=${os_arch%/*} arch=${os_arch#*/} name="gaia-$v-$os-$arch"
		install -d "$work/$name"
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$work/$name/gaia" ./cmd/gaia
		install -m 0644 LICENSE "$work/$name/LICENSE"
		"$work/mkpkg" tgz -root "$work/$name" -prefix "$name" -mtime "$epoch" -o "$out/$name.tar.gz"
	done

	{
		echo "version: $v"
		echo "commit: $(git rev-parse HEAD)"
		echo "go: $(go env GOVERSION)"
		echo "SOURCE_DATE_EPOCH: $epoch"
	} >"$out/BUILDINFO.txt"
	(cd "$out" && sha256 -- *.deb *.tar.gz BUILDINFO.txt >SHA256SUMS)
	cat "$out/SHA256SUMS"
	echo "version $v in $out/"
	cd "$repo_root"
}

cmd_sign() {
	local out=${1:-dist} key=${GAIA_RELEASE_KEY:-$HOME/.ssh/gaia-release-signing.pub} v
	v=$(sed -n 's/^version: //p' "$out/BUILDINFO.txt")
	[[ "$v" != *~dev* ]] || { echo "refusing to sign the dev version $v: tag HEAD v$(tr -d ' \n' <VERSION) and run dist on a clean tree" >&2; exit 3; }
	[[ -f "$key" ]] || { echo "no release key at $key (GAIA_RELEASE_KEY)" >&2; exit 3; }
	grep -qF "$(awk '{print $2}' "$key")" "$SIGNERS" || { echo "$key is not the key in $SIGNERS" >&2; exit 3; }
	rm -f "$out/SHA256SUMS.sig"
	ssh-keygen -Y sign -f "$key" -n "$NAMESPACE" "$out/SHA256SUMS" # writes SHA256SUMS.sig; the private half comes from ssh-agent
	cmd_verify "$out"
}

cmd_verify() {
	local dir=${1:?DIR}
	ssh-keygen -Y verify -f "$SIGNERS" -I "$SIGNER" -n "$NAMESPACE" -s "$dir/SHA256SUMS.sig" <"$dir/SHA256SUMS"
	(cd "$dir" && sha256 -c SHA256SUMS)
}

cmd_reproduce() {
	local dir=${1:?DIR} commit work
	commit=$(sed -n 's/^commit: //p' "$dir/BUILDINFO.txt")
	[[ "$(go env GOVERSION)" == "$(sed -n 's/^go: //p' "$dir/BUILDINFO.txt")" ]] || { echo "UNKNOWN: $dir was built with another Go" >&2; exit 3; }
	work=$(mktemp -d)
	trap 'rm -rf "$work"' RETURN
	git clone -q --no-local "$repo_root" "$work/src"
	git -C "$work/src" checkout -q "$commit"
	# A release's version comes from its tag, which the clone has too.
	(cd "$work/src" && scripts/release.sh dist "$work/out" >/dev/null)
	if diff "$dir/SHA256SUMS" "$work/out/SHA256SUMS"; then
		echo "OK: $commit built again gives the same $(wc -l <"$dir/SHA256SUMS" | tr -d ' ') files"
	else
		echo "CRITICAL: the build of $commit differs"
		exit 2
	fi
}

case "${1:-}" in
dist) cmd_dist "${2:-}" ;;
reproduce) cmd_reproduce "${2:-}" ;;
sign) cmd_sign "${2:-}" ;;
verify) cmd_verify "${2:-}" ;;
*)
	sed -n '2,/^set -euo/p' "$0" | sed '$d'
	exit 3
	;;
esac
