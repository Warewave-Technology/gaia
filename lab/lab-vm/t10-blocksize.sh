#!/usr/bin/env bash
# Runs T10 on gaia-lab: block size under real incremental write patterns (ADR-004 A3). Gate: N/A (decision input).
#
# Splitters (SPLITTERS): Kopia's fixed splitters; the point's block size follows the splitter (4M, 1M, 256K), so a
# partly changed block is read back and rewritten at that size. The list Kopia prints is logged and each splitter
# must be on it. Workloads (WORKLOADS), each in its own nested Debian 12 guest (official cloud image, prepared offline
# with virt-customize, no network):
#   random      T6's pattern: every 2 s one 4 MiB block and eight 64 KiB pieces at random offsets of a 1 GiB file
#   sequential  every 2 s a 4 MiB append to a growing file (a log / file copy), new file every 256 MiB
#   pgbench     PostgreSQL 15, pgbench scale 30, then pgbench -c 4 -R 500 (TPC-B like), checkpoint_timeout 30 s
# For every splitter x workload: a fresh guest from the workload's image, a repository on DISK2 (cache on tmpfs, A2),
# a full backup, then CYCLES incrementals CHURN_S seconds apart (gaia-backup backup, libvirt pull mode, as T6). Per
# incremental: dirty bytes, repository growth, growth / dirty, bytes read back from the repository, backup time. Before
# the last one the guest is paused for a reference (as T6); the latest point is restored (A2: 4 readers, tmpfs cache,
# holes) and compared with it.
#
# Usage (on gaia-lab, as root, after setup.sh and t5-disk2.sh):  lab/lab-vm/t10-blocksize.sh
#   systemd-run --unit=gaia-t10 --collect -E HOME=/root lab/lab-vm/t10-blocksize.sh
# Env: SPLITTERS ("FIXED-4M FIXED-1M FIXED-256K"), WORKLOADS ("random sequential pgbench"), CYCLES (3), CHURN_S (60),
#      DISK2 (/mnt/gaia-t5), DISK_GB (8), OUT_DIR (/root/t10-<UTC timestamp>), KEEP=1, ALLOW_TCG=1 (mock only).
# Exit: 0 every combination measured and every restore identical / 2 a restore differs / 3 UNKNOWN (missing data,
#      also when the harness failed).
set -euo pipefail

SPLITTERS="${SPLITTERS:-FIXED-4M FIXED-1M FIXED-256K}"
WORKLOADS="${WORKLOADS:-random sequential pgbench}"
CYCLES="${CYCLES:-3}"
CHURN_S="${CHURN_S:-60}"
DISK2="${DISK2:-/mnt/gaia-t5}"
DISK_GB="${DISK_GB:-8}"
OUT_DIR="${OUT_DIR:-/root/t10-$(date -u +%Y%m%dT%H%M%SZ)}"
DOM=gaia-t10
IMAGES=/var/lib/gaia/images
CLOUD_URL=https://cloud.debian.org/images/cloud/bookworm/latest
case $(uname -m) in
x86_64) CLOUD_IMG=debian-12-genericcloud-amd64.qcow2 TTY=ttyS0 ;;
aarch64) CLOUD_IMG=debian-12-genericcloud-arm64.qcow2 TTY=ttyAMA0 ;;
*) echo "UNKNOWN: unsupported architecture $(uname -m)"; exit 3 ;;
esac
WORK=/var/lib/gaia/t10
LV_IMAGES=/var/lib/libvirt/images
DISK="$LV_IMAGES/$DOM.qcow2"
SERIAL=/var/log/libvirt/qemu/$DOM-serial.log
CACHE=/run/gaia-t10/cache
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
[[ "$CYCLES" =~ ^[1-9][0-9]*$ ]] || { echo "CYCLES must be >= 1"; exit 3; }
for bin in go jq make qemu-img virsh virt-customize kopia curl sha512sum fstrim mountpoint; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done
[[ -d "$DISK2" ]] || { echo "UNKNOWN: no second disk at $DISK2 (lab/lab-vm/t5-disk2.sh)"; exit 3; }
mkdir -p "$OUT_DIR" "$WORK" "$IMAGES" /run/gaia-t10
exec > >(tee "$OUT_DIR/t10.log") 2>&1
ln -sfn "$OUT_DIR" /root/t10-latest

section() { printf '\n== %s\n' "$*"; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
now() { date +%s.%N; }
elapsed() { awk -v a="$(now)" -v b="$1" 'BEGIN { printf "%.3f", a - b }'; }

# shellcheck disable=SC2329 # invoked by the trap
cleanup() {
	virsh -q destroy "$DOM" >/dev/null 2>&1 || true
	virsh -q undefine "$DOM" --checkpoints-metadata --nvram >/dev/null 2>&1 || virsh -q undefine "$DOM" --checkpoints-metadata >/dev/null 2>&1 || true
	rm -f "$DISK" "$LV_IMAGES"/gaia-scratch-"$DOM"-* || true
	mountpoint -q "$CACHE" && umount "$CACHE"
	if [[ "${KEEP:-}" != 1 ]]; then rm -rf "$WORK" "$DISK2/t10-repo"; fi
	rm -rf /run/gaia-t10
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

section "virtualisation"
virt_type=kvm
if [[ ! -e /dev/kvm && "${ALLOW_TCG:-}" == 1 ]]; then
	virt_type=qemu
	echo "WARNING: no /dev/kvm, ALLOW_TCG=1: emulated guests (mock run, not a T10 measurement)"
elif [[ ! -e /dev/kvm ]]; then
	echo "UNKNOWN: /dev/kvm is missing (see T6)"
	exit 3
fi
virsh -q version | head -4

# CLAUDE.md T10: the splitter names come from the list Kopia prints, not from assumptions.
section "splitters Kopia offers (kopia benchmark splitter)"
kopia --version | head -1
kopia benchmark splitter --data-size=4MB --block-count=1 >"$OUT_DIR/kopia-splitters.txt" 2>&1 || true
grep -o -E '\bFIXED-[0-9]+[KM]\b' "$OUT_DIR/kopia-splitters.txt" | sort -u | tr '\n' ' '
echo
block_size() { # the point's block size for a fixed splitter
	case $1 in
	FIXED-*K) echo $((${1//[!0-9]/} * 1024)) ;;
	FIXED-*M) echo $((${1//[!0-9]/} * 1048576)) ;;
	*) return 1 ;;
	esac
}
for sp in $SPLITTERS; do
	grep -q -E "\\b$sp\\b" "$OUT_DIR/kopia-splitters.txt" || { echo "UNKNOWN: Kopia does not list $sp"; exit 3; }
	echo "$sp: block size $(block_size "$sp")"
done

section "free space"
free2=$(($(df -Pk "$DISK2" | awk 'NR == 2 { print $4 }') * 1024))
free1=$(($(df -Pk "$LV_IMAGES" | awk 'NR == 2 { print $4 }') * 1024))
((free2 >= 20 << 30)) || { echo "UNKNOWN: $DISK2 has $((free2 >> 30)) GiB free, needs 20"; exit 3; }
((free1 >= 40 << 30)) || { echo "UNKNOWN: $LV_IMAGES has $((free1 >> 30)) GiB free, needs 40"; exit 3; }
df -h "$DISK2" "$LV_IMAGES"

section "guest image"
if [[ ! -s "$IMAGES/$CLOUD_IMG" ]]; then
	curl -fsSL "$CLOUD_URL/$CLOUD_IMG" -o "$IMAGES/$CLOUD_IMG.part" && mv "$IMAGES/$CLOUD_IMG.part" "$IMAGES/$CLOUD_IMG"
fi
curl -fsSL "$CLOUD_URL/SHA512SUMS" -o "$WORK/SHA512SUMS"
grep " $CLOUD_IMG\$" "$WORK/SHA512SUMS"
(cd "$IMAGES" && grep " $CLOUD_IMG\$" "$WORK/SHA512SUMS" | sha512sum -c -)

# The workloads, one script; the guest reads its workload from /etc/gaia-workload. Progress goes to the serial log.
cat >"$WORK/gaia-wl" <<'EOF'
#!/bin/bash
set -eu
wl=$(cat /etc/gaia-workload)
tty=/dev/@TTY@
d=/var/lib/gaia-wl
mkdir -p "$d"
total=0
case $wl in
random)
	[ -s "$d/data" ] || dd if=/dev/urandom of="$d/data" bs=4M count=256 conv=fsync status=none
	echo GAIA-READY >"$tty"
	while true; do
		dd if=/dev/urandom of="$d/data" bs=4M count=1 seek=$(((RANDOM * 32768 + RANDOM) % 256)) conv=notrunc,fsync status=none
		for _ in 1 2 3 4 5 6 7 8; do
			dd if=/dev/urandom of="$d/data" bs=64K count=1 seek=$(((RANDOM * 32768 + RANDOM) % 16384)) conv=notrunc,fsync status=none
		done
		total=$((total + 4194304 + 8 * 65536))
		echo "GAIA-CHURN bytes=$total" >"$tty"
		sleep 2
	done
	;;
sequential)
	echo GAIA-READY >"$tty"
	n=0
	while true; do
		dd if=/dev/urandom of="$d/seq-$((n / 64))" bs=4M count=1 oflag=append conv=notrunc,fsync status=none
		n=$((n + 1))
		total=$((total + 4194304))
		echo "GAIA-CHURN bytes=$total" >"$tty"
		sleep 2
	done
	;;
pgbench)
	until pg_isready -q; do sleep 1; done
	if ! su postgres -c "psql -lqt" | cut -d'|' -f1 | grep -qw bench; then
		su postgres -c "createdb bench"
		su postgres -c "pgbench -i -s 30 -q bench" >/var/log/gaia-pgbench-init.log 2>&1
	fi
	su postgres -c "psql -qAt -c checkpoint bench"
	sync
	echo GAIA-READY >"$tty"
	exec su postgres -c "pgbench -c 4 -j 2 -T 86400 -R 500 -P 30 bench" >/var/log/gaia-pgbench.log 2>&1
	;;
esac
EOF
sed -i "s|@TTY@|$TTY|" "$WORK/gaia-wl"
cat >"$WORK/gaia-wl.service" <<'EOF'
[Unit]
Description=Gaia T10 workload
After=local-fs.target postgresql.service
[Service]
ExecStart=/usr/local/sbin/gaia-wl
[Install]
WantedBy=multi-user.target
EOF

declare -A WL_IMG
for wl in $WORKLOADS; do
	log "preparing the $wl image"
	img="$WORK/base-$wl.qcow2"
	qemu-img convert -q -f qcow2 -O qcow2 "$IMAGES/$CLOUD_IMG" "$img"
	qemu-img resize -q "$img" "${DISK_GB}G"
	extra=()
	if [[ $wl == pgbench ]]; then
		# Installing PostgreSQL needs the network, inside the libguestfs appliance only; the guest itself has none.
		# shellcheck disable=SC2016 # $f expands inside the guest
		extra=(--install postgresql --run-command 'for f in /etc/postgresql/*/main/postgresql.conf; do echo "checkpoint_timeout = 30s" >>"$f"; done')
	fi
	LIBGUESTFS_BACKEND=direct virt-customize -q -a "$img" --hostname "gaia-t10-$wl" \
		--write "/etc/gaia-workload:$wl" \
		--upload "$WORK/gaia-wl:/usr/local/sbin/gaia-wl" --chmod 0755:/usr/local/sbin/gaia-wl \
		--upload "$WORK/gaia-wl.service:/etc/systemd/system/gaia-wl.service" \
		--run-command 'systemctl enable gaia-wl.service' \
		--run-command 'systemctl mask systemd-networkd-wait-online.service' \
		--touch /etc/cloud/cloud-init.disabled "${extra[@]}"
	WL_IMG[$wl]=$img
done

mkdir -p "$CACHE"
mount -t tmpfs -o size=2g tmpfs "$CACHE"
GAIA_REPO_PASSWORD=$(head -c 24 /dev/urandom | base64)
export GAIA_REPO_PASSWORD
gb() { "$BIN/gaia-backup" "$@"; }

start_guest() { # start_guest IMAGE
	virsh -q destroy "$DOM" >/dev/null 2>&1 || true
	virsh -q undefine "$DOM" --checkpoints-metadata --nvram >/dev/null 2>&1 || virsh -q undefine "$DOM" --checkpoints-metadata >/dev/null 2>&1 || true
	cp --sparse=always "$1" "$DISK"
	rm -f "$SERIAL"
	local os emulator cpu
	if [[ $(uname -m) == x86_64 ]]; then
		os="<os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><features><acpi/><apic/></features>"
		emulator=/usr/bin/qemu-system-x86_64
	else
		os="<os firmware='efi'><type arch='aarch64' machine='virt'>hvm</type><boot dev='hd'/></os><features><acpi/><gic version='3'/></features>"
		emulator=/usr/bin/qemu-system-aarch64
	fi
	cpu="<cpu mode='host-passthrough'/>"
	[[ $virt_type == kvm ]] || cpu="<cpu mode='custom'><model>max</model></cpu>"
	cat >"$WORK/$DOM.xml" <<EOF
<domain type='$virt_type'>
  <name>$DOM</name>
  <memory unit='MiB'>1536</memory>
  <vcpu>2</vcpu>
  $os
  $cpu
  <devices>
    <emulator>$emulator</emulator>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2' discard='unmap'/><source file='$DISK'/><target dev='vda' bus='virtio'/></disk>
    <serial type='file'><source path='$SERIAL'/><target port='0'/></serial>
  </devices>
</domain>
EOF
	virsh -q define "$WORK/$DOM.xml" >/dev/null
	virsh -q start "$DOM"
	local i
	for ((i = 0; ; i++)); do
		grep -q GAIA-READY "$SERIAL" 2>/dev/null && break
		((i < 240)) || { echo "UNKNOWN: no GAIA-READY after 1200 s"; tail -30 "$SERIAL" || true; exit 3; }
		sleep 5
	done
}
churn() { { grep -a -o 'GAIA-CHURN bytes=[0-9]*' "$SERIAL" || true; } | tail -1 | cut -d= -f2; } # pgbench reports none

: >"$OUT_DIR/cycles.jsonl"
: >"$OUT_DIR/restores.jsonl"
section "measurements"
for wl in $WORKLOADS; do
	for sp in $SPLITTERS; do
		bs=$(block_size "$sp")
		log "$wl x $sp (block $bs): guest"
		start_guest "${WL_IMG[$wl]}"
		rm -rf "$DISK2/t10-repo" "$WORK/kopia.config" "${CACHE:?}"/*
		gb repo-create -repo "$DISK2/t10-repo" -config "$WORK/kopia.config" -splitter "$sp" -cache-dir "$CACHE" -cache-limit-mb 1024 >/dev/null
		for ((c = 0; c <= CYCLES; c++)); do
			((c == 0)) || sleep "$CHURN_S"
			last=$((c == CYCLES))
			resume=()
			if ((last)); then # the reference of the point that will be restored, as T6
				virsh -q suspend "$DOM"
				qemu-img convert -U -q -f qcow2 -O raw "$DISK" "$WORK/ref.raw"
				ref_sha=$("$BIN/imgtool" hash -file "$WORK/ref.raw" | jq -r .sha256)
				rm -f "$WORK/ref.raw"
				resume=(-resume)
			fi
			before=$(du -sb "$DISK2/t10-repo" | cut -f1)
			guest=$(churn)
			gb backup -config "$WORK/kopia.config" -domain "$DOM" -disk vda -nbd tcp:127.0.0.1:10809 -conns 4 -block-size "$bs" \
				-scratch-dir "$LV_IMAGES" "${resume[@]}" -json >"$WORK/backup.json"
			[[ "$(virsh -q domstate "$DOM")" == running ]] || virsh -q resume "$DOM" || true
			after=$(du -sb "$DISK2/t10-repo" | cut -f1)
			jq -c --arg wl "$wl" --arg sp "$sp" --argjson bs "$bs" --argjson c "$c" --argjson growth "$((after - before))" \
				--argjson repo "$after" --arg guest "${guest:-0}" '
				{workload: $wl, splitter: $sp, block_size: $bs, cycle: $c, kind, dirty_bytes: .changed_bytes,
				 nbd_requested_bytes, repo_growth_bytes: $growth, repo_bytes_after: $repo,
				 growth_per_dirty: (if .changed_bytes > 0 then $growth / .changed_bytes else null end),
				 repo_read_back_bytes: .point.stats.repo_read_bytes, blocks_written: .point.stats.blocks_written,
				 blocks_reused: .point.stats.blocks_reused, backup_s: .timings_s.total, data_s: .timings_s.data,
				 guest_workload_bytes_total: ($guest | tonumber), point_id: .point.id}' "$WORK/backup.json" | tee -a "$OUT_DIR/cycles.jsonl"
		done
		virsh -q destroy "$DOM"
		point=$(jq -r .point.id "$WORK/backup.json")
		rm -rf "${CACHE:?}"/* "$WORK/restored.raw"
		sync
		echo 3 >/proc/sys/vm/drop_caches
		t0=$(now)
		gb restore -config "$WORK/kopia.config" -point "$point" -out "$WORK/restored.raw" -workers 4 -json >"$WORK/restore.json"
		restore_s=$(elapsed "$t0")
		got=$("$BIN/imgtool" hash -file "$WORK/restored.raw" | jq -r .sha256)
		jq -c --arg wl "$wl" --arg sp "$sp" --argjson s "$restore_s" --arg got "$got" --arg ref "$ref_sha" \
			'{workload: $wl, splitter: $sp, restore_s: $s, identical: ($got == $ref), restored_sha256: $got, reference_sha256: $ref, stats}' \
			"$WORK/restore.json" | tee -a "$OUT_DIR/restores.jsonl"
		rm -f "$WORK/restored.raw"
		rm -rf "$DISK2/t10-repo"
	done
done

section "summary (medians over the incrementals of each splitter x workload)"
jq -n --slurpfile cy "$OUT_DIR/cycles.jsonl" --slurpfile rs "$OUT_DIR/restores.jsonl" --argjson cycles "$CYCLES" '
	def median: map(select(. != null)) | sort | length as $n | if $n == 0 then null elif $n % 2 == 1 then .[($n - 1) / 2] else (.[$n / 2 - 1] + .[$n / 2]) / 2 end;
	[$cy | group_by([.workload, .splitter])[] as $g
	 | ($g | map(select(.cycle > 0))) as $inc | ($g | map(select(.cycle == 0)) | first) as $full
	 | ($rs | map(select(.workload == $g[0].workload and .splitter == $g[0].splitter)) | first) as $r
	 | {workload: $g[0].workload, splitter: $g[0].splitter, block_size: $g[0].block_size, incrementals: ($inc | length),
	    full: {repo_bytes: $full.repo_bytes_after, backup_s: $full.backup_s},
	    median_dirty_bytes: ($inc | map(.dirty_bytes) | median),
	    median_repo_growth_bytes: ($inc | map(.repo_growth_bytes) | median),
	    median_growth_per_dirty: ($inc | map(.growth_per_dirty) | median),
	    median_repo_read_back_bytes: ($inc | map(.repo_read_back_bytes) | median),
	    median_incremental_backup_s: ($inc | map(.backup_s) | median),
	    restore_latest_s: $r.restore_s, restore_identical: $r.identical}]
	| sort_by(.workload, .block_size) | reverse | sort_by(.workload)
	| {combinations: ., complete: (length > 0 and all(.[]; .incrementals == $cycles and .restore_identical != null)),
	   all_identical: all(.[]; .restore_identical == true)}' | tee "$OUT_DIR/summary.json"

section "result"
if jq -e '.combinations | any(.restore_identical == false)' "$OUT_DIR/summary.json" >/dev/null; then
	echo "CRITICAL: a restore differs from its reference"
	exit 2
elif jq -e '.complete and .all_identical' "$OUT_DIR/summary.json" >/dev/null; then
	echo "OK: every combination measured, every restore identical (gate N/A: the numbers are the decision input)"
	exit 0
fi
echo "UNKNOWN: a combination is incomplete"
exit 3
