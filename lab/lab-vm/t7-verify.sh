#!/usr/bin/env bash
# Runs T7 on gaia-lab: the verification engine v0 (`gaia verify`, `gaia evidence verify`) against a
# restored image, end to end:
#   1. a Debian 12 guest (official cloud image, cloud-init) with SSH and an HTTP health endpoint runs
#      on an isolated libvirt network; gaia-backup takes a full backup of it while it runs, the guest is
#      shut down, and the point is restored from Kopia to a raw image (the restore time feeds Proven RTO);
#   2. gaia verify boots the restored image in its sandbox: copy-on-write overlay, an isolated bridge with
#      no uplink and no host address, probes and DHCP in a network namespace on that bridge;
#   3. the same for a copy with a damaged bootloader;
#   4. the ledger and bundles are checked, then checked again after changing one byte in each of
#      several places (copies).
# Gates (CLAUDE.md T7): (a) healthy image -> OK with a signed bundle; (b) damaged bootloader -> CRITICAL;
#   (c) one modified byte in a bundle or the ledger -> `gaia evidence verify` fails;
#   (d) the restored image's sha256 is unchanged after the tests.
#
# Usage (on gaia-lab, as root, after setup.sh):  lab/lab-vm/t7-verify.sh
#   systemd-run --unit=gaia-t7 --collect -E HOME=/root lab/lab-vm/t7-verify.sh
# Env: DISK_GB (8), BOOT_TIMEOUT_S (300 healthy, BROKEN_BOOT_TIMEOUT_S 180 damaged), OUT_DIR
#      (/root/t7-<UTC timestamp>, keeps the evidence, ledger and public key), KEEP=1 keeps the work dir,
#      ALLOW_TCG=1 (mock only: no /dev/kvm; on aarch64 the arm64 image with UEFI).
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

DISK_GB="${DISK_GB:-8}"
BOOT_TIMEOUT_S="${BOOT_TIMEOUT_S:-300}"
BROKEN_BOOT_TIMEOUT_S="${BROKEN_BOOT_TIMEOUT_S:-180}"
OUT_DIR="${OUT_DIR:-/root/t7-$(date -u +%Y%m%dT%H%M%SZ)}"
DOM=gaia-t7
MAC=52:54:00:47:a7:01
SRC_NET=gaia-t7-src
IMAGES=/var/lib/gaia/images
CLOUD_URL=https://cloud.debian.org/images/cloud/bookworm/latest
case $(uname -m) in
x86_64) CLOUD_IMG=debian-12-genericcloud-amd64.qcow2 TTY=ttyS0 ARCH=x86_64 FIRMWARE=bios ;;
aarch64) CLOUD_IMG=debian-12-genericcloud-arm64.qcow2 TTY=ttyAMA0 ARCH=aarch64 FIRMWARE=efi ;;
*) echo "UNKNOWN: unsupported architecture $(uname -m)"; exit 3 ;;
esac
WORK=/var/lib/gaia/t7
LV_IMAGES=/var/lib/libvirt/images
DISK="$LV_IMAGES/$DOM.qcow2"
SEED="$LV_IMAGES/$DOM-seed.iso"
RESTORED="$LV_IMAGES/$DOM-restored.raw"
BROKEN="$LV_IMAGES/$DOM-broken.raw"
SERIAL=/var/log/libvirt/qemu/$DOM-serial.log
EVIDENCE="$OUT_DIR/evidence"
LEDGER="$OUT_DIR/ledger.jsonl"
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in go jq make qemu-img virsh cloud-localds dnsmasq ip curl sha512sum fstrim; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done
mkdir -p "$OUT_DIR" "$EVIDENCE"
exec > >(tee "$OUT_DIR/t7.log") 2>&1
ln -sfn "$OUT_DIR" /root/t7-latest

section() { printf '\n== %s\n' "$*"; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

cleanup() {
	virsh -q destroy "$DOM" >/dev/null 2>&1 || true
	virsh -q undefine "$DOM" --checkpoints-metadata --nvram >/dev/null 2>&1 || virsh -q undefine "$DOM" --checkpoints-metadata >/dev/null 2>&1 || true
	virsh -q net-destroy "$SRC_NET" >/dev/null 2>&1 || true
	rm -f "$DISK" "$SEED" "$RESTORED" "$BROKEN" "$LV_IMAGES"/gaia-scratch-"$DOM"-* || true
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
go version -m "$BIN/gaia" | grep -E 'vcs\.(revision|modified)' || echo "no VCS stamp"

section "virtualisation"
uname -r
virt_type=kvm
if [[ ! -e /dev/kvm && "${ALLOW_TCG:-}" == 1 ]]; then
	virt_type=qemu
	echo "WARNING: no /dev/kvm, ALLOW_TCG=1: guests are emulated (mock run, not a T7 measurement)"
elif [[ ! -e /dev/kvm ]]; then
	echo "UNKNOWN: /dev/kvm is missing in gaia-lab (see T6: nested KVM)"
	exit 3
fi
virsh -q version
dnsmasq --version | head -1

section "free space"
mkdir -p "$IMAGES" "$WORK"
free_kib=$(df -Pk "$LV_IMAGES" | awk 'NR == 2 { print $4 }')
((free_kib >= 30 * 1048576)) || { echo "UNKNOWN: $((free_kib / 1048576)) GiB free on $LV_IMAGES, needs 30 GiB"; exit 3; }
df -h "$LV_IMAGES"

section "guest image"
if [[ ! -s "$IMAGES/$CLOUD_IMG" ]]; then
	curl -fsSL "$CLOUD_URL/$CLOUD_IMG" -o "$IMAGES/$CLOUD_IMG.part"
	mv "$IMAGES/$CLOUD_IMG.part" "$IMAGES/$CLOUD_IMG"
fi
curl -fsSL "$CLOUD_URL/SHA512SUMS" -o "$IMAGES/SHA512SUMS"
grep " $CLOUD_IMG\$" "$IMAGES/SHA512SUMS"
(cd "$IMAGES" && grep " $CLOUD_IMG\$" SHA512SUMS | sha512sum -c -) || { echo "UNKNOWN: guest image checksum mismatch"; rm -f "$IMAGES/$CLOUD_IMG"; exit 3; }

section "source guest (SSH + HTTP /health, network bound to its MAC)"
cleanup
mkdir -p "$WORK"
qemu-img convert -q -f qcow2 -O qcow2 "$IMAGES/$CLOUD_IMG" "$DISK"
qemu-img resize -q "$DISK" "${DISK_GB}G"
printf 'instance-id: %s-%s\nlocal-hostname: %s\n' "$DOM" "$(date +%s)" "$DOM" >"$WORK/meta-data"
cat >"$WORK/network-config" <<EOF
version: 2
ethernets:
  nic0:
    match:
      macaddress: "$MAC"
    set-name: eth0
    dhcp4: true
EOF
cat >"$WORK/user-data" <<'EOF'
#cloud-config
write_files:
  - path: /srv/gaia-health/health
    content: "ok\n"
  - path: /etc/systemd/system/gaia-health.service
    content: |
      [Unit]
      Description=Gaia T7 HTTP health endpoint
      After=network.target
      [Service]
      ExecStart=/usr/bin/python3 -m http.server 8080 --directory /srv/gaia-health
      Restart=always
      [Install]
      WantedBy=multi-user.target
runcmd:
  - [systemctl, daemon-reload]
  - [systemctl, enable, --now, gaia-health.service]
  - [sh, -c, "until python3 -c 'import urllib.request; urllib.request.urlopen(\"http://127.0.0.1:8080/health\")' 2>/dev/null; do sleep 1; done; sync; echo GAIA-READY > /dev/@TTY@"]
EOF
sed -i "s/@TTY@/$TTY/g" "$WORK/user-data"
cloud-localds -N "$WORK/network-config" "$SEED" "$WORK/user-data" "$WORK/meta-data"
cat >"$WORK/$SRC_NET.xml" <<EOF
<network><name>$SRC_NET</name><bridge name='gt7src' stp='off' delay='0'/>
  <ip address='10.253.0.1' netmask='255.255.255.0'><dhcp><range start='10.253.0.10' end='10.253.0.99'/></dhcp></ip>
</network>
EOF
virsh -q net-create "$WORK/$SRC_NET.xml" >/dev/null
if [[ $ARCH == x86_64 ]]; then
	os="<os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><features><acpi/><apic/></features>"
	emulator=/usr/bin/qemu-system-x86_64
else
	os="<os firmware='efi'><type arch='aarch64' machine='virt'>hvm</type><boot dev='hd'/></os><features><acpi/><gic version='3'/></features>"
	emulator=/usr/bin/qemu-system-aarch64
fi
cpu="<cpu mode='host-passthrough'/>"
[[ $virt_type == kvm ]] || cpu="<cpu mode='custom'><model>max</model></cpu>"
rm -f "$SERIAL"
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
    <interface type='network'><source network='$SRC_NET'/><mac address='$MAC'/><model type='virtio'/><rom enabled='no'/></interface>
    <serial type='file'><source path='$SERIAL'/><target port='0'/></serial>
  </devices>
</domain>
EOF
virsh -q define "$WORK/$DOM.xml" >/dev/null
virsh -q start "$DOM"
log "source guest started, waiting for GAIA-READY (HTTP /health answers inside the guest)"
for ((i = 0; ; i++)); do
	grep -q GAIA-READY "$SERIAL" 2>/dev/null && break
	((i < 240)) || { echo "UNKNOWN: no GAIA-READY after 1200 s"; tail -30 "$SERIAL" || true; exit 3; }
	sleep 5
done
log "source guest ready"

section "backup (running guest, crash-consistent) and restore from Kopia"
GAIA_REPO_PASSWORD=$(head -c 24 /dev/urandom | base64)
export GAIA_REPO_PASSWORD
"$BIN/gaia-backup" repo-create -repo "$WORK/repo" -config "$WORK/kopia.config" -json >/dev/null
"$BIN/gaia-backup" backup -config "$WORK/kopia.config" -domain "$DOM" -disk vda -full -scratch-dir "$LV_IMAGES" -json >"$OUT_DIR/backup.json"
jq -c '{verdict, kind, disk_bytes, changed_bytes, point: .point.id, timings_s}' "$OUT_DIR/backup.json"
virsh -q shutdown "$DOM" >/dev/null
for ((i = 0; i < 60; i++)); do [[ "$(virsh -q domstate "$DOM")" == "shut off" ]] && break; sleep 2; done
virsh -q destroy "$DOM" >/dev/null 2>&1 || true
virsh -q net-destroy "$SRC_NET" >/dev/null
"$BIN/gaia-backup" restore -config "$WORK/kopia.config" -domain "$DOM" -disk vda -out "$RESTORED" -workers 4 -json >"$OUT_DIR/restore.json"
jq -c '{verdict, stats}' "$OUT_DIR/restore.json"
chmod 0644 "$RESTORED"
"$BIN/imgtool" hash -file "$RESTORED" | tee "$OUT_DIR/restored-before.json"

section "keys"
"$BIN/gaia" evidence keygen -key "$WORK/evidence-key.pem" -pub "$OUT_DIR/evidence-pub.pem" -json | jq -c '{fingerprint}'

config() { # config TIMEOUT -> the VM's verification config
	jq -n --arg mac "$MAC" --arg arch "$ARCH" --arg fw "$FIRMWARE" --argjson boot "$1" '{
		vm: "gaia-t7", template: "linux-basic",
		machine: {arch: $arch, firmware: $fw, memory_mib: 1024, vcpus: 2, disk_bus: "virtio", nic_model: "virtio", mac: $mac},
		boot_timeout_s: $boot, probe_timeout_s: 120, shutdown_timeout_s: 60,
		checks: [{name: "http-health", type: "http", port: 8080, path: "/health", expect_status: 200, expect_body: "ok"}]}'
}
config "$BOOT_TIMEOUT_S" >"$OUT_DIR/verify-healthy.json"
config "$BROKEN_BOOT_TIMEOUT_S" >"$OUT_DIR/verify-broken.json"

section "gaia verify: restored image"
rc=0
"$BIN/gaia" verify -config "$OUT_DIR/verify-healthy.json" -image "$RESTORED" -restore-json "$OUT_DIR/restore.json" \
	-key "$WORK/evidence-key.pem" -evidence-dir "$EVIDENCE" -ledger "$LEDGER" -json >"$OUT_DIR/verify-healthy-result.json" || rc=$?
echo "exit $rc" | tee "$OUT_DIR/verify-healthy-exit.txt"
jq -c '{verdict, bundle_id, checks: [.checks[] | {name, verdict, seconds, detail}], timings_s, shutdown, screenshots, errors,
	sandbox: (.sandbox | {virt, guest_ip, host_addresses_on_bridge, netns_routes, isolated})}' "$OUT_DIR/verify-healthy-result.json"

section "gaia verify: damaged bootloader"
cp --sparse=always "$RESTORED" "$BROKEN"
if [[ $FIRMWARE == bios ]]; then
	# The MBR's boot code (first 440 bytes); the partition table stays.
	dd if=/dev/zero of="$BROKEN" bs=440 count=1 conv=notrunc status=none
	damage="zeroed the MBR boot code (bytes 0-439)"
else
	esp=$(sfdisk -J "$BROKEN" | jq -r '.partitiontable.partitions[] | select(.type | ascii_upcase == "C12A7328-F81F-11D2-BA4B-00A0C93EC93B") | .start')
	dd if=/dev/zero of="$BROKEN" bs=512 seek="$esp" count=16384 conv=notrunc status=none
	damage="zeroed the first 8 MiB of the EFI system partition (sector $esp)"
fi
chmod 0644 "$BROKEN"
echo "damage: $damage" | tee "$OUT_DIR/damage.txt"
rc=0
"$BIN/gaia" verify -config "$OUT_DIR/verify-broken.json" -image "$BROKEN" -key "$WORK/evidence-key.pem" \
	-evidence-dir "$EVIDENCE" -ledger "$LEDGER" -json >"$OUT_DIR/verify-broken-result.json" || rc=$?
echo "exit $rc" | tee "$OUT_DIR/verify-broken-exit.txt"
jq -c '{verdict, bundle_id, checks: [.checks[] | {name, verdict, seconds, detail}], timings_s, shutdown, screenshots, errors}' "$OUT_DIR/verify-broken-result.json"

section "restored image after both runs"
"$BIN/imgtool" hash -file "$RESTORED" | tee "$OUT_DIR/restored-after.json"

section "gaia evidence verify: as written"
rc=0
"$BIN/gaia" evidence verify -pubkey "$OUT_DIR/evidence-pub.pem" -ledger "$LEDGER" -evidence-dir "$EVIDENCE" -json >"$OUT_DIR/evidence-verify.json" || rc=$?
echo "exit $rc"
jq -c '{verdict, entries: .report.entries, bundles: [.report.bundles[] | {bundle_id, files, problems}], problems: .report.problems}' "$OUT_DIR/evidence-verify.json"
echo "$rc" >"$WORK/untampered.rc"

section "gaia evidence verify: one byte changed (on copies)"
healthy_id=$(jq -r .bundle_id "$OUT_DIR/verify-healthy-result.json")
broken_id=$(jq -r .bundle_id "$OUT_DIR/verify-broken-result.json")
flip() { # flip FILE OFFSET: XOR one byte with 0x01
	local b
	b=$(od -An -tu1 -j "$2" -N1 "$1" | tr -d ' ')
	printf '%b' "\\0$(printf '%03o' $((b ^ 1)))" | dd of="$1" bs=1 seek="$2" conv=notrunc status=none
}
: >"$OUT_DIR/tamper.jsonl"
tamper() { # tamper NAME RELPATH (relative to a copy of OUT_DIR's evidence+ledger) OFFSET
	local name=$1 rel=$2 off=$3 t="$WORK/tamper-$1" rc=0
	if [[ ! -f "$OUT_DIR/$rel" || ! "$off" =~ ^[0-9]+$ ]]; then # nothing to change: not checked, so not a pass
		jq -nc --arg name "$name" --arg file "$rel" '{case: $name, file: $file, offset: null, exit: null, verdict: "not_run", problems: []}' |
			tee -a "$OUT_DIR/tamper.jsonl"
		return
	fi
	rm -rf "$t" && mkdir -p "$t" && cp -a "$EVIDENCE" "$LEDGER" "$t/"
	flip "$t/$rel" "$off"
	"$BIN/gaia" evidence verify -pubkey "$OUT_DIR/evidence-pub.pem" -ledger "$t/ledger.jsonl" -evidence-dir "$t/evidence" -json >"$t/out.json" || rc=$?
	jq -c --arg name "$name" --arg file "$rel" --argjson off "$off" --argjson rc "$rc" \
		'{case: $name, file: $file, offset: $off, exit: $rc, verdict,
		  problems: ([.report.problems[]?] + [.report.bundles[]? | .problems[] ]) }' "$t/out.json" | tee -a "$OUT_DIR/tamper.jsonl"
}
mid() { [[ -f "$1" ]] && echo $(($(stat -c %s "$1") / 2)) || true; }
# offset_of FILE STRING [LINE]: byte offset just after the first STRING (in line LINE, counted from the file start)
offset_of() {
	local file=$1 str=$2 line=${3:-} before=0 at
	[[ -f "$file" ]] || return 0
	if [[ -n "$line" ]]; then
		before=$(head -n $((line - 1)) "$file" | wc -c)
		at=$(sed -n "${line}p" "$file" | grep -b -o -F "$str" | head -1 | cut -d: -f1 || true)
	else
		at=$(grep -b -o -F "$str" "$file" | head -1 | cut -d: -f1 || true)
	fi
	[[ -n "$at" ]] && echo $((before + at + ${#str}))
	return 0
}
tamper screenshot "evidence/$healthy_id/screenshot-boot.png" "$(mid "$EVIDENCE/$healthy_id/screenshot-boot.png")"
tamper result-verdict "evidence/$broken_id/result.json" "$(offset_of "$EVIDENCE/$broken_id/result.json" '"verdict": "')"
tamper serial-log "evidence/$healthy_id/serial.log" "$(mid "$EVIDENCE/$healthy_id/serial.log")"
tamper manifest "evidence/$healthy_id/manifest.json" "$(mid "$EVIDENCE/$healthy_id/manifest.json")"
tamper signature "evidence/$broken_id/manifest.sig" 10
tamper ledger-verdict ledger.jsonl "$(offset_of "$LEDGER" '"verdict":"' 1)"
tamper ledger-prev ledger.jsonl "$(offset_of "$LEDGER" '"prev":"' 2)"

section "gate (CLAUDE.md T7)"
jq -n --slurpfile h "$OUT_DIR/verify-healthy-result.json" --slurpfile b "$OUT_DIR/verify-broken-result.json" \
	--slurpfile ev "$OUT_DIR/evidence-verify.json" --slurpfile t "$OUT_DIR/tamper.jsonl" \
	--slurpfile before "$OUT_DIR/restored-before.json" --slurpfile after "$OUT_DIR/restored-after.json" \
	--argjson untampered_rc "$(cat "$WORK/untampered.rc")" '
	($h[0]) as $h | ($b[0]) as $b | {
	  a_healthy_ok: {verdict: $h.verdict, bundle: $h.bundle_id, ledger_seq: $h.ledger_seq, evidence_verify: $ev[0].verdict,
	                 pass: ($h.verdict == "OK" and $ev[0].verdict == "OK" and $untampered_rc == 0)},
	  b_broken_critical: {verdict: $b.verdict, boot: ($b.checks[] | select(.type == "boot_serial") | .verdict),
	                      pass: ($b.verdict == "CRITICAL")},
	  c_tamper_detected: {cases: ($t | length), detected: ($t | map(select(.exit == 2 and .verdict == "CRITICAL")) | length),
	                      not_run: ($t | map(select(.verdict == "not_run")) | length), untampered_exit: $untampered_rc,
	                      pass: (($t | length) >= 1 and all($t[]; .exit == 2 and .verdict == "CRITICAL") and $untampered_rc == 0)},
	  d_image_unchanged: {sha256_before: $before[0].sha256, sha256_after: $after[0].sha256,
	                      verify_reported: [$h.image.unchanged, $h.image.sha256_before == $before[0].sha256],
	                      pass: ($before[0].sha256 == $after[0].sha256 and $h.image.unchanged == true and $h.image.sha256_before == $before[0].sha256)},
	  proven_rto_s: $h.timings_s.proven_rto, restore_s: $h.timings_s.restore, boot_s: $h.timings_s.boot, ready_s: $h.timings_s.ready
	} | . + {verdict: (if .a_healthy_ok.pass and .b_broken_critical.pass and .c_tamper_detected.pass and .d_image_unchanged.pass then "OK"
	                   elif .a_healthy_ok.verdict == "UNKNOWN" or .b_broken_critical.verdict == "UNKNOWN" or .c_tamper_detected.not_run > 0 then "UNKNOWN"
	                   else "CRITICAL" end)}' | tee "$OUT_DIR/gate.json"

section "evidence kept"
find "$EVIDENCE" -type f -printf '%P %s\n' | sort
cp "$OUT_DIR/evidence-pub.pem" "$EVIDENCE/../" 2>/dev/null || true

section "result"
verdict=$(jq -r .verdict "$OUT_DIR/gate.json")
echo "$verdict"
echo "(gate.json, evidence/, ledger.jsonl, evidence-pub.pem in $OUT_DIR)"
case $verdict in
OK) exit 0 ;;
CRITICAL) exit 2 ;;
*) exit 3 ;;
esac
