#!/usr/bin/env bash
# fixture: T27 gate (c) (CLAUDE.md T27) on the T24 gate VM (gaia-t24-scale, VMID 9108). Not part of the product.
# A server of its own on the full-size T24 catalog (5,000 VMs, 100 hosts, 3,000,000 points), listening on the lab
# network so the workstation's browser checks (lab/scale/ui-gate.mjs) run against it; its first tick is the start's
# full recompute, whose time is the C11 30 s limit. Its admin signs in with a WebAuthn key in software (softkey), and
# the session's token is written to a file (0600) for ui-gate; it is never printed.
#
#   (workstation)  lab/scale/t27-gate-c.sh make-binaries DIR   builds gaia, gen and softkey for linux/amd64 into DIR
#   (lab VM, root) DIR/t27-gate-c.sh up                        catalog + server on :18450; prints the start recompute
#   (lab VM, root) DIR/t27-gate-c.sh token FILE                signs the admin in again, writes the token to FILE
#   (lab VM, root) DIR/t27-gate-c.sh down                      stops the server and removes its data
set -euo pipefail
UNIT=gaia-t27c
DATA=/var/lib/gaia-t27c
WORK=/root/t27c
PORT=18450
here="$(cd "$(dirname "$0")" && pwd)"

case "${1:-}" in
make-binaries)
	dir=${2:?DIR}
	root="$(cd "$here/../.." && pwd)"
	mkdir -p "$dir"
	rev=$(git -C "$root" rev-parse --short=12 HEAD)
	export GOOS=linux GOARCH=amd64 CGO_ENABLED=0
	go build -C "$root" -trimpath -ldflags "-X github.com/Warewave-Technology/gaia/internal/buildinfo.Version=t27c-$rev" -o "$dir/gaia" ./cmd/gaia
	go build -C "$root" -trimpath -o "$dir/gen" ./lab/scale/gen
	go build -C "$root" -trimpath -o "$dir/softkey" ./lab/tools/softkey
	install -m 0755 "$0" "$dir/t27-gate-c.sh"
	sum=sha256sum
	command -v sha256sum >/dev/null || sum="shasum -a 256"
	(cd "$dir" && $sum gaia gen softkey t27-gate-c.sh) >"$dir/MANIFEST"
	echo "$rev" >"$dir/COMMIT"
	cat "$dir/MANIFEST" "$dir/COMMIT"
	exit 0
	;;
esac
((EUID == 0)) || { echo "run as root"; exit 3; }
fp() { python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("tls_certificate_sha256") or d["result"]["tls_certificate_sha256"])' "$WORK/init.json"; }
signin() {
	"$here/softkey" login -cli-config "$WORK/cli.json" -server "https://localhost:$PORT" -fingerprint "$(fp)" -user admin \
		-password-file "$WORK/admin.pw" -key "$WORK/admin-key.json" >&2
}
token() {
	signin
	(umask 077 && python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["token"])' "$WORK/cli.json" >"$1")
	echo "token written to $1" >&2
}
case "${1:-}" in
down)
	systemctl stop "$UNIT" 2>/dev/null || true
	rm -rf "$DATA" "$WORK"
	fstrim -a 2>/dev/null || true
	exit 0 ;;
token) token "${2:?FILE}"; exit 0 ;;
up) ;;
*) echo "usage: $0 make-binaries DIR | up | token FILE | down"; exit 3 ;;
esac

(cd "$here" && sha256sum -c MANIFEST >/dev/null) || { echo "UNKNOWN: the binaries do not match MANIFEST"; exit 3; }
free_gb=$(df --output=avail -BG /var/lib | tail -1 | tr -dc 0-9)
((free_gb >= 8)) || { echo "UNKNOWN: ${free_gb} GB free under /var/lib, the catalog wants 8"; exit 3; }
systemctl stop "$UNIT" 2>/dev/null || true
rm -rf "$DATA" "$WORK"
install -d -m 0700 "$DATA" "$WORK"
head -c 18 /dev/urandom | base64 | tr -d '/+=' | sed 's/^/t27c catalog /' | install -m 0600 /dev/stdin "$WORK/admin.pw"
head -c 18 /dev/urandom | base64 | install -m 0600 /dev/stdin "$WORK/kit.pass"
"$here/gaia" server init -config "$DATA/server.json" -data-dir "$DATA/data" -listen ":$PORT" -hostname localhost \
	-public-url "https://localhost:$PORT" -admin admin -password-file "$WORK/admin.pw" -kit-passphrase-file "$WORK/kit.pass" -json >"$WORK/init.json"
t0=$(date +%s)
"$here/gen" -db "$DATA/data/gaia.db" -vms 5000 -hosts 100 -points 3000000 -seed 1 >"$WORK/catalog.json"
echo "catalog generated in $(($(date +%s) - t0)) s" >&2
sync; echo 3 >/proc/sys/vm/drop_caches # the start's recompute runs cold, as in T24
systemd-run --unit "$UNIT" --collect -p WorkingDirectory="$DATA" "$here/gaia" server run -config "$DATA/server.json"
for ((i = 0; i < 120; i++)); do
	journalctl -u "$UNIT" --no-pager -o cat 2>/dev/null | grep -q 'status table recomputed' && break
	sleep 1
done
python3 - "$WORK/catalog.json" <<'EOF' >&2
import json, sys
c = json.load(open(sys.argv[1]))
print(json.dumps({k: c[k] for k in ("now", "vms", "hosts", "points", "present_points", "jobs", "tests")}))
EOF
journalctl -u "$UNIT" --no-pager -o cat | grep -E 'listening|status table recomputed' | head -3
token "$WORK/token"
echo "server: https://$(hostname -I | awk '{ print $1 }'):$PORT  token: $WORK/token" >&2
