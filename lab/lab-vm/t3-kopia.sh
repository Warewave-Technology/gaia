#!/usr/bin/env bash
# Runs the Track B gate on gaia-lab (T3, T4): builds the tools, makes the 30 GiB test image (once), checks
# the Kopia CLI flags the spike uses against the installed kopia, runs spikes/kopia/run.sh REPEAT times per
# config and evaluates the gate of ADR-004 §1 per config: median of full_backup / sha256_full_read_s over
# >= 3 runs <= the gate factor, and every restore byte-identical. Output goes verbatim into the report.
#
# Usage (on gaia-lab, as root, after setup.sh):  lab/lab-vm/t3-kopia.sh
# It takes long (~1 h for 30 GiB), so start it detached and follow the log:
#   systemd-run --unit=gaia-t3 --collect -E HOME=/root lab/lab-vm/t3-kopia.sh
# Env: SIZE_GB (30), IMAGE (/var/lib/gaia/test.img, built if missing),
#      CONFIGS ("s2-default/FIXED-4M", the ADR-004 §2 default): COMPRESSION[/SPLITTER[/PARALLEL]] per item,
#      e.g. "none/FIXED-4M/8"; an empty field keeps Kopia's default,
#      REPEAT (3): runs per config, in rounds (every config once, then again), so a slow period of the
#      shared host does not hit one config only,
#      OUT_DIR (/root/t3-<UTC timestamp>), KEEP=1 keeps the Kopia work dirs (default: removed, then fstrim).
# Exit: worst gate verdict of all configs, 0 OK / 1 WARNING / 2 CRITICAL / 3 UNKNOWN (also when the harness
#      failed). A config is UNKNOWN when it has fewer than 3 runs with a report, or any run of it was UNKNOWN.
set -euo pipefail

SIZE_GB="${SIZE_GB:-30}"
IMAGE="${IMAGE:-/var/lib/gaia/test.img}"
OUT_DIR="${OUT_DIR:-/root/t3-$(date -u +%Y%m%dT%H%M%SZ)}"
CONFIGS="${CONFIGS:-s2-default/FIXED-4M}"
REPEAT="${REPEAT:-3}"
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
# Any unexpected failure is a harness failure: UNKNOWN, never a verdict the run did not reach.
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
[[ "$REPEAT" =~ ^[1-9][0-9]*$ ]] || { echo "REPEAT must be a positive integer"; exit 3; }
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
check_flag cache-directory repository create filesystem || flags_ok=0
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

# One run needs the image copy, the repository (up to ~2x the allocated image after the incremental),
# a full-size restore and Kopia's cache (~10 GB). Ask for 3x the image size free before each run, so a
# full disk shows up as UNKNOWN up front instead of a failed restore.
need_kib=$(($(stat -c %s "$IMAGE") * 3 / 1024))
labels=()
runs=()
declare -A unknown_runs
for config in $CONFIGS; do
	label="${config//\//_}"
	labels+=("$label")
	unknown_runs[$label]=0
done
((${#labels[@]})) || { echo "UNKNOWN: CONFIGS is empty"; exit 3; }
for ((round = 1; round <= REPEAT; round++)); do
	for config in $CONFIGS; do
		IFS=/ read -r compression splitter parallel <<<"$config"
		label="${config//\//_}"
		run="${label}-r${round}"
		runs+=("$run")
		work="$(dirname "$IMAGE")/t3-kopia-${run}-$(basename "$OUT_DIR")"
		section "run $round/$REPEAT: spikes/kopia/run.sh COMPRESSION=$compression SPLITTER=${splitter:-default} PARALLEL=${parallel:-default}"
		free_kib=$(df -Pk "$(dirname "$IMAGE")" | awk 'NR == 2 { print $4 }')
		if ((free_kib < need_kib)); then
			echo "UNKNOWN: not run, $((free_kib / 1048576)) GiB free, needs $((need_kib / 1048576)) GiB"
			unknown_runs[$label]=$((unknown_runs[$label] + 1))
			continue
		fi
		rc=0
		IMAGE="$IMAGE" WORK="$work" COMPRESSION="$compression" SPLITTER="$splitter" PARALLEL="$parallel" \
			"$repo_root/spikes/kopia/run.sh" || rc=$?
		echo "exit: $rc"
		# The verdict comes from the report, and must agree with the exit code: run.sh also exits 1
		# when it cannot start, which must not read as WARNING.
		if [[ -s "$work/report.json" ]]; then
			cp "$work/report.json" "$OUT_DIR/report-${run}.json"
			case "$(jq -r .verdict "$work/report.json")" in
			OK) want=0 ;;
			WARNING) want=1 ;;
			CRITICAL) want=2 ;;
			*) want=3 ;;
			esac
			if ((rc != want)); then
				echo "UNKNOWN: exit $rc does not match the report's verdict"
				unknown_runs[$label]=$((unknown_runs[$label] + 1))
			fi
		else
			echo "UNKNOWN: no report.json"
			unknown_runs[$label]=$((unknown_runs[$label] + 1))
		fi
		df -h "$(dirname "$IMAGE")"
		[[ "${KEEP:-}" == 1 ]] || rm -rf "$work"
	done
done

# The lab VM's disk is a thin volume (discard=on): hand freed blocks back to the Proxmox pool.
# sync first: ext4 hands blocks freed by the rm above to the allocator only when the journal commits,
# and fstrim right after the rm of a full run trimmed 17.4 MiB in T3.
section "fstrim"
sync
fstrim -av || echo "WARNING: fstrim failed"

for run in "${runs[@]}"; do
	section "report-${run}.json"
	cat "$OUT_DIR/report-${run}.json" 2>/dev/null || echo "(missing)"
done

# A derived view for reading; the JSON above is the evidence.
section "runs (ratio = full_backup / sha256_full_read_s, the gate reference; ratio_qemu = full_backup / qemu-img, context)"
for run in "${runs[@]}"; do
	jq -r --arg r "$run" '[$r, .verdict, (.seconds.full_backup / .context.sha256_full_read_s * 100 | round / 100),
		(.seconds.full_backup / .seconds.baseline_qemu_img * 100 | round / 100), .seconds.full_backup,
		.context.sha256_full_read_s, (.context.plain_read_s // "-"), .gate.restore_identical] | @tsv' \
		"$OUT_DIR/report-${run}.json" 2>/dev/null || echo "$run (no report)"
done | awk -F'\t' 'BEGIN { printf "%-32s %-8s %6s %10s %9s %10s %10s %9s\n", "run", "verdict", "ratio", "ratio_qemu", "full_s", "sha256_s", "plain_s", "restores" }
	NF == 8 { printf "%-32s %-8s %6s %10s %9s %10s %10s %9s\n", $1, $2, $3, $4, $5, $6, $7, ($8 == "true" ? "identical" : "DIFFER"); next } { print }'

# ADR-004 §1: per config, the median of full_backup / sha256_full_read_s over >= 3 runs; every restore identical.
# shellcheck disable=SC2016 # $vars below are jq's, not the shell's
gate_jq='
def median: sort | length as $n | if $n == 0 then null elif $n % 2 == 1 then .[($n - 1) / 2] else (.[$n / 2 - 1] + .[$n / 2]) / 2 end;
(map(.seconds.full_backup / .context.sha256_full_read_s)) as $ratios
| (map(.seconds.full_backup / .seconds.baseline_qemu_img)) as $qemu
| (.[0].gate.speed.gate_factor // 2) as $factor
| ($ratios | median) as $m
| (map(.gate.restore_identical) | all) as $identical
| {config: $config, runs_planned: $repeat, runs_with_report: length, runs_unknown: $unknown,
   rule: "median(full_backup / sha256_full_read_s) over >= 3 runs <= gate_factor, all restores identical",
   gate_factor: $factor, full_vs_sha256_read: $ratios, median_full_vs_sha256_read: $m,
   context_median_full_vs_qemu_img: ($qemu | median), restores_identical: (length > 0 and $identical),
   verdict: (if length > 0 and ($identical | not) then "CRITICAL"
             elif $unknown > 0 or length < 3 then "UNKNOWN"
             elif $m <= $factor then "OK" else "WARNING" end)}'
section "gate (ADR-004 §1)"
for label in "${labels[@]}"; do
	files=()
	for ((round = 1; round <= REPEAT; round++)); do
		f="$OUT_DIR/report-${label}-r${round}.json"
		[[ -s "$f" ]] && files+=("$f")
	done
	if ((${#files[@]})); then jq -s . "${files[@]}"; else echo '[]'; fi |
		jq --arg config "$label" --argjson repeat "$REPEAT" --argjson unknown "${unknown_runs[$label]}" "$gate_jq"
done | jq -s . | tee "$OUT_DIR/gate.json"

# One verdict per config, or the gate was not evaluated.
[[ "$(jq length "$OUT_DIR/gate.json")" == "${#labels[@]}" ]] || { echo "UNKNOWN: gate.json is incomplete"; exit 3; }
worst=0
for v in $(jq -r '.[].verdict' "$OUT_DIR/gate.json"); do
	case $v in
	OK) rc=0 ;;
	WARNING) rc=1 ;;
	CRITICAL) rc=2 ;;
	*) rc=3 ;;
	esac
	((rc > worst)) && worst=$rc
done

section "result"
case $worst in
0) echo "OK" ;;
1) echo "WARNING" ;;
2) echo "CRITICAL" ;;
*) echo "UNKNOWN" ;;
esac
echo "(worst gate verdict of all configs; reports and gate.json in $OUT_DIR)"
exit "$worst"
