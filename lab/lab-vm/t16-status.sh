#!/usr/bin/env bash
# Runs T16 on gaia-lab: scheduled restore tests and the status model on one VM, as a timeline (ADR-007 "The status
# model"). gaia server and its sandbox helper run as in docs/install.md (packaged units, the server without any
# privilege, the helper as root); the KVM host is gaia-lab itself, reached over SSH as in T15.
#
# The guest is Debian 12's cloud image with two disks: vda (system) and vdb (data, mounted by /etc/fstab, so a
# restore test that leaves a disk out does not reach a login prompt). It keeps writing a little. A second serial
# port is a unix socket on the host: a small agent in the guest answers BREAK (zero the MBR boot code of vda, as a
# broken boot loader would) and FIX (put it back).
#
# Timeline (gate b): the VM is added -> UNKNOWN until its first passing test -> OK; BREAK, the next backup and test
# -> CRITICAL with the reason; FIX, the next backup and test -> OK; the policy stops testing (test_interval_s 1 day)
# -> WARNING once the proof is older than max_proof_age_s. Every status change comes from state_changes with its time.
# Gate (a), the truth table, is the unit test run at the start.
#
# Usage (on gaia-lab, as root, after setup.sh):
#   systemd-run --unit=gaia-t16 --collect -E HOME=/root lab/lab-vm/t16-status.sh
# Env: INTERVAL_S (300: backup and test interval), RPO_S (900), RTO_S (600), MAX_PROOF_S (900), DISK_GB (4),
#      DATA_GB (1), PHASE_TIMEOUT_S (1800), HOST_ADDR, OUT_DIR (/root/t16-<UTC timestamp>), KEEP=1 keeps everything.
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

INTERVAL_S="${INTERVAL_S:-300}"
RPO_S="${RPO_S:-900}"
RTO_S="${RTO_S:-600}"
MAX_PROOF_S="${MAX_PROOF_S:-900}"
DISK_GB="${DISK_GB:-4}"
DATA_GB="${DATA_GB:-1}"
PHASE_TIMEOUT_S="${PHASE_TIMEOUT_S:-1800}"
HOST_ADDR="${HOST_ADDR:-$(hostname -I | awk '{ print $1 }')}"
OUT_DIR="${OUT_DIR:-/root/t16-$(date -u +%Y%m%dT%H%M%SZ)}"
DOM=gaia-t16
IMAGES=/var/lib/gaia/images
CLOUD_URL=https://cloud.debian.org/images/cloud/bookworm/latest
CLOUD_IMG=debian-12-genericcloud-amd64.qcow2
LV_IMAGES=/var/lib/libvirt/images
VDA="$LV_IMAGES/$DOM-vda.qcow2"
VDB="$LV_IMAGES/$DOM-vdb.qcow2"
SEED="$LV_IMAGES/$DOM-seed.iso"
SERIAL=/var/log/libvirt/qemu/$DOM-serial.log
WORK=/var/lib/gaia/t16
CTL_SOCK=$WORK/ctl.sock
SRV_USER=gaia-t16
SRV_HOME=/var/lib/gaia-t16
SRV_BIN=/usr/local/sbin/gaia-t16
UNIT=gaia-t16-server
SB_UNIT=gaia-t16-sandbox
SB_HOME=/var/lib/gaia-t16-sandbox
SB_SOCK=/run/gaia-t16-sandbox/sandbox.sock
SB_WORK=$LV_IMAGES/gaia-t16-sandbox
HOST_USER=gaia-backup
API=https://127.0.0.1:8443
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
[[ $(uname -m) == x86_64 ]] || { echo "UNKNOWN: T16 runs on gaia-lab (x86_64)"; exit 3; }
for bin in go jq make qemu-img virsh cloud-localds curl sha512sum fstrim ssh-keygen dnsmasq python3; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done
mkdir -p "$OUT_DIR"
exec > >(tee "$OUT_DIR/t16.log") 2>&1
ln -sfn "$OUT_DIR" /root/t16-latest

section() { printf '\n== %s\n' "$*"; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
gaia() { "$BIN/gaia" "$@"; }
api() { gaia "$@" -json; }

cleanup() {
	[[ "${KEEP:-}" == 1 ]] && return 0
	virsh -q destroy "$DOM" >/dev/null 2>&1 || true
	virsh -q undefine "$DOM" --checkpoints-metadata >/dev/null 2>&1 || true
	rm -f "$VDA" "$VDB" "$SEED" "$LV_IMAGES"/gaia-scratch-"$DOM"-* 2>/dev/null || true
	systemctl stop "$UNIT" "$SB_UNIT" >/dev/null 2>&1 || true
	rm -f "/run/systemd/system/$UNIT.service" "/run/systemd/system/$SB_UNIT.service"
	systemctl daemon-reload
	rm -rf "$SRV_HOME" "$SB_HOME" "$SB_WORK" "$WORK" /root/.config/gaia "$(dirname "$SB_SOCK")"
	userdel "$SRV_USER" >/dev/null 2>&1 || true
	pkill -u "$HOST_USER" >/dev/null 2>&1 || true
	for _ in 1 2 3 4 5; do
		userdel -r "$HOST_USER" >/dev/null 2>&1 && break
		id "$HOST_USER" >/dev/null 2>&1 || break
		sleep 1
	done
}
# shellcheck disable=SC2329 # invoked by the trap
on_exit() {
	if [[ -n "${SRV_STARTED:-}" ]]; then
		journalctl -u "$UNIT" --no-pager -o short-iso-precise >"$OUT_DIR/server.journal" 2>&1 || true
		journalctl -u "$SB_UNIT" --no-pager -o short-iso-precise >"$OUT_DIR/sandbox.journal" 2>&1 || true
	fi
	cleanup
	sync
	fstrim -a >/dev/null 2>&1 || true
}
trap on_exit EXIT

section "date / commit"
date -u +%Y-%m-%dT%H:%M:%SZ
git -C "$repo_root" rev-parse HEAD 2>/dev/null || echo "not a git checkout"
echo "output: $OUT_DIR"
echo "backup and test every $INTERVAL_S s; RPO target $RPO_S s, RTO target $RTO_S s, maximum proof age $MAX_PROOF_S s"

section "build"
owner=$(stat -c %U "$repo_root")
asowner() {
	if [[ "$owner" == root ]]; then "$@"; else runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" "$@"; fi
}
asowner make -C "$repo_root" -s build
go version -m "$BIN/gaia" | grep -E 'vcs\.(revision|modified)' || echo "no VCS stamp"
virsh -q version
[[ -e /dev/kvm ]] || { echo "UNKNOWN: /dev/kvm is missing in gaia-lab"; exit 3; }

# Gate (a): the truth table, every consistent combination of the status inputs.
section "gate (a): status truth table"
(cd "$repo_root" && asowner go test -count=1 -v -run 'TestTruthTable' ./internal/status) | tee "$OUT_DIR/truth-table.txt" | grep -E '^(---|ok|FAIL)|combinations'

section "free space"
cleanup
mkdir -p "$IMAGES" "$WORK"
need_gib=$((3 * (DISK_GB + DATA_GB) + 10))
free_kib=$(df -Pk "$WORK" | awk 'NR == 2 { print $4 }')
((free_kib >= need_gib * 1048576)) || { echo "UNKNOWN: $((free_kib / 1048576)) GiB free on $WORK, needs $need_gib GiB"; exit 3; }
df -h "$WORK" "$LV_IMAGES"

section "guest image"
if [[ ! -s "$IMAGES/$CLOUD_IMG" ]]; then
	curl -fsSL "$CLOUD_URL/$CLOUD_IMG" -o "$IMAGES/$CLOUD_IMG.part"
	mv "$IMAGES/$CLOUD_IMG.part" "$IMAGES/$CLOUD_IMG"
fi
curl -fsSL "$CLOUD_URL/SHA512SUMS" -o "$IMAGES/SHA512SUMS"
(cd "$IMAGES" && grep " $CLOUD_IMG\$" SHA512SUMS | sha512sum -c -) || { echo "UNKNOWN: guest image checksum mismatch"; rm -f "$IMAGES/$CLOUD_IMG"; exit 3; }

# --- gaia server and the sandbox helper, from the packaged units ------------------------------------------------
section "gaia server and sandbox helper"
useradd --system --home-dir "$SRV_HOME" --shell /usr/sbin/nologin "$SRV_USER"
install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME" "$SRV_HOME/data" "$SRV_HOME/repo"
install -m 0755 "$BIN/gaia" "$SRV_BIN"
sed -e "s|^ExecStart=.*|ExecStart=$SRV_BIN sandbox serve -group $SRV_USER -socket $SB_SOCK -images $SB_HOME/images -work-dir $SB_WORK -bundle-dir $SB_HOME/bundles|" \
	"$repo_root/packaging/systemd/gaia-sandbox.service" >"/run/systemd/system/$SB_UNIT.service"
diff "$repo_root/packaging/systemd/gaia-sandbox.service" "/run/systemd/system/$SB_UNIT.service" || true
pwfile="$SRV_HOME/admin.pw"
head -c 24 /dev/urandom | base64 | install -m 0600 -o "$SRV_USER" -g "$SRV_USER" /dev/stdin "$pwfile"
runuser -u "$SRV_USER" -- "$SRV_BIN" server init -config "$SRV_HOME/server.json" -data-dir "$SRV_HOME/data" \
	-listen 127.0.0.1:8443 -hostname 127.0.0.1 -admin admin -password-file "$pwfile" -json >"$OUT_DIR/server-init.json"
cert=$(jq -r .result.tls_certificate_sha256 "$OUT_DIR/server-init.json")
jq --arg s "$SB_SOCK" --arg i "$SB_HOME/images" '. + {sandbox_socket: $s, sandbox_images_dir: $i}' "$SRV_HOME/server.json" >"$WORK/server.json"
install -m 0640 -o "$SRV_USER" -g "$SRV_USER" "$WORK/server.json" "$SRV_HOME/server.json"
jq . "$SRV_HOME/server.json" | tee "$OUT_DIR/server.json"
sed -e "s|^User=.*|User=$SRV_USER|" -e "s|^Group=.*|Group=$SRV_USER|" \
	-e "s|^ExecStart=.*|ExecStart=$SRV_BIN server run -config $SRV_HOME/server.json|" \
	-e "s|^ReadWritePaths=.*|ReadWritePaths=$SRV_HOME -$SB_HOME/images|" \
	-e "s|gaia-sandbox.service|$SB_UNIT.service|" \
	"$repo_root/packaging/systemd/gaia.service" >"/run/systemd/system/$UNIT.service"
diff "$repo_root/packaging/systemd/gaia.service" "/run/systemd/system/$UNIT.service" || true
systemctl daemon-reload
systemctl start "$SB_UNIT"
for ((i = 0; i < 30; i++)); do [[ -S $SB_SOCK ]] && break; sleep 1; done
ls -la "$(dirname "$SB_SOCK")" "$SB_HOME"
systemctl start "$UNIT"
SRV_STARTED=1
for ((i = 0; i < 30; i++)); do curl -fsk "$API/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
api login -server "$API" -user admin -fingerprint "$cert" -password-file "$pwfile" >/dev/null
rm -f "$pwfile"
api system >"$OUT_DIR/system.json"
jq -c '{version, schema_version}' "$OUT_DIR/system.json"

section "host: $HOST_USER over SSH (as T15)"
useradd -m -s /usr/sbin/nologin -G libvirt,libvirt-qemu "$HOST_USER"
install -d -m 700 -o "$HOST_USER" -g "$HOST_USER" "/home/$HOST_USER/.ssh"
jq -r .ssh_authorized_keys_line "$OUT_DIR/system.json" >"/home/$HOST_USER/.ssh/authorized_keys"
chown "$HOST_USER:" "/home/$HOST_USER/.ssh/authorized_keys"
chmod 600 "/home/$HOST_USER/.ssh/authorized_keys"
host_fp=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $2 }')
host_id=$(api hosts add name=gaia-lab address="$HOST_ADDR" ssh_user="$HOST_USER" | jq .id)
[[ $(api hosts scan "$host_id" | jq -r .fingerprint) == "$host_fp" ]] || { echo "UNKNOWN: scanned fingerprint differs"; exit 3; }
api hosts trust "$host_id" fingerprint="$host_fp" >/dev/null
api hosts check "$host_id" | jq -c .

# --- the guest ---------------------------------------------------------------------------------------------------
section "guest ($DOM: vda ${DISK_GB} GiB, vdb ${DATA_GB} GiB on /data)"
qemu-img convert -q -f qcow2 -O qcow2 "$IMAGES/$CLOUD_IMG" "$VDA"
qemu-img resize -q "$VDA" "${DISK_GB}G"
qemu-img create -q -f qcow2 "$VDB" "${DATA_GB}G"
chown libvirt-qemu:libvirt-qemu "$VDA" "$VDB"
cat >"$WORK/meta-data" <<EOF
instance-id: $DOM-$(date +%s)
local-hostname: $DOM
EOF
cat >"$WORK/user-data" <<'EOF'
#cloud-config
write_files:
  - path: /usr/local/sbin/gaia-setup
    permissions: '0755'
    content: |
      #!/bin/bash
      set -eu
      blkid /dev/vdb >/dev/null 2>&1 || mkfs.ext4 -q -L gaia-data /dev/vdb
      grep -q gaia-data /etc/fstab || echo 'LABEL=gaia-data /data ext4 defaults 0 2' >> /etc/fstab
      mkdir -p /data && mount -a
      dd if=/dev/vda of=/root/mbr-boot-code.bak bs=440 count=1 status=none
  - path: /usr/local/sbin/gaia-churn
    permissions: '0755'
    content: |
      #!/bin/bash
      set -eu
      f=/var/lib/gaia-churn/data
      mkdir -p /var/lib/gaia-churn
      [ -s "$f" ] || dd if=/dev/urandom of="$f" bs=1M count=128 conv=fsync status=none
      while true; do
        dd if=/dev/urandom of="$f" bs=64K count=1 seek=$(( RANDOM % 2048 )) conv=notrunc,fsync status=none
        head -c 65536 /dev/urandom >> /data/app.log && sync /data/app.log
        [ "$(stat -c %s /data/app.log)" -lt 268435456 ] || mv /data/app.log /data/app.log.1
        sleep 2
      done
  - path: /usr/local/sbin/gaia-ctl
    permissions: '0755'
    content: |
      #!/bin/bash
      # Test fixture: BREAK zeroes the MBR boot code of vda (the guest no longer boots), FIX puts it back.
      set -u
      stty -F /dev/ttyS1 raw -echo 115200
      exec 3<>/dev/ttyS1
      echo "READY" >&3
      while read -r cmd <&3; do
        case "$cmd" in
        BREAK) dd if=/dev/zero of=/dev/vda bs=440 count=1 conv=notrunc,fsync status=none && sync && echo "ACK BREAK" >&3 ;;
        FIX) dd if=/root/mbr-boot-code.bak of=/dev/vda bs=440 count=1 conv=notrunc,fsync status=none && sync && echo "ACK FIX" >&3 ;;
        *) echo "NAK $cmd" >&3 ;;
        esac
      done
  - path: /etc/systemd/system/gaia-churn.service
    content: |
      [Unit]
      Description=Gaia T16 test workload
      RequiresMountsFor=/data
      [Service]
      ExecStart=/usr/local/sbin/gaia-churn
      [Install]
      WantedBy=multi-user.target
  - path: /etc/systemd/system/gaia-ctl.service
    content: |
      [Unit]
      Description=Gaia T16 test fixture control on ttyS1
      [Service]
      ExecStart=/usr/local/sbin/gaia-ctl
      Restart=always
      [Install]
      WantedBy=multi-user.target
runcmd:
  - [/usr/local/sbin/gaia-setup]
  - [systemctl, daemon-reload]
  - [systemctl, enable, --now, gaia-churn.service, gaia-ctl.service]
  - [sh, -c, 'echo GAIA-READY > /dev/ttyS0']
EOF
cloud-localds "$SEED" "$WORK/user-data" "$WORK/meta-data"
rm -f "$SERIAL" "$CTL_SOCK"
# No network interface; the seed is a read-only CD-ROM (left out of backups); serial 1 is the fixture's control line.
cat >"$WORK/$DOM.xml" <<EOF
<domain type='kvm'>
  <name>$DOM</name>
  <memory unit='MiB'>1024</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><features><acpi/><apic/></features>
  <cpu mode='host-passthrough'/>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2' discard='unmap'/><source file='$VDA'/><target dev='vda' bus='virtio'/></disk>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2' discard='unmap'/><source file='$VDB'/><target dev='vdb' bus='virtio'/></disk>
    <disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='$SEED'/><target dev='sda' bus='sata'/><readonly/></disk>
    <serial type='file'><source path='$SERIAL'/><target port='0'/></serial>
    <serial type='unix'><source mode='bind' path='$CTL_SOCK'/><target port='1'/></serial>
  </devices>
</domain>
EOF
virsh -q define "$WORK/$DOM.xml" >/dev/null
virsh -q start "$DOM"
log "guest started, waiting for GAIA-READY"
for ((i = 0; ; i++)); do
	grep -q GAIA-READY "$SERIAL" 2>/dev/null && break
	((i < 240)) || { echo "UNKNOWN: no GAIA-READY after 1200 s"; tail -30 "$SERIAL" || true; exit 3; }
	sleep 5
done
log "guest ready"

# ctl CMD: send a command to the guest's fixture agent and wait for its ACK.
ctl() {
	python3 - "$CTL_SOCK" "$1" <<'PY'
import socket, sys, time
s = socket.socket(socket.AF_UNIX)
s.connect(sys.argv[1])
s.settimeout(60)
s.sendall((sys.argv[2] + "\n").encode())
buf, end = b"", time.time() + 60
while time.time() < end:
    buf += s.recv(256)
    if ("ACK " + sys.argv[2]).encode() in buf:
        print("guest:", "ACK", sys.argv[2])
        sys.exit(0)
print("guest did not acknowledge:", buf, file=sys.stderr)
sys.exit(1)
PY
}

# --- policy and VM; the timeline -----------------------------------------------------------------------------------
section "repository, policy, VM"
repo_id=$(api repositories add name=main location="$SRV_HOME/repo" | jq .id)
api policies add name=t16 repository_id="$repo_id" backup_interval_s="$INTERVAL_S" test_interval_s="$INTERVAL_S" keep_last=8 \
	keep_daily=0 keep_weekly=0 rpo_target_s="$RPO_S" rto_target_s="$RTO_S" max_proof_age_s="$MAX_PROOF_S" check_template=linux-boot |
	tee "$OUT_DIR/policy.json" | jq -c .
policy_id=$(jq .id "$OUT_DIR/policy.json")

: >"$OUT_DIR/timeline.jsonl"
: >"$OUT_DIR/phases.jsonl"
last_seen=""
# status_now: the VM's status as gaia status prints it (exit code = the status, so never fatal here).
status_now() { gaia status "$vm_id" -json 2>/dev/null || true; }
# wait_status TARGET PHASE: polls the status every 10 s, logging every change, until it is TARGET.
wait_status() {
	local target=$1 phase=$2 t0 s st
	t0=$(date +%s)
	while (($(date +%s) - t0 < PHASE_TIMEOUT_S)); do
		s=$(status_now)
		st=$(jq -r .status <<<"$s")
		if [[ "$st" != "$last_seen" ]]; then
			log "status $st: $(jq -r '.reasons[0].text' <<<"$s")"
			jq -c --arg phase "$phase" '{seen_at: (now | todate), phase: $phase, status, changed_at, reason: .reasons[0].text,
				theoretical_rpo_s, proven_rpo_s, proven_rto_s, proof_age_s}' <<<"$s" >>"$OUT_DIR/timeline.jsonl"
			last_seen=$st
		fi
		if [[ "$st" == "$target" ]]; then
			jq -nc --arg phase "$phase" --arg target "$target" --argjson s "$(($(date +%s) - t0))" \
				'{phase: $phase, reached: $target, after_s: $s, at: (now | todate)}' | tee -a "$OUT_DIR/phases.jsonl"
			return 0
		fi
		sleep 10
	done
	echo "UNKNOWN: phase $phase: status $last_seen, not $target, after $PHASE_TIMEOUT_S s"
	exit 3
}

section "phase 1: a newly added VM is UNKNOWN until its first passing test"
api vms add host_id="$host_id" name="$DOM" policy_id="$policy_id" | tee "$OUT_DIR/vm.json" | jq -c .
vm_id=$(jq .id "$OUT_DIR/vm.json")
wait_status UNKNOWN added
wait_status OK healthy

section "phase 2: the guest's boot loader is broken -> the next backup and test are CRITICAL"
ctl BREAK
jq -nc '{injected: "BREAK", at: (now | todate)}' >>"$OUT_DIR/phases.jsonl"
wait_status CRITICAL broken

section "phase 3: repaired -> OK again"
ctl FIX
jq -nc '{injected: "FIX", at: (now | todate)}' >>"$OUT_DIR/phases.jsonl"
wait_status OK repaired

section "phase 4: no more restore tests -> WARNING once the proof is older than $MAX_PROOF_S s"
api policies set "$policy_id" test_interval_s=86400 | jq -c '{id, test_interval_s}'
jq -nc '{injected: "test_interval_s 86400", at: (now | todate)}' >>"$OUT_DIR/phases.jsonl"
wait_status WARNING stale-proof
status_now | tee "$OUT_DIR/status-end.json" | jq -c '{status, reasons: [.reasons[].rule], notes}'
gaia status || true

# --- what the server recorded ------------------------------------------------------------------------------------
section "records"
api state-changes list entity=vm_status entity_id="$vm_id" >"$OUT_DIR/status-history.json"
jq -r '.[] | [.at, .from, .to, .reason] | @tsv' "$OUT_DIR/status-history.json"
api tests list vm_id="$vm_id" limit=100 >"$OUT_DIR/tests.json"
jq -c '.[] | {id, point_id, verdict, proven_rto_s, started_at, finished_at, reason, bundle_id}' "$OUT_DIR/tests.json"
api jobs list vm_id="$vm_id" limit=200 >"$OUT_DIR/jobs.json"
jq -r 'group_by(.kind)[] | "\(.[0].kind): \(length) jobs, states \(map(.state) | group_by(.) | map("\(.[0])=\(length)") | join(" "))"' "$OUT_DIR/jobs.json"
api points list vm_id="$vm_id" limit=100 >"$OUT_DIR/points.json"
for t in $(jq -r '.[] | select(.verdict == "CRITICAL") | .id' "$OUT_DIR/tests.json" | head -1); do
	api api GET "/api/v1/restore-tests/$t" >"$OUT_DIR/test-critical.json" || true
done
"$BIN/gaia" evidence verify -pubkey "$SRV_HOME/data/keys/evidence-key.pub" -ledger "$SRV_HOME/data/evidence/ledger.jsonl" -evidence-dir "$SRV_HOME/data/evidence/bundles" -json \
	>"$OUT_DIR/ledger-verify.json" || true
jq -c '{verdict, entries, intact, bundles: (.bundles | length)}' "$OUT_DIR/ledger-verify.json"
api audit verify | jq -c . | tee "$OUT_DIR/audit-verify.json"
crit_bundle=$(jq -r '[.[] | select(.verdict == "CRITICAL")][0].bundle_id // ""' "$OUT_DIR/tests.json")
if [[ -n "$crit_bundle" ]]; then
	mkdir -p "$OUT_DIR/bundle-critical"
	cp "$SRV_HOME/data/evidence/bundles/$crit_bundle"/{result.json,manifest.json} "$OUT_DIR/bundle-critical/" 2>/dev/null || true
	cp "$SRV_HOME/data/evidence/bundles/$crit_bundle"/screenshot-*.png "$OUT_DIR/bundle-critical/" 2>/dev/null || true
	tail -c 2000 "$SRV_HOME/data/evidence/bundles/$crit_bundle/serial.log" >"$OUT_DIR/bundle-critical/serial-tail.log" 2>/dev/null || true
fi

# --- gate --------------------------------------------------------------------------------------------------------
section "gate (CLAUDE.md T16)"
jq -n --slurpfile h "$OUT_DIR/status-history.json" --slurpfile t "$OUT_DIR/tests.json" --slurpfile p "$OUT_DIR/phases.jsonl" \
	--slurpfile e "$OUT_DIR/status-end.json" --slurpfile l "$OUT_DIR/ledger-verify.json" --rawfile tt "$OUT_DIR/truth-table.txt" '
	($h[0] | map({at, from, to, reason})) as $hist
	| ($hist | map(.to)) as $seq
	| {
	  a_truth_table: {passed: ($tt | test("--- PASS: TestTruthTableExhaustive") and test("--- PASS: TestTruthTableExamples")),
	                  combinations: ($tt | capture("(?<n>[0-9]+) combinations").n | tonumber)},
	  b_timeline: {
	    expected: ["UNKNOWN", "OK", "CRITICAL", "OK", "WARNING"], recorded: $seq, transitions: $hist,
	    critical_reason_names_the_test: ([$hist[] | select(.to == "CRITICAL") | .reason | test("restore test .* CRITICAL")] | all),
	    warning_reason: ([$hist[] | select(.to == "WARNING") | .reason][0]),
	    phases: $p,
	    pass: ($seq == ["UNKNOWN", "OK", "CRITICAL", "OK", "WARNING"])},
	  tests: [$t[0][] | {id, point_id, verdict, proven_rto_s}] | reverse,
	  status_at_end: {status: $e[0].status, rules: [$e[0].reasons[].rule]},
	  ledger: {verdict: $l[0].verdict, intact: $l[0].intact, entries: $l[0].entries}
	}
	| . + {verdict: (if (.a_truth_table.passed | not) or (.b_timeline.pass | not) or (.b_timeline.critical_reason_names_the_test | not) then "CRITICAL"
	                 elif .ledger.intact != true then "CRITICAL" else "OK" end)}' | tee "$OUT_DIR/gate.json"

section "result"
verdict=$(jq -r .verdict "$OUT_DIR/gate.json")
echo "$verdict"
echo "(gate.json, timeline.jsonl, status-history.json, tests.json, server.journal, sandbox.journal in $OUT_DIR)"
case $verdict in
OK) exit 0 ;;
CRITICAL) exit 2 ;;
*) exit 3 ;;
esac
