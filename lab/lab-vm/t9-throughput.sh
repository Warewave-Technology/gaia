#!/usr/bin/env bash
# Runs T9 on gaia-lab: full-backup throughput through the product path (internal/nbd -> internal/diskstore ->
# Kopia Go library), gated by ADR-004 A1.
#
# Source: the 30 GiB test image exported read-only by qemu-nbd (--cache=none, so no run reads from the page cache).
# Repository: the second disk (DISK2, scsi2 from T5); one context run with the repository on the source disk.
# Per run, in this order, each after dropping the page cache:
#   reference  gaia-backup nbd-read: exactly the bytes the backup reads (the allocated extents), hash-free,
#              with 1, 4 and 8 NBD connections; the gate uses the fastest (A1);
#   backup     gaia-backup backup-nbd into a fresh repository, then sync of the repository's filesystem:
#              full_backup_s = backup + sync, so the data is on disk;
#   restore    gaia-backup restore (A2: 4 readers, cache on tmpfs with a 1 GiB hard limit, holes), sha256 vs source.
# Configs (CONFIGS, "readers/writers/compressor[/hash[/splitter]]"): tuning per CLAUDE.md T9; not the splitter (T10).
# The optional hash (e.g. BLAKE3-256-128) is the repository's content hash; T10 measures it (approved 2026-11-16).
# The optional splitter (FIXED-4M, FIXED-1M, FIXED-256K) is T10's: the point's block size follows it, and so does
# the reference read (same blocks); T9 itself leaves it at the default.
# Each config REPEAT times, in rounds. Afterwards one extra, uncounted backup per config with a CPU profile (pprof).
# Gate per config: median(full_backup_s / fastest plain read) <= 2 over >= 3 runs, every restore identical.
# T9 passes when at least one config passes; all configs are reported.
#
# Usage (on gaia-lab, as root, after setup.sh and t5-disk2.sh):  lab/lab-vm/t9-throughput.sh
#   systemd-run --unit=gaia-t9 --collect -E HOME=/root lab/lab-vm/t9-throughput.sh
# Env: IMAGE (/var/lib/gaia/test.img), DISK2 (/mnt/gaia-t5), CONFIGS ("4/4/s2-default 8/8/s2-default 4/4/none"),
#      REPEAT (3), REF_STREAMS ("1 4 8"), CONTEXT_RUN (1: same-disk runs of the first config, plus one profiled),
#      ONLY_CONTEXT=1 (skip the main rounds; context runs and their profile only), OUT_DIR (/root/t9-<UTC timestamp>), KEEP=1.
# Exit: 0 OK / 2 CRITICAL (no config passes, or a restore differs) / 3 UNKNOWN (not checked, also harness failure).
set -euo pipefail

IMAGE="${IMAGE:-/var/lib/gaia/test.img}"
DISK2="${DISK2:-/mnt/gaia-t5}"
CONFIGS="${CONFIGS:-4/4/s2-default 8/8/s2-default 4/4/none}"
REPEAT="${REPEAT:-3}"
REF_STREAMS="${REF_STREAMS:-1 4 8}"
CONTEXT_RUN="${CONTEXT_RUN:-1}"
OUT_DIR="${OUT_DIR:-/root/t9-$(date -u +%Y%m%dT%H%M%SZ)}"
WORK=/var/lib/gaia/t9
SOCK=/run/gaia-t9/nbd.sock
CACHE=/run/gaia-t9/cache
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
[[ "$REPEAT" =~ ^[1-9][0-9]*$ ]] || { echo "REPEAT must be a positive integer"; exit 3; }
for bin in go jq make qemu-nbd fstrim mountpoint; do command -v "$bin" >/dev/null || { echo "MISSING: $bin (run setup.sh)"; exit 3; }; done
[[ -s "$IMAGE" ]] || { echo "UNKNOWN: no $IMAGE (lab/lab-vm/t3-kopia.sh builds it)"; exit 3; }
[[ -d "$DISK2" ]] || { echo "UNKNOWN: no second disk at $DISK2 (lab/lab-vm/t5-disk2.sh)"; exit 3; }
mkdir -p "$OUT_DIR" "$WORK" /run/gaia-t9
exec > >(tee "$OUT_DIR/t9.log") 2>&1
ln -sfn "$OUT_DIR" /root/t9-latest

section() { printf '\n== %s\n' "$*"; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
now() { date +%s.%N; }
elapsed() { awk -v a="$(now)" -v b="$1" 'BEGIN { printf "%.3f", a - b }'; }
drop_caches() { sync; echo 3 >/proc/sys/vm/drop_caches; }
cpu_sample() { awk '$1 == "cpu" { print $2 + $3 + $4 + $5 + $6 + $7 + $8 + $9, $5, $6; exit }' /proc/stat; }
cpu_json() { # cpu_json "SAMPLE" -> {"busy_pct", "iowait_pct"} since SAMPLE
	local t0 i0 w0 t1 i1 w1
	read -r t0 i0 w0 <<<"$1"
	read -r t1 i1 w1 <<<"$(cpu_sample)"
	awk -v t="$((t1 - t0))" -v i="$((i1 - i0))" -v w="$((w1 - w0))" -v n="$(nproc)" \
		'BEGIN { printf "{\"cpus\": %d, \"busy_pct\": %.1f, \"iowait_pct\": %.1f}", n, 100 * (t - i - w) / t, 100 * w / t }'
}
disk_sample() { awk '$3 ~ /^(sd[a-z]+|vd[a-z]+|nvme[0-9]+n[0-9]+)$/ { print $3, $6, $10 }' /proc/diskstats; }
disk_json() { # %.0f, not %d: mawk clamps %d at 2^31-1 (T5)
	awk 'NR == FNR { r[$1] = $2; w[$1] = $3; next }
		($1 in r) { printf "%s\"%s\": {\"read_bytes\": %.0f, \"written_bytes\": %.0f}", sep, $1, ($2 - r[$1]) * 512, ($3 - w[$1]) * 512; sep = ", " }
		BEGIN { printf "{" } END { print "}" }' <(echo "$1") <(disk_sample)
}

nbd_pid=""
# shellcheck disable=SC2329 # invoked by the trap
cleanup() {
	[[ -n "$nbd_pid" ]] && kill "$nbd_pid" 2>/dev/null
	mountpoint -q "$CACHE" && umount "$CACHE"
	if [[ "${KEEP:-}" != 1 ]]; then rm -rf "$WORK" "$DISK2/t9-repo"; fi
	rm -rf /run/gaia-t9
	sync
	fstrim -a >/dev/null 2>&1 || true
}
trap cleanup EXIT

section "date / commit"
date -u +%Y-%m-%dT%H:%M:%SZ
git -C "$repo_root" rev-parse HEAD 2>/dev/null || echo "not a git checkout"
echo "output: $OUT_DIR"

section "build"
owner=$(stat -c %U "$repo_root")
if [[ "$owner" == root ]]; then
	make -C "$repo_root" -s build
else
	runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" make -C "$repo_root" -s build
fi
go version -m "$BIN/gaia-backup" | grep -E 'vcs\.(revision|modified)' || echo "no VCS stamp"

section "machine"
grep -m1 'model name' /proc/cpuinfo || true
nproc
for f in aes sha_ni avx2 avx512f; do printf '%s: %s\n' "$f" "$(grep -m1 -qw "$f" /proc/cpuinfo && echo yes || echo no)"; done
free -m
qemu-nbd --version | head -1
kopia --version | head -1
echo "kopia benchmark hashing (4 MiB blocks, single goroutine; context for the hash configs):"
kopia benchmark hashing --block-size=4MiB --repeat=20 2>&1 | grep -v '^$' | head -20 || echo "(kopia benchmark hashing failed)"
lsblk -o NAME,SIZE,ROTA,MOUNTPOINT
df -h "$(dirname "$IMAGE")" "$DISK2"

section "free space"
img_alloc=$(($(stat -c %b "$IMAGE") * $(stat -c %B "$IMAGE")))
need2=$((img_alloc * 13 / 10)) # a repository with compression none is about the allocated size
free2=$(($(df -Pk "$DISK2" | awk 'NR == 2 { print $4 }') * 1024))
((free2 >= need2)) || { echo "UNKNOWN: $DISK2 has $((free2 >> 30)) GiB free, needs $((need2 >> 30)) GiB"; exit 3; }
free1=$(($(df -Pk "$WORK" | awk 'NR == 2 { print $4 }') * 1024))
((free1 >= 3 * need2)) || { echo "UNKNOWN: $WORK has $((free1 >> 30)) GiB free, needs $((3 * need2 >> 30)) GiB"; exit 3; }
echo "image $(stat -c %s "$IMAGE") bytes, $img_alloc allocated; $DISK2 free $free2; $WORK free $free1"

section "source: qemu-nbd -r --cache=none, sha256 of the image"
"$BIN/imgtool" hash -file "$IMAGE" | tee "$OUT_DIR/source.json"
src_sha=$(jq -r .sha256 "$OUT_DIR/source.json")
qemu-nbd -r -t -f raw --cache=none --aio=native --shared=32 -x test --socket="$SOCK" "$IMAGE" &
nbd_pid=$!
for _ in $(seq 50); do [[ -S "$SOCK" ]] && break; sleep 0.2; done
[[ -S "$SOCK" ]] || { echo "UNKNOWN: qemu-nbd did not start"; exit 3; }
mkdir -p "$CACHE"
mount -t tmpfs -o size=2g tmpfs "$CACHE"
GAIA_REPO_PASSWORD=$(head -c 24 /dev/urandom | base64)
export GAIA_REPO_PASSWORD
gb() { "$BIN/gaia-backup" "$@"; }

: >"$OUT_DIR/runs.jsonl"
# run_once LABEL REPO_DIR READERS WRITERS COMPRESSOR HASH SPLITTER ROUND [PROFILE]  ("" = Kopia's default / FIXED-4M)
run_once() {
	local label=$1 repodir=$2 readers=$3 writers=$4 comp=$5 hash=$6 splitter=${7:-FIXED-4M} round=$8 profile=${9:-} refs=() n c d t0 out bs
	case $splitter in
	FIXED-*K) bs=$((${splitter//[!0-9]/} * 1024)) ;;
	FIXED-*M) bs=$((${splitter//[!0-9]/} * 1048576)) ;;
	*) echo "UNKNOWN: splitter $splitter"; exit 3 ;;
	esac
	rm -rf "$repodir" "$WORK/kopia.config" "${CACHE:?}"/* "$WORK/restored.raw"
	if [[ -z "$profile" ]]; then
		for n in $REF_STREAMS; do
			drop_caches
			gb nbd-read -nbd "unix:$SOCK" -export test -conns "$n" -block-size "$bs" -json >"$WORK/ref.json"
			refs+=("$(jq -c --argjson n "$n" '{streams: $n, seconds: .read.seconds, bytes: .read.bytes, mib_s}' "$WORK/ref.json")")
		done
	fi
	gb repo-create -repo "$repodir" -config "$WORK/kopia.config" -cache-dir "$CACHE" -cache-limit-mb 1024 ${hash:+-hash "$hash"} -splitter "$splitter" >/dev/null
	drop_caches
	c=$(cpu_sample) d=$(disk_sample) t0=$(now)
	gb backup-nbd -config "$WORK/kopia.config" -nbd "unix:$SOCK" -export test -domain t9 -conns "$readers" -writers "$writers" -block-size "$bs" \
		-compression "$comp" ${profile:+-cpuprofile "$profile"} -json >"$WORK/backup.json"
	local backup_s sync_s cpu disk t1
	backup_s=$(elapsed "$t0")
	t1=$(now)
	sync -f "$repodir"
	sync_s=$(elapsed "$t1")
	cpu=$(cpu_json "$c") disk=$(disk_json "$d")
	local repo_bytes restore='null' identical=null
	repo_bytes=$(du -sb "$repodir" | cut -f1)
	if [[ -z "$profile" ]]; then
		rm -rf "${CACHE:?}"/*
		drop_caches
		t1=$(now)
		gb restore -config "$WORK/kopia.config" -domain t9 -out "$WORK/restored.raw" -workers 4 -json >"$WORK/restore.json"
		restore=$(jq -c --argjson s "$(elapsed "$t1")" '{seconds_wall: $s, stats}' "$WORK/restore.json")
		[[ "$("$BIN/imgtool" hash -file "$WORK/restored.raw" | jq -r .sha256)" == "$src_sha" ]] && identical=true || identical=false
		rm -f "$WORK/restored.raw"
	fi
	out=$(jq -nc --arg cfg "$label" --argjson round "$round" --arg repo "$repodir" --argjson readers "$readers" --argjson writers "$writers" \
		--arg comp "$comp" --argjson backup_s "$backup_s" --argjson sync_s "$sync_s" --argjson cpu "$cpu" --argjson disk "$disk" \
		--argjson repo_bytes "$repo_bytes" --argjson restore "$restore" --argjson identical "$identical" \
		--slurpfile b "$WORK/backup.json" --argjson refs "[$(IFS=,; echo "${refs[*]:-}")]" --arg profile "$profile" '
		($refs | map(.seconds) | min) as $ref
		| {config: $cfg, round: $round, repository: $repo, readers: $readers, writers: $writers, compressor: $comp,
		   full_backup_s: (($backup_s + $sync_s) * 1000 | round / 1000), backup_cmd_s: $backup_s, sync_s: $sync_s,
		   plain_read: $refs, fastest_plain_read_s: $ref,
		   ratio: (if $ref then ($backup_s + $sync_s) / $ref else null end),
		   bytes_read: $b[0].point.stats.source_read_bytes, repository_bytes: $repo_bytes, pipeline: ($b[0].point.stats | {readers, writers, compressor,
		     seconds, read_busy_seconds, write_busy_seconds, reader_wait_seconds, blocks_written, blocks_zero}),
		   cpu: $cpu, disk_io: $disk, restore: $restore, identical: $identical, cpuprofile: (if $profile == "" then null else $profile end)}')
	echo "$out" >>"$OUT_DIR/runs.jsonl"
	jq -c '{config, round, full_backup_s, fastest_plain_read_s, ratio, identical, cpu, write_busy: .pipeline.write_busy_seconds, reader_wait: .pipeline.reader_wait_seconds}' <<<"$out"
	rm -rf "$repodir"
}

section "runs (repository on $DISK2)"
((${ONLY_CONTEXT:-0})) && REPEAT=0 && echo "ONLY_CONTEXT=1: skipped"
for ((round = 1; round <= REPEAT; round++)); do
	for cfg in $CONFIGS; do
		IFS=/ read -r readers writers comp hash splitter <<<"$cfg"
		log "round $round/$REPEAT: $cfg"
		run_once "$cfg" "$DISK2/t9-repo" "$readers" "$writers" "$comp" "${hash:-}" "${splitter:-}" "$round"
	done
done
if ((CONTEXT_RUN > 0)); then
	section "context: repository on the source disk ($WORK)"
	cfg=${CONFIGS%% *}
	IFS=/ read -r readers writers comp hash splitter <<<"$cfg"
	for ((round = 1; round <= CONTEXT_RUN; round++)); do
		run_once "$cfg same-disk" "$WORK/t9-repo" "$readers" "$writers" "$comp" "${hash:-}" "${splitter:-}" "$round"
	done
	mkdir -p "$OUT_DIR/profiles"
	p="$OUT_DIR/profiles/cpu-${readers}r-${writers}w-${comp}${hash:+-$hash}${splitter:+-$splitter}-same-disk.pprof"
	run_once "$cfg same-disk profile" "$WORK/t9-repo" "$readers" "$writers" "$comp" "${hash:-}" "${splitter:-}" 0 "$p" >/dev/null
	echo "-- $cfg same-disk (profile)"
	go tool pprof -top -nodecount=25 "$BIN/gaia-backup" "$p" 2>/dev/null | tee "${p%.pprof}.top.txt" | sed -n '1,32p'
fi

section "CPU profiles (one extra, uncounted backup per config)"
mkdir -p "$OUT_DIR/profiles"
((${ONLY_CONTEXT:-0})) || for cfg in $CONFIGS; do
	IFS=/ read -r readers writers comp hash splitter <<<"$cfg"
	p="$OUT_DIR/profiles/cpu-${readers}r-${writers}w-${comp}${hash:+-$hash}${splitter:+-$splitter}.pprof"
	run_once "$cfg profile" "$DISK2/t9-repo" "$readers" "$writers" "$comp" "${hash:-}" "${splitter:-}" 0 "$p" >/dev/null
	echo "-- $cfg"
	go tool pprof -top -nodecount=25 "$BIN/gaia-backup" "$p" 2>/dev/null | tee "${p%.pprof}.top.txt" | sed -n '1,32p'
done
cp "$BIN/gaia-backup" "$OUT_DIR/profiles/gaia-backup.bin" # pprof needs the exact binary

section "summary (median of the counted runs per config)"
jq -s --argjson repeat "$REPEAT" '
	def median: map(select(. != null)) | sort | length as $n | if $n == 0 then null elif $n % 2 == 1 then .[($n - 1) / 2] else (.[$n / 2 - 1] + .[$n / 2]) / 2 end;
	map(select(.round > 0)) | group_by(.config) | map({
	  config: .[0].config, runs: length,
	  ratios: map(.ratio), median_ratio: (map(.ratio) | median),
	  median_full_backup_s: (map(.full_backup_s) | median), median_fastest_plain_read_s: (map(.fastest_plain_read_s) | median),
	  median_backup_mib_s: (map(.bytes_read / 1048576 / .full_backup_s) | median),
	  median_repository_bytes: (map(.repository_bytes) | median),
	  median_restore_s: (map(.restore.seconds_wall) | median),
	  median_cpu_busy_pct: (map(.cpu.busy_pct) | median), median_iowait_pct: (map(.cpu.iowait_pct) | median),
	  median_write_busy_per_s: (map(.pipeline.write_busy_seconds / .pipeline.seconds) | median),
	  median_reader_wait_per_s: (map(.pipeline.reader_wait_seconds / .pipeline.seconds) | median),
	  identical: (if any(.identical == false) then false elif all(.identical == true) then true else null end)
	} | . + {gate: (if .identical == false then "CRITICAL" elif .runs < 3 or .identical == null or .median_ratio == null then "UNKNOWN"
	                elif .median_ratio <= 2 then "OK" else "CRITICAL" end)})' "$OUT_DIR/runs.jsonl" | tee "$OUT_DIR/summary.json"

section "gate (ADR-004 A1)"
jq '{rule: "per config: median(full_backup_s / fastest hash-free read of the same bytes) <= 2 over >= 3 runs, every restore identical; T9 passes if a config passes",
	configs: map({config, median_ratio, gate}),
	best: (map(select(.median_ratio != null and (.config | test("same-disk") | not))) | min_by(.median_ratio) // null | if . then {config, median_ratio, gate} else null end),
	verdict: (if any(.[]; .identical == false) then "CRITICAL" elif any(.[]; .gate == "OK" and (.config | test("same-disk") | not)) then "OK"
	          elif all(.[]; .gate == "UNKNOWN") then "UNKNOWN" else "CRITICAL" end)}' "$OUT_DIR/summary.json" | tee "$OUT_DIR/gate.json"

if ((${ONLY_CONTEXT:-0})); then # same-disk runs are context, never the T9 gate
	jq '. + {verdict: "UNKNOWN", note: "ONLY_CONTEXT=1: same-disk context runs only, not the T9 gate"}' "$OUT_DIR/gate.json" >"$OUT_DIR/gate.tmp"
	mv "$OUT_DIR/gate.tmp" "$OUT_DIR/gate.json"
	jq -c '{verdict, note}' "$OUT_DIR/gate.json"
fi

section "result"
verdict=$(jq -r .verdict "$OUT_DIR/gate.json")
echo "$verdict"
echo "(gate.json, summary.json, runs.jsonl, profiles/ in $OUT_DIR)"
case $verdict in
OK) exit 0 ;;
CRITICAL) exit 2 ;;
*) exit 3 ;;
esac
