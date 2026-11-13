#!/usr/bin/env bash
# Prepares the gaia-lab Debian 12 VM: Go, Kopia, govc, qemu-img and the Gaia tools.
# Run as root inside the VM, from a checkout of the repository.
set -euo pipefail

GO_VERSION="${GO_VERSION:-1.24.7}"

apt-get update -q
apt-get install -y -q ca-certificates curl gnupg git jq make qemu-utils e2fsprogs

# Go (Debian's packaged Go is too old for this module).
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "go${GO_VERSION} "; then
	curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz
	rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz && rm /tmp/go.tgz
fi
echo 'export PATH=$PATH:/usr/local/go/bin' >/etc/profile.d/go.sh
export PATH=$PATH:/usr/local/go/bin

# Kopia from its signed apt repository.
install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://kopia.io/signing-key | gpg --dearmor --yes -o /etc/apt/keyrings/kopia-keyring.gpg
echo "deb [signed-by=/etc/apt/keyrings/kopia-keyring.gpg] http://packages.kopia.io/apt/ stable main" \
	>/etc/apt/sources.list.d/kopia.list
apt-get update -q && apt-get install -y -q kopia

# govc (vSphere CLI) from the govmomi release.
curl -fsSL https://github.com/vmware/govmomi/releases/latest/download/govc_Linux_x86_64.tar.gz |
	tar -C /usr/local/bin -xz govc

# Gaia tools. go.sum is generated here, where the Go checksum database is reachable.
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$repo_root"
go mod tidy
make build

echo
go version
kopia --version
govc version
echo "ready. Tools are in $repo_root/bin"
