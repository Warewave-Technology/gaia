#!/usr/bin/env bash
# Phase 2 / T5: why is a Kopia full restore of a disk image 3-5x slower than the backup?
#
# One repository (ADR-004 §2 defaults), one full backup of IMAGE, then every restore variant REPEAT times,
# in rounds (every variant once, then again), each from a cold content cache and dropped page caches.
# Per run it measures what the restore costs, not only how long it takes: CPU busy/iowait, bytes read and
# written per disk, the content cache left behind, and the space the restored file allocates. Every restore
# that produces a file is checked byte for byte (sha256) against the image.
#
# Variants (VARIANTS, space separated):
#   restore-file          kopia restore to a file on the work disk; Kopia's defaults (what T3/T4 measured)
#   restore-p1            same, --parallel=1
#   restore-sparse        same, --write-sparse-files
#   restore-tmpfscache    same, content cache on tmpfs with a hard limit (TMPFS_CACHE_MB)
#   restore-tuned         --write-sparse-files and the tmpfs cache
#   restore-disk2         kopia restore to a file on a second disk (DISK2, a mounted directory)
#   show-null             kopia show of the file object to /dev/null: Kopia's read path, no target file
#   show-null-tmpfscache  same, tmpfs cache
#   mount-file            kopia mount (FUSE), MOUNT_READERS parallel dd over disjoint ranges into a file
#   mount-tuned           same, tmpfs cache and sparse writes (dd conv=sparse)
# A file restore is timed until the file is on disk (sync of the file), so dirty page cache does not
# hide part of the write. T3/T4 stopped the clock when kopia exited.
#
# Usage:  IMAGE=/var/lib/gaia/test.img WORK=/var/lib/gaia/t5-restore spikes/kopia/restore.sh
# Needs:  root (drop caches, tmpfs), kopia, jq, imgtool (make build); fusermount3 for the mount variants.
# Output: WORK/report.json. Exit: 0 OK / 2 CRITICAL (a restore differs) / 3 UNKNOWN (a variant has fewer
#         than 3 or fewer than REPEAT good runs). There is no speed gate: T5 is an investigation.
set -euo pipefail

IMAGE="${IMAGE:?set IMAGE to the test disk image}"
WORK="${WORK:-/var/lib/gaia/t5-restore}"
REPEAT="${REPEAT:-3}"
COMPRESSION="${COMPRESSION:-s2-default}"
SPLITTER="${SPLITTER:-FIXED-4M}"
DISK2="${DISK2:-}"
TMPFS_CACHE_MB="${TMPFS_CACHE_MB:-1024}"
MOUNT_READERS="${MOUNT_READERS:-4}"
WRITE_TEST_MIB="${WRITE_TEST_MIB:-8192}"
default_variants="restore-file restore-p1 restore-sparse restore-tmpfscache restore-tuned show-null show-null-tmpfscache mount-file mount-tuned"
[[ -n "$DISK2" ]] && default_variants+=" restore-disk2"
VARIANTS="${VARIANTS:-$default_variants}"
IMGTOOL="${IMGTOOL:-$(dirname "$0")/../../bin/imgtool}"

export KOPIA_PASSWORD="${KOPIA_PASSWORD:-gaia-spike-not-a-secret}"
export KOPIA_CHECK_FOR_UPDATES=false
SRC_DIR="$WORK/src"
REPO="$WORK/repo"
CFG="$WORK/kopia.config"          # content cache on the work disk, Kopia's default limits
CACHE="$WORK/cache"
CFG_T="$WORK/kopia-tmpfs.config"  # the same repository, content cache on tmpfs with a hard limit
CACHE_T="$WORK/cache-tmpfs"
MNT="$WORK/mnt"
RUNS="$WORK/runs.jsonl"
REPORT="$WORK/report.json"

((EUID == 0)) || { echo "run as root: drops page caches and mounts a tmpfs"; exit 3; }
[[ "$REPEAT" =~ ^[1-9][0-9]*$ ]] || { echo "REPEAT must be a positive integer"; exit 3; }
for bin in kopia jq dd; do command -v "$bin" >/dev/null || { echo "missing: $bin"; exit 3; }; done
[[ -x "$IMGTOOL" ]] || { echo "imgtool not found at $IMGTOOL (run: make build)"; exit 3; }
[[ ! -e "$WORK" ]] || { echo "$WORK exists; remove it for a clean run"; exit 3; }
[[ -z "$DISK2" || -d "$DISK2" ]] || { echo "DISK2=$DISK2 is not a directory"; exit 3; }
variants_json=$(jq -nc --arg v "$VARIANTS" '$v | split(" ") | map(select(length > 0))')
known=" restore-file restore-p1 restore-sparse restore-tmpfscache restore-tuned restore-disk2 show-null show-null-tmpfscache mount-file mount-tuned "
for v in $VARIANTS; do [[ "$known" == *" $v "* ]] || { echo "unknown variant: $v"; exit 3; }; done
[[ "$VARIANTS" != *restore-disk2* || -n "$DISK2" ]] || { echo "restore-disk2 needs DISK2"; exit 3; }
mkdir -p "$SRC_DIR" "$CACHE_T" "$MNT"
: >"$RUNS"

# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
	mountpoint -q "$MNT" && { fusermount3 -u "$MNT" 2>/dev/null || umount -l "$MNT"; }
	mountpoint -q "$CACHE_T" && umount "$CACHE_T"
	[[ -z "$DISK2" ]] || rm -f "$DISK2/gaia-t5-restored.img"
	return 0
}
trap cleanup EXIT

now() { date +%s.%N; }
elapsed() { awk -v a="$(now)" -v b="$1" 'BEGIN { printf "%.3f", a - b }'; }
drop_caches() { sync && echo 3 >/proc/sys/vm/drop_caches; }
log() { echo "[$(date +%H:%M:%S)] $*"; }
cpu_sample() { awk '$1 == "cpu" { print $2 + $3 + $4 + $5 + $6 + $7 + $8 + $9, $5, $6; exit }' /proc/stat; }
cpu_json() { # cpu_json "TOTAL IDLE IOWAIT" (from cpu_sample before the step)
	local t0 i0 w0 t1 i1 w1
	read -r t0 i0 w0 <<<"$1"
	read -r t1 i1 w1 <<<"$(cpu_sample)"
	awk -v t="$((t1 - t0))" -v i="$((i1 - i0))" -v w="$((w1 - w0))" -v n="$(nproc)" \
		'BEGIN { printf "{\"cpus\": %d, \"busy_pct\": %.1f, \"iowait_pct\": %.1f}", n, 100 * (t - i - w) / t, 100 * w / t }'
}
# Whole disks only (sda, vdb, nvme0n1), sectors of 512 bytes: "name read written" per line.
disk_sample() { awk '$3 ~ /^(sd[a-z]+|vd[a-z]+|nvme[0-9]+n[0-9]+)$/ { print $3, $6, $10 }' /proc/diskstats; }
disk_json() { # disk_json "SAMPLE" (from disk_sample before the step) -> {"sda": {"read_bytes": .., "written_bytes": ..}}
	awk 'NR == FNR { r[$1] = $2; w[$1] = $3; next }
		($1 in r) { printf "%s\"%s\": {\"read_bytes\": %d, \"written_bytes\": %d}", sep, $1, ($2 - r[$1]) * 512, ($3 - w[$1]) * 512; sep = ", " }
		BEGIN { printf "{" } END { print "}" }' <(echo "$1") <(disk_sample)
}
k() { kopia --config-file="$CFG" "$@"; }

log "source: $IMAGE"
# A hard link costs nothing and keeps the image untouched (Kopia only reads it); a copy if WORK is elsewhere.
# The snapshot is of a directory so that kopia mount can expose the file.
ln "$IMAGE" "$SRC_DIR/disk.img" 2>/dev/null || cp --sparse=always "$IMAGE" "$SRC_DIR/disk.img"
img_bytes=$(stat -c %s "$SRC_DIR/disk.img")
img_alloc=$(($(stat -c %b "$SRC_DIR/disk.img") * $(stat -c %B "$SRC_DIR/disk.img")))
sha_img=$("$IMGTOOL" hash -file "$SRC_DIR/disk.img" | jq -r .sha256)

log "creating Kopia repository (compression=$COMPRESSION, splitter=$SPLITTER), full backup"
k repository create filesystem --path="$REPO" --cache-directory="$CACHE" --object-splitter="$SPLITTER" >/dev/null
k policy set --global --compression="$COMPRESSION" >/dev/null
repo_format=$(k repository status --json | jq -c '{hash: .contentFormat.hash, encryption: .contentFormat.encryption, splitter: .objectFormat.splitter}')
drop_caches
t=$(now)
k snapshot create "$SRC_DIR" >/dev/null
full_s=$(elapsed "$t")
repo_bytes=$(du -sb "$REPO" | cut -f1)
root=$(k snapshot list "$SRC_DIR" --json | jq -r '.[-1].rootEntry.obj')
fobj=$(k show "$root" | jq -r '.entries[] | select(.name == "disk.img") | .obj')
[[ "$fobj" == I* || "$fobj" == [0-9a-f]* ]] || { echo "UNKNOWN: no object id for disk.img in $root"; exit 3; }

log "tmpfs content cache: ${TMPFS_CACHE_MB} MB hard limit"
mount -t tmpfs -o size="$((TMPFS_CACHE_MB * 2))m" tmpfs "$CACHE_T"
kopia --config-file="$CFG_T" repository connect filesystem --path="$REPO" --cache-directory="$CACHE_T" \
	--content-cache-size-mb="$((TMPFS_CACHE_MB / 2))" --content-cache-size-limit-mb="$TMPFS_CACHE_MB" \
	--metadata-cache-size-mb="$((TMPFS_CACHE_MB / 4))" --metadata-cache-size-limit-mb="$((TMPFS_CACHE_MB / 2))" >/dev/null

# Context: how fast the disks are without Kopia. Reading the repository back, and WRITE_TEST_MIB of direct writes.
log "context: repository read, sequential write per target disk"
drop_caches
t=$(now)
find "$REPO" -type f -exec cat {} + >/dev/null
repo_read_s=$(elapsed "$t")
write_test() { # write_test DIR -> seconds for WRITE_TEST_MIB of O_DIRECT writes
	local t0 s
	t0=$(now)
	dd if=/dev/zero of="$1/gaia-t5-write-test" bs=4M count="$((WRITE_TEST_MIB / 4))" oflag=direct status=none
	s=$(elapsed "$t0")
	rm -f "$1/gaia-t5-write-test"
	echo "$s"
}
write_work_s=$(write_test "$WORK")
write_disk2_s=null
[[ -n "$DISK2" ]] && write_disk2_s=$(write_test "$DISK2")

# mount_copy CFG OUT SPARSE: kopia mount, then MOUNT_READERS dd over disjoint 4 MiB-aligned ranges.
mount_copy() {
	local cfg=$1 out=$2 sparse=$3 mpid i n chunks per conv=notrunc rc=0 pids=()
	kopia --config-file="$cfg" mount "$root" "$MNT" >"$WORK/mount.log" 2>&1 &
	mpid=$!
	for ((i = 0; i < 120; i++)); do [[ -e "$MNT/disk.img" ]] && break; sleep 0.5; done
	[[ -e "$MNT/disk.img" ]] || { cat "$WORK/mount.log"; kill "$mpid" 2>/dev/null; return 1; }
	n=$(stat -c %s "$MNT/disk.img")
	chunks=$(((n + 4194303) / 4194304))
	per=$(((chunks + MOUNT_READERS - 1) / MOUNT_READERS))
	[[ "$sparse" == 1 ]] && conv+=,sparse
	truncate -s "$n" "$out"
	for ((i = 0; i < MOUNT_READERS; i++)); do
		dd if="$MNT/disk.img" of="$out" bs=4M skip=$((i * per)) seek=$((i * per)) count="$per" conv="$conv" status=none &
		pids+=($!)
	done
	for i in "${pids[@]}"; do wait "$i" || rc=1; done
	kill -INT "$mpid" 2>/dev/null
	wait "$mpid" 2>/dev/null || true
	mountpoint -q "$MNT" && { fusermount3 -u "$MNT" 2>/dev/null || umount -l "$MNT"; }
	return "$rc"
}

# run_variant NAME ROUND: one cold restore, appends one JSON line to RUNS.
run_variant() {
	local name=$1 round=$2 cfg=$CFG cache=$CACHE out="$WORK/restored.img" method=restore flags=() sparse=0
	case $name in
	restore-file) ;;
	restore-p1) flags=(--parallel=1) ;;
	restore-sparse) flags=(--write-sparse-files) ;;
	restore-tmpfscache) cfg=$CFG_T cache=$CACHE_T ;;
	restore-tuned) cfg=$CFG_T cache=$CACHE_T flags=(--write-sparse-files) ;;
	restore-disk2) out="$DISK2/gaia-t5-restored.img" ;;
	show-null) method=show out='' ;;
	show-null-tmpfscache) method=show out='' cfg=$CFG_T cache=$CACHE_T ;;
	mount-file) method=mount ;;
	mount-tuned) method=mount cfg=$CFG_T cache=$CACHE_T sparse=1 ;;
	esac
	if [[ "$method" == mount ]] && ! command -v fusermount3 >/dev/null; then
		log "$name: not run, fusermount3 missing (apt-get install fuse3)"
		jq -nc --arg v "$name" --argjson r "$round" '{variant: $v, round: $r, rc: null, not_run: "fusermount3 missing"}' >>"$RUNS"
		return 0
	fi
	# A cold restore: no content in Kopia's cache, nothing in the page cache.
	rm -rf "$cache/contents"
	[[ -n "$out" ]] && rm -f "$out"
	drop_caches
	local c d t0 rc=0 s
	c=$(cpu_sample)
	d=$(disk_sample)
	t0=$(now)
	case $method in
	restore) kopia --config-file="$cfg" restore "$root/disk.img" "$out" "${flags[@]}" >/dev/null 2>"$WORK/err" && sync "$out" || rc=$? ;;
	show) kopia --config-file="$cfg" show "$fobj" >/dev/null 2>"$WORK/err" || rc=$? ;;
	mount) mount_copy "$cfg" "$out" "$sparse" 2>"$WORK/err" && sync "$out" || rc=$? ;;
	esac
	s=$(elapsed "$t0")
	local cpu disk cache_bytes alloc=null identical=null
	cpu=$(cpu_json "$c")
	disk=$(disk_json "$d")
	((rc == 0)) || { echo "$name failed (exit $rc):"; tail -5 "$WORK/err"; }
	cache_bytes=$(du -sb "$cache/contents" 2>/dev/null | cut -f1 || echo 0)
	# A failed run says nothing about the data: it counts as not measured (UNKNOWN), not as a mismatch.
	if [[ -n "$out" ]] && ((rc == 0)); then
		if [[ -f "$out" ]]; then
			alloc=$(($(stat -c %b "$out") * $(stat -c %B "$out")))
			[[ "$("$IMGTOOL" hash -file "$out" | jq -r .sha256)" == "$sha_img" ]] && identical=true || identical=false
		else
			identical=false
		fi
	fi
	[[ -z "$out" ]] || rm -f "$out"
	jq -nc --arg v "$name" --argjson r "$round" --argjson rc "$rc" --argjson s "$s" --argjson b "$img_bytes" \
		--argjson cpu "$cpu" --argjson disk "$disk" --argjson cache "${cache_bytes:-0}" \
		--argjson alloc "$alloc" --argjson identical "$identical" \
		'{variant: $v, round: $r, rc: $rc, seconds: $s, mib_s: ($b / 1048576 / $s), cpu: $cpu, disk_io: $disk,
		  content_cache_bytes_after: $cache, target_allocated_bytes: $alloc, identical: $identical}' | tee -a "$RUNS"
}

for ((round = 1; round <= REPEAT; round++)); do
	for v in $VARIANTS; do
		log "round $round/$REPEAT: $v"
		run_variant "$v" "$round"
	done
done

# shellcheck disable=SC2016 # $vars below are jq's, not the shell's
summary_jq='
def median: map(select(. != null)) | sort | length as $n | if $n == 0 then null elif $n % 2 == 1 then .[($n - 1) / 2] else (.[$n / 2 - 1] + .[$n / 2]) / 2 end;
($runs | map(select(.variant == "restore-file" and .rc == 0) | .seconds) | median) as $base
| [$variants[] as $v | ($runs | map(select(.variant == $v))) as $all | ($all | map(select(.rc == 0))) as $ok
  | {variant: $v, runs_planned: $repeat, runs_ok: ($ok | length),
     runs_failed_or_not_run: ((($all | length) - ($ok | length)) + ($repeat - ($all | length))),
     seconds: ($ok | map(.seconds)), median_seconds: ($ok | map(.seconds) | median),
     median_mib_s: ($ok | map(.mib_s) | median),
     speedup_vs_restore_file: (if $base and ($ok | length) > 0 then $base / ($ok | map(.seconds) | median) else null end),
     median_cpu_busy_pct: ($ok | map(.cpu.busy_pct) | median), median_iowait_pct: ($ok | map(.cpu.iowait_pct) | median),
     median_written_bytes: ($ok | map([.disk_io[].written_bytes] | add) | median),
     median_read_bytes: ($ok | map([.disk_io[].read_bytes] | add) | median),
     median_content_cache_bytes_after: ($ok | map(.content_cache_bytes_after) | median),
     target_allocated_bytes: ($ok | map(.target_allocated_bytes) | median),
     identical: (if ($all | map(.identical) | any(. == false)) then false
                 elif ($ok | length) > 0 and ($ok | map(.identical) | all(. == true)) then true else null end)}
  | . + {verdict: (if .identical == false then "CRITICAL"
                   elif .runs_ok < 3 or .runs_ok < .runs_planned then "UNKNOWN" else "OK" end)}]'
summary=$(jq -n --slurpfile runs "$RUNS" --argjson repeat "$REPEAT" --argjson variants "$variants_json" "$summary_jq")
verdict=$(jq -r 'if any(.verdict == "CRITICAL") then "CRITICAL" elif any(.verdict == "UNKNOWN") then "UNKNOWN" else "OK" end' <<<"$summary")

jq -n --arg verdict "$verdict" --argjson summary "$summary" --slurpfile runs "$RUNS" \
	--argjson image_bytes "$img_bytes" --argjson image_allocated "$img_alloc" --argjson full_s "$full_s" \
	--argjson repo_bytes "$repo_bytes" --argjson repo_format "$repo_format" --arg compression "$COMPRESSION" \
	--argjson repo_read_s "$repo_read_s" --argjson write_work_s "$write_work_s" --argjson write_disk2_s "$write_disk2_s" \
	--argjson tmpfs_mb "$TMPFS_CACHE_MB" --argjson wmib "$WRITE_TEST_MIB" --argjson readers "$MOUNT_READERS" --arg kopia "$(kopia --version | head -1)" \
	'{verdict: $verdict,
	  setup: {image_bytes: $image_bytes, image_allocated_bytes: $image_allocated, compression: $compression,
	          kopia_repository: $repo_format, kopia: $kopia, tmpfs_cache_limit_mb: $tmpfs_mb, mount_readers: $readers},
	  context: {full_backup_s: $full_s, repository_bytes: $repo_bytes,
	            repository_read_s: $repo_read_s, repository_read_mib_s: ($repo_bytes / 1048576 / $repo_read_s),
	            write_test_mib: $wmib, write_direct_work_s: $write_work_s, write_work_mib_s: ($wmib / $write_work_s),
	            write_direct_disk2_s: $write_disk2_s,
	            write_disk2_mib_s: (if $write_disk2_s then $wmib / $write_disk2_s else null end)},
	  summary: $summary,
	  runs: $runs}' >"$REPORT"
jq '{verdict, setup, context, summary}' "$REPORT"
log "verdict: $verdict  (report: $REPORT)"
case $verdict in
OK) exit 0 ;;
CRITICAL) exit 2 ;;
*) exit 3 ;;
esac
