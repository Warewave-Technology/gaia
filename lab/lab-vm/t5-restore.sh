#!/usr/bin/env bash
# Runs the T5 restore investigation on gaia-lab: builds the tools, reuses the 30 GiB test image (builds it
# if missing), checks the Kopia CLI flags the spike uses against the installed kopia, records the CPU
# features that bound hashing and decryption, runs spikes/kopia/restore.sh and prints its report.
# Output goes verbatim into the report.
#
# Usage (on gaia-lab, as root, after setup.sh):  lab/lab-vm/t5-restore.sh
# It takes long (~1.5 h for 30 GiB and 9 variants x 3), so start it detached and follow the log:
#   systemd-run --unit=gaia-t5 --collect -E HOME=/root lab/lab-vm/t5-restore.sh
# Env: IMAGE (/var/lib/gaia/test.img, built with SIZE_GB=30 if missing), REPEAT (3), VARIANTS (the spike's),
#      DISK2 (a directory on a second disk; default /mnt/gaia-t5 when that is a mount point, see t5-disk2.sh),
#      WRITE_TEST_MIB (8192), OUT_DIR (/root/t5-<UTC timestamp>), KEEP=1 keeps the work dir (default: removed, then fstrim).
# Exit: the spike's verdict, 0 OK / 2 CRITICAL / 3 UNKNOWN (also when the harness failed).
set -euo pipefail

SIZE_GB="${SIZE_GB:-30}"
IMAGE="${IMAGE:-/var/lib/gaia/test.img}"
OUT_DIR="${OUT_DIR:-/root/t5-$(date -u +%Y%m%dT%H%M%SZ)}"
REPEAT="${REPEAT:-3}"
if [[ -z "${DISK2:-}" ]] && mountpoint -q /mnt/gaia-t5; then DISK2=/mnt/gaia-t5; fi
DISK2="${DISK2:-}"
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in go kopia jq make mkfs.ext4 fstrim; do command -v "$bin" >/dev/null || { echo "MISSING: $bin (run setup.sh)"; exit 3; }; done
mkdir -p "$OUT_DIR"
exec > >(tee "$OUT_DIR/t5.log") 2>&1
ln -sfn "$OUT_DIR" /root/t5-latest

section() { printf '\n== %s\n' "$*"; }

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
go version -m "$repo_root/bin/imgtool" | grep -E 'vcs\.(revision|modified)' || echo "no VCS stamp"

# Hashing (BLAKE2B, sha256) and AES-GCM speed depend on what the vCPU exposes; T4 left this open.
section "cpu / memory"
grep -m1 'model name' /proc/cpuinfo || echo "model name: not in /proc/cpuinfo ($(uname -m))"
nproc
for f in aes sha_ni avx2 pclmulqdq; do printf '%s: %s\n' "$f" "$(grep -m1 -qw "$f" /proc/cpuinfo && echo yes || echo no)"; done
free -m
command -v fusermount3 >/dev/null && echo "fusermount3: $(command -v fusermount3)" || echo "fusermount3: missing (mount variants will not run)"
[[ -e /dev/fuse ]] && echo "/dev/fuse: present" || echo "/dev/fuse: missing"

section "kopia CLI flags used by spikes/kopia/restore.sh"
kopia --version
check_flag() { # check_flag NAME SUBCOMMAND...: prints the help line for --NAME, fails if it is missing
	local name=$1 line
	shift
	if line=$(kopia "$@" --help 2>&1 | grep -E -- "^ *--(\[no-\])?${name}([ =]|$)"); then
		echo "kopia $* $(sed -E 's/^ +//' <<<"$line")"
	else
		echo "MISSING FLAG: kopia $* --$name"
		return 1
	fi
}
flags_ok=1
check_flag object-splitter repository create filesystem || flags_ok=0
check_flag cache-directory repository create filesystem || flags_ok=0
check_flag cache-directory repository connect filesystem || flags_ok=0
check_flag content-cache-size-mb repository connect filesystem || flags_ok=0
check_flag content-cache-size-limit-mb repository connect filesystem || flags_ok=0
check_flag metadata-cache-size-mb repository connect filesystem || flags_ok=0
check_flag metadata-cache-size-limit-mb repository connect filesystem || flags_ok=0
check_flag parallel restore || flags_ok=0
check_flag write-sparse-files restore || flags_ok=0
for sub in show mount; do kopia "$sub" --help 2>&1 | grep -E "^usage: kopia $sub " || flags_ok=0; done
((flags_ok)) || { echo "UNKNOWN: a Kopia flag the spike needs is missing"; exit 3; }

section "test image"
if [[ -e "$IMAGE" ]]; then
	echo "reusing $IMAGE (delete it to rebuild)"
else
	SIZE_GB="$SIZE_GB" OUT="$IMAGE" "$repo_root/scripts/make-test-image.sh"
fi
ls -ls "$IMAGE"
df -h "$(dirname "$IMAGE")" ${DISK2:+"$DISK2"}

# The repository (~1/3 of the image), a full-size restore, Kopia's cache (up to the repository size) and the
# write test (WRITE_TEST_MIB, 8 GiB): ask for 2x the image size free, so a full disk shows up as UNKNOWN up front.
work="$(dirname "$IMAGE")/t5-restore-$(basename "$OUT_DIR")"
need_kib=$(($(stat -c %s "$IMAGE") * 2 / 1024))
free_kib=$(df -Pk "$(dirname "$IMAGE")" | awk 'NR == 2 { print $4 }')
((free_kib >= need_kib)) || { echo "UNKNOWN: $((free_kib / 1048576)) GiB free, needs $((need_kib / 1048576)) GiB"; exit 3; }
if [[ -n "$DISK2" ]]; then
	free2_kib=$(df -Pk "$DISK2" | awk 'NR == 2 { print $4 }')
	need2_kib=$(($(stat -c %s "$IMAGE") / 1024 + ${WRITE_TEST_MIB:-8192} * 1024))
	((free2_kib >= need2_kib)) || { echo "UNKNOWN: $DISK2 has $((free2_kib / 1048576)) GiB free, needs $((need2_kib / 1048576)) GiB"; exit 3; }
fi

section "run: spikes/kopia/restore.sh REPEAT=$REPEAT DISK2=${DISK2:-none}"
rc=0
env IMAGE="$IMAGE" WORK="$work" REPEAT="$REPEAT" DISK2="$DISK2" ${VARIANTS:+"VARIANTS=$VARIANTS"} \
	"$repo_root/spikes/kopia/restore.sh" || rc=$?
echo "exit: $rc"
if [[ -s "$work/report.json" ]]; then
	cp "$work/report.json" "$OUT_DIR/report.json"
else
	echo "UNKNOWN: no report.json"
	rc=3
fi
du -sh /root/.cache/kopia 2>/dev/null || true
[[ "${KEEP:-}" == 1 ]] || rm -rf "$work"

section "fstrim"
sync
fstrim -av || echo "WARNING: fstrim failed"
df -h "$(dirname "$IMAGE")" ${DISK2:+"$DISK2"}

section "report.json"
cat "$OUT_DIR/report.json" 2>/dev/null || echo "(missing)"

# A derived view for reading; the JSON above is the evidence.
section "summary (median of the good runs; speedup = restore-file median / this median)"
jq -r '.summary[] | [.variant, .verdict, "\(.runs_ok)/\(.runs_planned)", (.median_seconds // "-"),
	((.median_mib_s // 0) | round), ((.speedup_vs_restore_file // 0) * 100 | round / 100), (.median_cpu_busy_pct // null | if . then . * 10 | round / 10 else "-" end),
	(.median_iowait_pct // null | if . then . * 10 | round / 10 else "-" end), (((.median_written_bytes // 0) / 1073741824 * 10 | round) / 10),
	(((.median_content_cache_bytes_after // 0) / 1073741824 * 10 | round) / 10), (.identical // "n/a")] | @tsv' \
	"$OUT_DIR/report.json" 2>/dev/null |
	awk -F'\t' 'BEGIN { printf "%-22s %-8s %4s %9s %6s %7s %5s %6s %9s %8s %9s\n", "variant", "verdict", "ok", "median_s", "MiB/s", "speedup", "busy%", "iowt%", "wrote_GiB", "cache_GiB", "identical" }
	{ printf "%-22s %-8s %4s %9s %6s %7s %5s %6s %9s %8s %9s\n", $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11 }' ||
	echo "(no summary)"

section "result"
case $rc in
0) echo "OK" ;;
2) echo "CRITICAL" ;;
*) echo "UNKNOWN" ;;
esac
echo "(report.json in $OUT_DIR)"
((rc == 0 || rc == 2)) && exit "$rc"
exit 3
