#!/usr/bin/env bash
# Phase 1 / Track B: can a Kopia repository be Gaia's store for VM disk images?
#
# Scenario
#   0. baseline: qemu-img convert of the image (the gate's reference speed)
#   1. full backup of the image
#   2. change ~CHANGE_PERCENT of the image, incremental backup
#   3. restore both snapshots and verify them byte for byte (sha256)
#
# Gate (ADR-001, feasibility doc):
#   - both restores are byte-identical to what was backed up
#   - full backup time <= GATE_FACTOR x baseline time (default 2, i.e. Kopia at least half as fast)
#
# Usage:  IMAGE=/var/lib/gaia/test.img WORK=/var/lib/gaia/spike-kopia spikes/kopia/run.sh
# Needs:  kopia, qemu-img, jq, imgtool (make build). Run as root to drop page caches between steps.
set -euo pipefail

IMAGE="${IMAGE:?set IMAGE to the test disk image}"
WORK="${WORK:-/var/lib/gaia/spike-kopia}"
CHANGE_PERCENT="${CHANGE_PERCENT:-1}"
COMPRESSION="${COMPRESSION:-zstd}" # none|zstd|...
GATE_FACTOR="${GATE_FACTOR:-2}"
IMGTOOL="${IMGTOOL:-$(dirname "$0")/../../bin/imgtool}"

export KOPIA_PASSWORD="${KOPIA_PASSWORD:-gaia-spike-not-a-secret}"
export KOPIA_CHECK_FOR_UPDATES=false
CFG="$WORK/kopia.config"
REPO="$WORK/repo"
SRC="$WORK/source.img"
OUT="$WORK/restored.img"
REPORT="$WORK/report.json"

for bin in kopia qemu-img jq; do command -v "$bin" >/dev/null || { echo "missing: $bin"; exit 1; }; done
[[ -x "$IMGTOOL" ]] || { echo "imgtool not found at $IMGTOOL (run: make build)"; exit 1; }
[[ ! -e "$WORK" ]] || { echo "$WORK exists; remove it for a clean run"; exit 1; }
mkdir -p "$WORK"

k() { kopia --config-file="$CFG" "$@"; }
now() { date +%s.%N; }
elapsed() { awk -v a="$(now)" -v b="$1" 'BEGIN { printf "%.3f", a - b }'; }
drop_caches() { if [[ $EUID -eq 0 ]]; then sync && echo 3 >/proc/sys/vm/drop_caches; fi; }
repo_bytes() { du -sb "$REPO" | cut -f1; }
sha() { "$IMGTOOL" hash -file "$1" | jq -r .sha256; }

log() { echo "[$(date +%H:%M:%S)] $*"; }

log "copying image to work dir (the source is never modified)"
cp --sparse=always "$IMAGE" "$SRC"
img_bytes=$(stat -c %s "$SRC")

log "0/3 baseline: qemu-img convert"
drop_caches
t=$(now)
qemu-img convert -O raw "$SRC" "$WORK/baseline.raw"
baseline_s=$(elapsed "$t")
rm -f "$WORK/baseline.raw"

log "creating Kopia repository (filesystem, compression=$COMPRESSION)"
k repository create filesystem --path="$REPO" >/dev/null
k policy set --global --compression="$COMPRESSION" >/dev/null

log "1/3 full backup"
sha_v1=$(sha "$SRC")
drop_caches
t=$(now)
k snapshot create "$SRC" >/dev/null
full_s=$(elapsed "$t")
repo_after_full=$(repo_bytes)
snap_v1=$(k snapshot list "$SRC" --json | jq -r '.[-1].id')

log "2/3 change ${CHANGE_PERCENT}% of the image, incremental backup"
mutate_json=$("$IMGTOOL" mutate -file "$SRC" -percent "$CHANGE_PERCENT")
sha_v2=$(sha "$SRC")
drop_caches
t=$(now)
k snapshot create "$SRC" >/dev/null
incr_s=$(elapsed "$t")
repo_after_incr=$(repo_bytes)
snap_v2=$(k snapshot list "$SRC" --json | jq -r '.[-1].id')

log "3/3 restore and verify both snapshots"
verify() { # verify SNAPSHOT_ID EXPECTED_SHA -> prints "seconds ok|FAIL"
	rm -f "$OUT"
	drop_caches
	local t0 s got
	t0=$(now)
	if ! k restore "$1" "$OUT" >/dev/null; then
		echo "0 FAIL"
		return
	fi
	s=$(elapsed "$t0")
	local file="$OUT"
	[[ -d "$OUT" ]] && file=$(find "$OUT" -type f | head -1) # some versions restore into a directory
	got=$(sha "$file")
	rm -rf "$OUT"
	[[ "$got" == "$2" ]] && echo "$s ok" || echo "$s FAIL"
}
read -r restore_v2_s ok_v2 < <(verify "$snap_v2" "$sha_v2")
read -r restore_v1_s ok_v1 < <(verify "$snap_v1" "$sha_v1")

gate_speed=$(awk -v f="$full_s" -v b="$baseline_s" -v g="$GATE_FACTOR" 'BEGIN { print (f <= g * b) ? 1 : 0 }')
verdict=OK
[[ "$ok_v1" == ok && "$ok_v2" == ok ]] || verdict=CRITICAL
[[ "$verdict" == OK && "$gate_speed" == 0 ]] && verdict=WARNING

jq -n \
	--arg verdict "$verdict" --arg compression "$COMPRESSION" \
	--argjson image_bytes "$img_bytes" \
	--argjson baseline_s "$baseline_s" --argjson full_s "$full_s" --argjson incr_s "$incr_s" \
	--argjson restore_v1_s "$restore_v1_s" --argjson restore_v2_s "$restore_v2_s" \
	--argjson repo_after_full "$repo_after_full" --argjson repo_after_incr "$repo_after_incr" \
	--arg ok_v1 "$ok_v1" --arg ok_v2 "$ok_v2" --argjson gate_factor "$GATE_FACTOR" \
	--argjson mutate "$mutate_json" \
	'{
	  verdict: $verdict,
	  gate: {restore_identical: ($ok_v1 == "ok" and $ok_v2 == "ok"),
	         speed: {rule: "full_s <= gate_factor * baseline_s", gate_factor: $gate_factor,
	                 passed: ($full_s <= $gate_factor * $baseline_s)}},
	  image_gib: ($image_bytes / 1073741824),
	  compression: $compression,
	  seconds: {baseline_qemu_img: $baseline_s, full_backup: $full_s, incremental_backup: $incr_s,
	            restore_full: $restore_v1_s, restore_incremental: $restore_v2_s},
	  throughput_mib_s: {baseline: ($image_bytes / 1048576 / $baseline_s),
	                     full_backup: ($image_bytes / 1048576 / $full_s),
	                     restore_full: ($image_bytes / 1048576 / $restore_v1_s)},
	  repository_bytes: {after_full: $repo_after_full, after_incremental: $repo_after_incr,
	                     incremental_growth: ($repo_after_incr - $repo_after_full)},
	  ratios: {stored_vs_image: ($repo_after_full / $image_bytes),
	           incremental_growth_vs_changed: (($repo_after_incr - $repo_after_full) / $mutate.bytes_changed)},
	  change: $mutate
	}' | tee "$REPORT"

log "verdict: $verdict  (report: $REPORT)"
[[ "$verdict" == OK ]] && exit 0
[[ "$verdict" == WARNING ]] && exit 1
exit 2
