#!/usr/bin/env bash
# Runs the T3 gate on gaia-lab: builds the tools, makes the 30 GiB test image (once), checks the
# Kopia CLI flags the spike uses against the installed kopia, and runs spikes/kopia/run.sh with
# COMPRESSION=zstd and COMPRESSION=none. Output goes verbatim into the T3 report.
#
# Usage (on gaia-lab, as root, after setup.sh):  lab/lab-vm/t3-kopia.sh
# It takes long (~1 h for 30 GiB), so start it detached and follow the log:
#   systemd-run --unit=gaia-t3 --collect -E HOME=/root lab/lab-vm/t3-kopia.sh
# Env: SIZE_GB (30), IMAGE (/var/lib/gaia/test.img, built if missing), SPLITTER (empty = Kopia default),
#      OUT_DIR (/root/t3-<UTC timestamp>), KEEP=1 keeps the Kopia work dirs (default: removed, then fstrim).
# Exit: worst verdict of the two runs, 0 OK / 1 WARNING / 2 CRITICAL / 3 UNKNOWN (also when the harness failed).
set -euo pipefail

SIZE_GB="${SIZE_GB:-30}"
IMAGE="${IMAGE:-/var/lib/gaia/test.img}"
OUT_DIR="${OUT_DIR:-/root/t3-$(date -u +%Y%m%dT%H%M%SZ)}"
export SPLITTER="${SPLITTER:-}"
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
# Any unexpected failure is a harness failure: UNKNOWN, never a verdict the run did not reach.
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in go kopia qemu-img jq make mkfs.ext4 fstrim; do command -v "$bin" >/dev/null || { echo "MISSING: $bin (run setup.sh)"; exit 3; }; done
mkdir -p "$OUT_DIR"
exec > >(tee "$OUT_DIR/t3.log") 2>&1
ln -sfn "$OUT_DIR" /root/t3-latest

section() { printf '\n== %s\n' "$*"; }

section "date / commit"
date -u +%Y-%m-%dT%H:%M:%SZ
git -C "$repo_root" rev-parse HEAD 2>/dev/null || echo "not a git checkout"
echo "output: $OUT_DIR"

# Build as the checkout's owner (see setup.sh), so the tools that run are the ones committed.
section "build"
owner=$(stat -c %U "$repo_root")
if [[ "$owner" == root ]]; then
	make -C "$repo_root" -s build
else
	runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" make -C "$repo_root" -s build
fi
go version -m "$repo_root/bin/imgtool" | grep -E 'vcs\.(revision|modified)' || echo "no VCS stamp"

# CLAUDE.md T3: the flags the spike uses, checked against the installed kopia's own help.
section "kopia CLI flags used by spikes/kopia/run.sh"
kopia --version
check_flag() { # check_flag NAME SUBCOMMAND...: prints the help line for --NAME, fails if it is missing
	local name=$1 line
	shift
	# kingpin prints value flags as "--name=VALUE" and booleans as "--[no-]name".
	if line=$(kopia "$@" --help 2>&1 | grep -E -- "^ *--(\[no-\])?${name}([ =]|$)"); then
		echo "kopia $* $(sed -E 's/^ +//' <<<"$line")"
	else
		echo "MISSING FLAG: kopia $* --$name"
		return 1
	fi
}
flags_ok=1
check_flag path repository create filesystem || flags_ok=0
check_flag object-splitter repository create filesystem || flags_ok=0
check_flag compression policy set || flags_ok=0
check_flag global policy set || flags_ok=0
check_flag json snapshot list || flags_ok=0
check_flag json repository status || flags_ok=0
kopia restore --help 2>&1 | grep -E '^usage: kopia restore' || flags_ok=0
((flags_ok)) || { echo "UNKNOWN: a Kopia flag the spike needs is missing"; exit 3; }

section "test image"
if [[ -e "$IMAGE" ]]; then
	echo "reusing $IMAGE (delete it to rebuild)"
	"$repo_root/bin/imgtool" hash -file "$IMAGE"
else
	SIZE_GB="$SIZE_GB" OUT="$IMAGE" "$repo_root/scripts/make-test-image.sh"
fi
ls -ls "$IMAGE"
df -h "$(dirname "$IMAGE")"

worst=0
for compression in zstd none; do
	work="$(dirname "$IMAGE")/t3-kopia-${compression}-$(basename "$OUT_DIR")"
	section "spikes/kopia/run.sh COMPRESSION=$compression SPLITTER=${SPLITTER:-default}"
	rc=0
	IMAGE="$IMAGE" WORK="$work" COMPRESSION="$compression" "$repo_root/spikes/kopia/run.sh" || rc=$?
	echo "exit: $rc"
	# The verdict comes from the report, and must agree with the exit code: run.sh also exits 1
	# when it cannot start, which must not read as WARNING.
	if [[ -s "$work/report.json" ]]; then
		cp "$work/report.json" "$OUT_DIR/report-${compression}.json"
		case "$(jq -r .verdict "$work/report.json")" in
		OK) want=0 ;;
		WARNING) want=1 ;;
		CRITICAL) want=2 ;;
		*) want=3 ;;
		esac
		((rc == want)) || { echo "UNKNOWN: exit $rc does not match the report's verdict"; rc=3; }
	else
		echo "UNKNOWN: no report.json"
		rc=3
	fi
	df -h "$(dirname "$IMAGE")"
	[[ "${KEEP:-}" == 1 ]] || rm -rf "$work"
	((rc > worst)) && worst=$rc
done

# The lab VM's disk is a thin volume (discard=on): hand freed blocks back to the Proxmox pool.
section "fstrim"
fstrim -av || echo "WARNING: fstrim failed"

for compression in zstd none; do
	section "report-${compression}.json"
	cat "$OUT_DIR/report-${compression}.json" 2>/dev/null || echo "(missing)"
done

section "result"
case $worst in
0) echo "OK" ;;
1) echo "WARNING" ;;
2) echo "CRITICAL" ;;
*) echo "UNKNOWN" ;;
esac
echo "(worst of both runs; reports in $OUT_DIR)"
exit "$worst"
