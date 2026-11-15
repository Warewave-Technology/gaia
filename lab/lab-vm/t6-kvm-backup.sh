#!/usr/bin/env bash
# Runs T6 on gaia-lab: an incremental KVM backup of a real Linux guest (nested KVM) with gaia-backup
# (libvirt pull-mode backup via go-libvirt, NBD via internal/nbd, Kopia Go library), and checks it.
#
# The guest is Debian 12's official cloud image with a cloud-init workload that keeps writing to its disk.
# For every point (one full, then INCREMENTALS incrementals, CHURN_S seconds of guest writes apart):
#   1. pause the guest and capture the reference: the disk as it is, converted to raw, sha256;
#   2. gaia-backup starts the backup job (and the checkpoint) while the guest is paused, resumes it, and
#      reads the disk over NBD while the guest writes again (pull mode serves the checkpoint state).
# Then every point is restored and compared with its reference. Gates (CLAUDE.md T6):
#   (a) an incremental reads only the dirty extents: NBD bytes requested == dirty bitmap size, < the full's;
#   (b) the restored full point is byte-identical to its reference;
#   (c) every restored incremental point is byte-identical to its reference.
#
# Usage (on gaia-lab, as root, after setup.sh):  lab/lab-vm/t6-kvm-backup.sh
#   systemd-run --unit=gaia-t6 --collect -E HOME=/root lab/lab-vm/t6-kvm-backup.sh
# Env: INCREMENTALS (2), CHURN_S (60), DISK_GB (8, guest disk), CONNS (4, NBD connections),
#      WORKERS (4, restore readers), CACHE_TMPFS_MB (1024: Kopia cache on tmpfs with this hard limit, T5;
#      0 = Kopia's default cache), OUT_DIR (/root/t6-<UTC timestamp>), KEEP=1 keeps the work dir,
#      ALLOW_TCG=1 (mock only: no /dev/kvm, emulate; also runs on aarch64 with the arm64 image).
# Downloads (once, cached in /var/lib/gaia/images): Debian 12 genericcloud amd64 qcow2, checked against
# Debian's SHA512SUMS. The guest has no network interface.
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

INCREMENTALS="${INCREMENTALS:-2}"
CHURN_S="${CHURN_S:-60}"
DISK_GB="${DISK_GB:-8}"
CONNS="${CONNS:-4}"
WORKERS="${WORKERS:-4}"
CACHE_TMPFS_MB="${CACHE_TMPFS_MB:-1024}"
OUT_DIR="${OUT_DIR:-/root/t6-$(date -u +%Y%m%dT%H%M%SZ)}"
DOM=gaia-t6
IMAGES=/var/lib/gaia/images
CLOUD_URL=https://cloud.debian.org/images/cloud/bookworm/latest
case $(uname -m) in
x86_64) CLOUD_IMG=debian-12-genericcloud-amd64.qcow2 TTY=ttyS0 ;;
aarch64) CLOUD_IMG=debian-12-genericcloud-arm64.qcow2 TTY=ttyAMA0 ;;
*) echo "UNKNOWN: unsupported architecture $(uname -m)"; exit 3 ;;
esac
WORK=/var/lib/gaia/t6
LV_IMAGES=/var/lib/libvirt/images
DISK="$LV_IMAGES/$DOM.qcow2"
SEED="$LV_IMAGES/$DOM-seed.iso"
SERIAL=/var/log/libvirt/qemu/$DOM-serial.log
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
[[ "$INCREMENTALS" =~ ^[1-9][0-9]*$ ]] || { echo "INCREMENTALS must be >= 1 (gate c needs an incremental)"; exit 3; }
for bin in go kopia jq make qemu-img virsh cloud-localds curl sha512sum fstrim; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done
mkdir -p "$OUT_DIR"
exec > >(tee "$OUT_DIR/t6.log") 2>&1
ln -sfn "$OUT_DIR" /root/t6-latest

section() { printf '\n== %s\n' "$*"; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

cleanup() {
	virsh -q destroy "$DOM" >/dev/null 2>&1 || true
	virsh -q undefine "$DOM" --checkpoints-metadata >/dev/null 2>&1 || true
	rm -f "$DISK" "$SEED" "$LV_IMAGES"/gaia-scratch-"$DOM"-* || true
	mountpoint -q "$WORK/cache" && umount "$WORK/cache" || true
	[[ "${KEEP:-}" == 1 ]] || rm -rf "$WORK"
}
# shellcheck disable=SC2329 # invoked by the trap
on_exit() {
	cleanup
	sync
	fstrim -a >/dev/null 2>&1 || true
}
trap on_exit EXIT

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
go version -m "$BIN/gaia-backup" | grep -E 'vcs\.(revision|modified)|kopia/kopia|go-libvirt' || echo "no VCS stamp"

# Nested KVM: gaia-lab needs /dev/kvm. If it is missing, the fix is on the Proxmox host or in the kernel.
section "nested KVM"
uname -r
grep -m1 -o -w -E 'svm|vmx' /proc/cpuinfo || echo "no svm/vmx flag in the vCPU"
if [[ ! -e /dev/kvm ]]; then
	modprobe kvm_amd 2>/dev/null || modprobe kvm_intel 2>/dev/null || true
fi
virt_type=kvm
if [[ ! -e /dev/kvm && "${ALLOW_TCG:-}" == 1 ]]; then
	virt_type=qemu
	echo "WARNING: no /dev/kvm, ALLOW_TCG=1: the guest is emulated (mock run, not a T6 measurement)"
elif [[ ! -e /dev/kvm ]]; then
	echo "UNKNOWN: /dev/kvm is missing in gaia-lab, so there is no nested guest to back up."
	echo "  Check on the Proxmox host: cat /sys/module/kvm_amd/parameters/nested  (must be 1)"
	echo "  and: qm config 9102 | grep ^cpu  (must be cpu: host)"
	echo "  If both are right, the guest kernel may lack KVM: modinfo kvm_amd; if missing,"
	echo "  apt-get install -y linux-image-amd64 && reboot  (Debian's standard kernel instead of -cloud)"
	exit 3
fi
ls -l /dev/kvm 2>/dev/null || true
virsh -q version
free -m

section "free space"
mkdir -p "$IMAGES" "$WORK"
# Guest disk (~2.5 GiB used), a raw reference and a restore per point, the repository: ask for 40 GiB.
free_kib=$(df -Pk "$WORK" | awk 'NR == 2 { print $4 }')
((free_kib >= 40 * 1048576)) || { echo "UNKNOWN: $((free_kib / 1048576)) GiB free on $WORK, needs 40 GiB"; exit 3; }
df -h "$WORK" "$LV_IMAGES"

section "guest image"
if [[ ! -s "$IMAGES/$CLOUD_IMG" ]]; then
	curl -fsSL "$CLOUD_URL/$CLOUD_IMG" -o "$IMAGES/$CLOUD_IMG.part"
	mv "$IMAGES/$CLOUD_IMG.part" "$IMAGES/$CLOUD_IMG"
fi
curl -fsSL "$CLOUD_URL/SHA512SUMS" -o "$IMAGES/SHA512SUMS"
grep " $CLOUD_IMG\$" "$IMAGES/SHA512SUMS"
(cd "$IMAGES" && grep " $CLOUD_IMG\$" SHA512SUMS | sha512sum -c -) || { echo "UNKNOWN: guest image checksum mismatch"; rm -f "$IMAGES/$CLOUD_IMG"; exit 3; }
qemu-img info "$IMAGES/$CLOUD_IMG" | grep -E 'virtual size|disk size'

section "guest"
cleanup
mkdir -p "$WORK" "$WORK/ref" "$WORK/restore"
qemu-img convert -q -f qcow2 -O qcow2 "$IMAGES/$CLOUD_IMG" "$DISK"
qemu-img resize -q "$DISK" "${DISK_GB}G"
cat >"$WORK/meta-data" <<EOF
instance-id: $DOM-$(date +%s)
local-hostname: $DOM
EOF
# The workload: 1 GiB of random data at first boot, then every 2 s one 4 MiB block and eight 64 KiB pieces
# rewritten at random offsets (whole and partial 4 MiB blocks), with fsync. Progress goes to the serial log.
cat >"$WORK/user-data" <<'EOF'
#cloud-config
write_files:
  - path: /usr/local/sbin/gaia-churn
    permissions: '0755'
    content: |
      #!/bin/bash
      set -eu
      f=/var/lib/gaia-churn/data
      mkdir -p /var/lib/gaia-churn
      [ -s "$f" ] || dd if=/dev/urandom of="$f" bs=4M count=256 conv=fsync status=none
      echo "GAIA-READY" > /dev/@TTY@
      total=0
      while true; do
        dd if=/dev/urandom of="$f" bs=4M count=1 seek=$(( (RANDOM * 32768 + RANDOM) % 256 )) conv=notrunc,fsync status=none
        for _ in 1 2 3 4 5 6 7 8; do
          dd if=/dev/urandom of="$f" bs=64K count=1 seek=$(( (RANDOM * 32768 + RANDOM) % 16384 )) conv=notrunc,fsync status=none
        done
        total=$((total + 4194304 + 8 * 65536))
        echo "GAIA-CHURN bytes=$total" > /dev/@TTY@
        sleep 2
      done
  - path: /etc/systemd/system/gaia-churn.service
    content: |
      [Unit]
      Description=Gaia T6 test workload
      [Service]
      ExecStart=/usr/local/sbin/gaia-churn
      [Install]
      WantedBy=multi-user.target
runcmd:
  - [systemctl, daemon-reload]
  - [systemctl, enable, --now, gaia-churn.service]
EOF
sed -i "s/@TTY@/$TTY/g" "$WORK/user-data"
cloud-localds "$SEED" "$WORK/user-data" "$WORK/meta-data"
rm -f "$SERIAL"
if [[ $(uname -m) == x86_64 ]]; then
	os="<os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><features><acpi/><apic/></features>"
	emulator=/usr/bin/qemu-system-x86_64
else
	os="<os firmware='efi'><type arch='aarch64' machine='virt'>hvm</type><boot dev='hd'/></os><features><acpi/><gic version='3'/></features>"
	emulator=/usr/bin/qemu-system-aarch64
fi
cpu="<cpu mode='host-passthrough'/>"
[[ $virt_type == kvm ]] || cpu="<cpu mode='custom'><model>max</model></cpu>"
# The cloud-init seed is a read-only second disk (vdb); it is left out of backups and checkpoints.
cat >"$WORK/$DOM.xml" <<EOF
<domain type='$virt_type'>
  <name>$DOM</name>
  <memory unit='MiB'>1024</memory>
  <vcpu>2</vcpu>
  $os
  $cpu
  <devices>
    <emulator>$emulator</emulator>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2' discard='unmap'/><source file='$DISK'/><target dev='vda' bus='virtio'/></disk>
    <disk type='file' device='disk'><driver name='qemu' type='raw'/><source file='$SEED'/><target dev='vdb' bus='virtio'/><readonly/></disk>
    <serial type='file'><source path='$SERIAL'/><target port='0'/></serial>
  </devices>
</domain>
EOF
virsh -q define "$WORK/$DOM.xml" >/dev/null
virsh -q start "$DOM"
log "guest started, waiting for the workload (GAIA-READY on the serial console)"
for ((i = 0; ; i++)); do
	grep -q GAIA-READY "$SERIAL" 2>/dev/null && break
	((i < 240)) || { echo "UNKNOWN: no GAIA-READY after 1200 s"; tail -30 "$SERIAL" || true; exit 3; }
	sleep 5
done
log "guest ready: $(grep -m1 -a 'PRETTY_NAME\|Debian GNU/Linux 12' "$SERIAL" | tr -d '\r' || echo 'Debian 12')"

section "repository (Kopia Go library, FIXED-4M, s2-default)"
GAIA_REPO_PASSWORD=$(head -c 24 /dev/urandom | base64)
export GAIA_REPO_PASSWORD
cache_args=()
if ((CACHE_TMPFS_MB > 0)); then
	mkdir -p "$WORK/cache"
	mount -t tmpfs -o size=$((CACHE_TMPFS_MB * 2))m tmpfs "$WORK/cache"
	cache_args=(-cache-dir "$WORK/cache" -cache-limit-mb "$CACHE_TMPFS_MB")
fi
"$BIN/gaia-backup" repo-create -repo "$WORK/repo" -config "$WORK/kopia.config" "${cache_args[@]}" -json | tee "$OUT_DIR/repo-create.json"

churn() { grep -a -o 'GAIA-CHURN bytes=[0-9]*' "$SERIAL" | tail -1 | cut -d= -f2; }

section "backups"
for ((n = 0; n <= INCREMENTALS; n++)); do
	if ((n > 0)); then
		log "guest writes for ${CHURN_S} s"
		sleep "$CHURN_S"
	fi
	virsh -q suspend "$DOM"
	guest_written=$(churn)
	t0=$(date +%s.%N)
	qemu-img convert -U -q -f qcow2 -O raw "$DISK" "$WORK/ref/p$n.raw"
	"$BIN/imgtool" hash -file "$WORK/ref/p$n.raw" >"$WORK/ref/p$n.json"
	ref_s=$(awk -v a="$t0" -v b="$(date +%s.%N)" 'BEGIN { printf "%.3f", b - a }')
	log "point $n: reference captured while paused (${ref_s} s), guest workload had written ${guest_written:-0} bytes"
	# gaia-backup starts the job with the guest paused, then resumes it; the guest writes during the backup.
	rc=0
	"$BIN/gaia-backup" backup -config "$WORK/kopia.config" -domain "$DOM" -disk vda -nbd tcp:127.0.0.1:10809 \
		-conns "$CONNS" -scratch-dir "$LV_IMAGES" -resume -json >"$OUT_DIR/backup-p$n.json" || rc=$?
	[[ "$(virsh -q domstate "$DOM")" == running ]] || virsh -q resume "$DOM" || true
	((rc == 0)) || { echo "UNKNOWN: backup of point $n failed (exit $rc)"; cat "$OUT_DIR/backup-p$n.json"; exit 3; }
	jq -c --argjson n "$n" --arg guest "${guest_written:-0}" --argjson ref_s "$ref_s" --slurpfile ref "$WORK/ref/p$n.json" \
		'{point: $n, kind, disk_bytes, changed_bytes, changed_extents, nbd_requested_bytes, nbd_payload_bytes,
		  nbd_read_commands, meta_context, guest_workload_bytes_total: ($guest | tonumber), reference_s: $ref_s,
		  reference_sha256: $ref[0].sha256, point_id: .point.id, checkpoint: .point.checkpoint,
		  parent_checkpoint: .point.parent_checkpoint, stats: .point.stats, timings_s}' "$OUT_DIR/backup-p$n.json" |
		tee -a "$OUT_DIR/points.jsonl"
done
virsh -q checkpoint-list "$DOM"
ls -la "$LV_IMAGES"
virsh -q destroy "$DOM"
du -sb "$WORK/repo" | tee "$OUT_DIR/repo-bytes.txt"

section "restores"
while read -r line; do
	n=$(jq -r .point <<<"$line")
	out="$WORK/restore/p$n.raw"
	rc=0
	"$BIN/gaia-backup" restore -config "$WORK/kopia.config" -point "$(jq -r .point_id <<<"$line")" -out "$out" \
		-workers "$WORKERS" -json >"$OUT_DIR/restore-p$n.json" </dev/null || rc=$?
	if ((rc != 0)); then
		jq -nc --argjson n "$n" '{point: $n, identical: null, error: "restore failed"}' | tee -a "$OUT_DIR/restores.jsonl"
		continue
	fi
	"$BIN/imgtool" hash -file "$out" >"$WORK/restore/p$n.json" </dev/null
	alloc=$(($(stat -c %b "$out") * $(stat -c %B "$out")))
	if [[ "$n" == "$INCREMENTALS" ]]; then # the latest point also as qcow2, compared by qemu-img
		qemu-img convert -q -f raw -O qcow2 "$out" "$WORK/restore/p$n.qcow2" </dev/null
		qemu-img compare -q -f raw -F qcow2 "$WORK/ref/p$n.raw" "$WORK/restore/p$n.qcow2" </dev/null && qcow2=true || qcow2=false
		rm -f "$WORK/restore/p$n.qcow2"
	else
		qcow2=null
	fi
	jq -c --argjson n "$n" --argjson alloc "$alloc" --argjson qcow2 "$qcow2" --slurpfile h "$WORK/restore/p$n.json" \
		--arg ref "$(jq -r .reference_sha256 <<<"$line")" \
		'{point: $n, restored_sha256: $h[0].sha256, reference_sha256: $ref, identical: ($h[0].sha256 == $ref),
		  qcow2_compare_identical: $qcow2, restored_allocated_bytes: $alloc, stats}' "$OUT_DIR/restore-p$n.json" |
		tee -a "$OUT_DIR/restores.jsonl"
	rm -f "$out"
done <"$OUT_DIR/points.jsonl"

# An independent look at the repository with the Kopia CLI: it opens what the library wrote.
section "kopia CLI cross-check"
KOPIA_PASSWORD="$GAIA_REPO_PASSWORD" kopia --config-file="$WORK/kopia.config" manifest list 2>&1 | grep -c 'type:gaia-disk-point' |
	sed 's/^/gaia-disk-point manifests: /' || echo "WARNING: kopia manifest list failed"
KOPIA_PASSWORD="$GAIA_REPO_PASSWORD" kopia --config-file="$WORK/kopia.config" content verify 2>&1 | tail -3 ||
	echo "WARNING: kopia content verify failed"

section "gate (CLAUDE.md T6)"
jq -n --slurpfile p "$OUT_DIR/points.jsonl" --slurpfile r "$OUT_DIR/restores.jsonl" --argjson planned "$((INCREMENTALS + 1))" '
	($p | map(select(.kind == "full")) | first) as $full
	| ($p | map(select(.kind == "incremental"))) as $inc
	| {
	  points_planned: $planned, points_backed_up: ($p | length), points_restored: ($r | map(select(.identical != null)) | length),
	  a_incremental_reads_only_dirty: {
	    rule: "per incremental: nbd_requested_bytes == changed_bytes (dirty bitmap), 0 < changed_bytes < the full point changed_bytes",
	    points: [$inc[] | {point, dirty_bitmap_bytes: .changed_bytes, nbd_requested_bytes, nbd_payload_bytes,
	             requested_vs_dirty: (if .changed_bytes > 0 then .nbd_requested_bytes / .changed_bytes else null end),
	             dirty_vs_full_data: (.changed_bytes / $full.changed_bytes),
	             guest_workload_bytes_since_parent: (.guest_workload_bytes_total - $p[.point - 1].guest_workload_bytes_total)}],
	    pass: ($inc | length > 0 and all(.nbd_requested_bytes == .changed_bytes and .changed_bytes > 0 and .changed_bytes < $full.changed_bytes))},
	  b_full_identical: ($r | map(select(.point == 0)) | first | .identical),
	  c_incrementals_identical: ($r | map(select(.point > 0)) | if length == ($planned - 1) and all(.identical == true) then true
	                             elif any(.identical == false) then false else null end),
	  restore_s: ($r | map({point, seconds: .stats.seconds}))
	}
	| . + {verdict: (if .points_backed_up < .points_planned or .points_restored < .points_planned
	                    or .b_full_identical == null or .c_incrementals_identical == null then "UNKNOWN"
	                 elif .a_incremental_reads_only_dirty.pass and .b_full_identical and .c_incrementals_identical then "OK"
	                 else "CRITICAL" end)}' | tee "$OUT_DIR/gate.json"

section "summary"
printf '%-6s %-12s %14s %14s %14s %10s %10s %s\n' point kind dirty_or_data nbd_requested guest_total backup_s restore_s identical
jq -r -s --slurpfile r "$OUT_DIR/restores.jsonl" '.[] as $p | ($r | map(select(.point == $p.point)) | first) as $x
	| [$p.point, $p.kind, $p.changed_bytes, $p.nbd_requested_bytes, $p.guest_workload_bytes_total,
	   ($p.timings_s.total * 1000 | round / 1000), (($x.stats.seconds // 0) * 1000 | round / 1000), ($x.identical // "n/a")] | @tsv' \
	"$OUT_DIR/points.jsonl" | awk -F'\t' '{ printf "%-6s %-12s %14s %14s %14s %10s %10s %s\n", $1, $2, $3, $4, $5, $6, $7, $8 }'

section "result"
verdict=$(jq -r .verdict "$OUT_DIR/gate.json")
echo "$verdict"
echo "(gate.json, points.jsonl, restores.jsonl, backup-*.json in $OUT_DIR)"
case $verdict in
OK) exit 0 ;;
CRITICAL) exit 2 ;;
*) exit 3 ;;
esac
