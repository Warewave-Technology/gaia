#!/usr/bin/env bash
# Prepares the gaia-lab Debian 12 VM: Go, Kopia, govc, qemu-img and the Gaia tools.
# Run as root inside the VM, from a checkout of the repository.
#
# Every tool is pinned to a version and checksum, so two labs built from the same commit
# measure with the same tools.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
# The Go version comes from go.mod, so the lab builds with what CI builds with.
GO_VERSION="${GO_VERSION:-$(awk '$1 == "go" { print $2 }' "$repo_root/go.mod")}"
# govc matches the govmomi version in go.mod.
GOVC_VERSION="v0.56.2"
GOVC_SHA256="12889f30e42e868487041f6628e9789606c80e13a194fb37c099abffc92d5a59"
KOPIA_VERSION="0.23.1"
KOPIA_SHA256="3998c96b2db3410880ec8f6723f3c127248915896c22e6b882b352693230253a"

[[ $EUID -eq 0 ]] || { echo "must run as root"; exit 1; }
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# A fresh cloud VM upgrades its packages through cloud-init on first boot; wait for that
# instead of failing on the apt lock (seen in T1).
if command -v cloud-init >/dev/null; then
	echo "cloud-init: $(cloud-init status --wait 2>&1 | tail -1)"
fi
aptq() { apt-get -o DPkg::Lock::Timeout=600 "$@"; }

aptq update -q
aptq install -y -q ca-certificates curl git jq make qemu-utils e2fsprogs fuse3

# Go (Debian's packaged Go is too old for this module). go.dev publishes the checksums.
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "go${GO_VERSION} "; then
	go_sha=$(curl -fsSL "https://go.dev/dl/?mode=json&include=all" |
		jq -r --arg f "go${GO_VERSION}.linux-amd64.tar.gz" '.[].files[] | select(.filename == $f) | .sha256')
	[[ -n "$go_sha" ]] || { echo "no checksum for go${GO_VERSION} on go.dev"; exit 1; }
	curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o "$tmp/go.tgz"
	echo "$go_sha  $tmp/go.tgz" | sha256sum -c --quiet
	rm -rf /usr/local/go && tar -C /usr/local -xzf "$tmp/go.tgz"
fi
# shellcheck disable=SC2016 # $PATH must expand at login, not now
echo 'export PATH=$PATH:/usr/local/go/bin' >/etc/profile.d/go.sh
# profile.d only reaches login shells; sudo and make need go on the default PATH too.
ln -sf /usr/local/go/bin/go /usr/local/go/bin/gofmt /usr/local/bin/
export PATH=$PATH:/usr/local/go/bin

# Kopia, pinned .deb from the GitHub release.
curl -fsSL "https://github.com/kopia/kopia/releases/download/v${KOPIA_VERSION}/kopia_${KOPIA_VERSION}_linux_amd64.deb" -o "$tmp/kopia.deb"
echo "$KOPIA_SHA256  $tmp/kopia.deb" | sha256sum -c --quiet
aptq install -y -q "$tmp/kopia.deb"

# govc (vSphere CLI), pinned release.
curl -fsSL "https://github.com/vmware/govmomi/releases/download/${GOVC_VERSION}/govc_Linux_x86_64.tar.gz" -o "$tmp/govc.tgz"
echo "$GOVC_SHA256  $tmp/govc.tgz" | sha256sum -c --quiet
tar -C /usr/local/bin -xzf "$tmp/govc.tgz" govc

# Gaia tools, built against the committed go.sum (never regenerated here). Build as the
# checkout's owner: go build stamps the git revision, and git refuses repos owned by another user.
cd "$repo_root"
owner=$(stat -c %U "$repo_root")
if [[ "$owner" == root ]]; then
	make build
else
	runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" make build
fi

echo
go version
kopia --version
govc version
qemu-img --version | head -1
echo "ready. Tools are in $repo_root/bin"
