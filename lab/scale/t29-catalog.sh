#!/usr/bin/env bash
# fixture: the T29 scale instance (CLAUDE.md T29: "a second, clean instance gets the T24 synthetic catalog"). Not part
# of the product. The instance is the release's package installed from the guide (docs/install.md steps 3 to 5) on
# the T24 gate VM (gaia-t24-scale, VMID 9108); this fills its database with the full-size T24 catalog (5,000 VMs,
# 100 hosts, 3,000,000 points, seeded failures) before its first start, so its admin's own account stays as made by
# `gaia server init`. The catalog's hosts do not exist: their backups fail as unreachable, which is part of what the
# catalog shows.
#
#   (workstation)  lab/scale/t29-catalog.sh make DIR     builds gen for linux/amd64 into DIR with this script
#   (lab VM, root) DIR/t29-catalog.sh fill               after step 4 (init), before step 5: the catalog into
#                                                        /var/lib/gaia/gaia.db (owned by gaia); then start gaia
# Exit: 0 OK / 3 UNKNOWN (not done).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
DB=/var/lib/gaia/gaia.db

case "${1:-}" in
make)
	dir=${2:?DIR}
	root="$(cd "$here/../.." && pwd)"
	mkdir -p "$dir"
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -C "$root" -trimpath -o "$dir/gen" ./lab/scale/gen
	install -m 0755 "$0" "$dir/t29-catalog.sh"
	sum=sha256sum
	command -v sha256sum >/dev/null || sum="shasum -a 256"
	(cd "$dir" && $sum gen t29-catalog.sh) >"$dir/MANIFEST"
	git -C "$root" rev-parse --short=12 HEAD >"$dir/COMMIT"
	cat "$dir/MANIFEST" "$dir/COMMIT"
	exit 0
	;;
fill) ;;
*)
	sed -n '2,12p' "$0"
	exit 3
	;;
esac
((EUID == 0)) || { echo "run as root"; exit 3; }
(cd "$here" && sha256sum -c MANIFEST >/dev/null) || { echo "UNKNOWN: the files do not match MANIFEST"; exit 3; }
[[ -f "$DB" ]] || { echo "UNKNOWN: no $DB: run install.md step 4 (gaia server init) first"; exit 3; }
free_gb=$(df --output=avail -BG /var/lib | tail -1 | tr -dc 0-9)
((free_gb >= 8)) || { echo "UNKNOWN: ${free_gb} GB free under /var/lib, the catalog wants 8"; exit 3; }
systemctl stop gaia 2>/dev/null || true
t0=$(date +%s)
"$here/gen" -db "$DB" -vms 5000 -hosts 100 -points 3000000 -seed 1 >/root/t29-catalog.json
chown gaia:gaia "$DB"* # gen ran as root (its directory is under /root); the database stays the gaia user's
echo "catalog generated in $(($(date +%s) - t0)) s" >&2
python3 - /root/t29-catalog.json <<'EOF'
import json, sys
c = json.load(open(sys.argv[1]))
print(json.dumps({k: c[k] for k in ("now", "vms", "hosts", "points", "present_points", "jobs", "tests")}))
EOF
systemctl enable --now gaia-sandbox gaia
for ((i = 0; i < 120; i++)); do
	journalctl -u gaia --no-pager -o cat --since "-5min" 2>/dev/null | grep -q 'status table recomputed' && break
	sleep 1
done
journalctl -u gaia --no-pager -o cat --since "-5min" | grep -E 'listening|status table recomputed' | head -3
