#!/usr/bin/env bash
# Builds a realistic raw disk image for Gaia's Phase 1 spikes.
#
# The image is an ext4 filesystem with a mix that behaves like a real server
# disk for dedup and compression:
#   ~10%  OS-like files (a copy of this machine's /usr)
#   ~25%  compressible text (application logs)
#   ~15%  incompressible data (database/media-like)
#   ~5%   written then deleted (stale blocks, as on any real disk)
#   rest  never written (zeros)
#
# Usage (as root, on the lab VM):  SIZE_GB=30 OUT=/var/lib/gaia/test.img scripts/make-test-image.sh
# Env: OS_DIR (/usr), the tree copied as the OS-like part; it must fit in 10% of the image.
set -euo pipefail

SIZE_GB="${SIZE_GB:-30}"
OUT="${OUT:-/var/lib/gaia/test.img}"
IMGTOOL="${IMGTOOL:-$(dirname "$0")/../bin/imgtool}"
OS_DIR="${OS_DIR:-/usr}"
CHUNK_MIB=256

[[ $EUID -eq 0 ]] || { echo "must run as root (loop mount)"; exit 1; }
[[ -x "$IMGTOOL" ]] || { echo "imgtool not found at $IMGTOOL (run: make build)"; exit 1; }
[[ ! -e "$OUT" ]] || { echo "$OUT already exists, refusing to overwrite"; exit 1; }

total_mib=$((SIZE_GB * 1024))
os_mib=$(du -sm "$OS_DIR" | cut -f1)
# The mix above is only true if the OS part stays near its share; a larger tree would also overflow small images.
((os_mib <= total_mib / 10)) || { echo "$OS_DIR is ${os_mib} MiB, more than 10% of ${SIZE_GB} GiB; raise SIZE_GB or set OS_DIR"; exit 1; }
mkdir -p "$(dirname "$OUT")"
truncate -s "${SIZE_GB}G" "$OUT"
mkfs.ext4 -F -q -L gaia-test "$OUT"

mnt="$(mktemp -d)"
trap 'umount "$mnt" 2>/dev/null || true; rmdir "$mnt"' EXIT
mount -o loop "$OUT" "$mnt"

# Every generated file gets its own seed, across all fill calls: with per-call seeds the
# deleted files would repeat the live database files byte for byte and dedup against them.
seed=0
fill() { # fill KIND TARGET_MIB DIR
	local kind=$1 target=$2 dir=$3 i=0 done_mib=0
	mkdir -p "$dir"
	while ((done_mib < target)); do
		local n=$((target - done_mib < CHUNK_MIB ? target - done_mib : CHUNK_MIB))
		seed=$((seed + 1))
		"$IMGTOOL" gen -out "$dir/$kind-$i.dat" -kind "$kind" -size "${n}MiB" -seed "$seed" >/dev/null
		done_mib=$((done_mib + n))
		i=$((i + 1))
	done
	echo "  $kind: ${done_mib} MiB in $i files"
}

echo "building ${SIZE_GB} GiB test image at $OUT"
echo "  os: copying $OS_DIR (${os_mib} MiB)"
cp -a "$OS_DIR" "$mnt/os-usr"
fill logs $((total_mib * 25 / 100)) "$mnt/var/log/app"
fill random $((total_mib * 15 / 100)) "$mnt/var/lib/db"
fill random $((total_mib * 5 / 100)) "$mnt/tmp/deleted"
rm -rf "$mnt/tmp/deleted"
sync
df -h "$mnt" | tail -1
umount "$mnt"
trap - EXIT
rmdir "$mnt"
"$IMGTOOL" hash -file "$OUT"
echo "done: $OUT"
