#!/usr/bin/env bash
# Runs T19 on gaia-lab: notifications by email and webhook, the daily report with its signed ledger head, and the
# detection of a ledger cut back after a daily report (ADR-007 §10, evidence spec §6.8/§8.6).
#
# fixture: notify-sink (lab/tools/notify-sink), a test SMTP server (STARTTLS and AUTH required) and an HTTPS webhook
# receiver on 127.0.0.1, with a certificate from a CA made for this run; it keeps every message it gets in
# OUT_DIR/sink/{mail,hook}.
#
# gaia server and its sandbox helper run as in T16 (packaged units, own users; filesystem repository); the KVM host is
# gaia-lab over SSH as the host user gaia-t19-backup (never gaia-backup: that one is server B's). Two channels are
# added from the guide (step 8c): mail (smtp, with the run's CA) and hook (webhook). Then every event is caused:
#   status       the VM is added (-> UNKNOWN), proven (-> OK), damaged (-> CRITICAL) and proven again (-> OK);
#   job_failed   a backup while the host refuses Gaia's key;
#   integrity,   one byte flipped in the repository's oldest data pack, then a full verification: critical, the damaged
#   quarantine   contents quarantined;
#   ledger       a bundle's file changed behind Gaia's back (CRITICAL), put back (OK again), and at the end the ledger
#                cut back after the daily report (CRITICAL, ledger_truncated);
#   daily_report sent on request.
# Gate (a): every event kind reaches both the SMTP sink and the webhook receiver, every webhook carries a valid
# signature, and every message Gaia queued is sent. Gate (b): after the daily report, the ledger (an export of it, and
# the server's own) is cut back by its last entry and that bundle: the cut ledger alone verifies, with the daily report
# received by mail or by webhook as -head it is CRITICAL (ledger_truncated), and the server's own check says so too.
#
# Usage (on gaia-lab, as root, after setup.sh):
#   systemd-run --unit=gaia-t19 --collect -E HOME=/root lab/lab-vm/t19-notify.sh
# Env: INTERVAL_S (300: backup and test interval), RPO_S (1200), RTO_S (600), MAX_PROOF_S (1800), DISK_GB (4),
#      DATA_GB (1), PHASE_TIMEOUT_S (1800), HOST_ADDR, OUT_DIR (/root/t19-<UTC timestamp>), KEEP=1 keeps everything.
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

INTERVAL_S="${INTERVAL_S:-300}"
RPO_S="${RPO_S:-1200}"
RTO_S="${RTO_S:-600}"
MAX_PROOF_S="${MAX_PROOF_S:-1800}"
DISK_GB="${DISK_GB:-4}"
DATA_GB="${DATA_GB:-1}"
PHASE_TIMEOUT_S="${PHASE_TIMEOUT_S:-1800}"
HOST_ADDR="${HOST_ADDR:-$(hostname -I | awk '{ print $1 }')}"
OUT_DIR="${OUT_DIR:-/root/t19-$(date -u +%Y%m%dT%H%M%SZ)}"
DOM=gaia-t19
IMAGES=/var/lib/gaia/images
CLOUD_URL=https://cloud.debian.org/images/cloud/bookworm/latest
CLOUD_IMG=debian-12-genericcloud-amd64.qcow2
LV_IMAGES=/var/lib/libvirt/images
VDA="$LV_IMAGES/$DOM-vda.qcow2"
VDB="$LV_IMAGES/$DOM-vdb.qcow2"
SEED="$LV_IMAGES/$DOM-seed.iso"
SERIAL=/var/log/libvirt/qemu/$DOM-serial.log
WORK=/var/lib/gaia/t19
SRV_USER=gaia-t19
SRV_HOME=/var/lib/gaia-t19
SRV_BIN=/usr/local/sbin/gaia-t19
UNIT=gaia-t19-server
SB_UNIT=gaia-t19-sandbox
SB_HOME=/var/lib/gaia-t19-sandbox
SB_SOCK=/run/gaia-t19-sandbox/sandbox.sock
SB_WORK=$LV_IMAGES/gaia-t19-sandbox
SINK_UNIT=gaia-t19-sink
SINK_SMTP=127.0.0.1:2587
SINK_HTTPS=127.0.0.1:2443
HOST_USER=gaia-t19-backup
API=https://127.0.0.1:8443
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
[[ $(uname -m) == x86_64 ]] || { echo "UNKNOWN: T19 runs on gaia-lab (x86_64)"; exit 3; }
for bin in go jq make qemu-img virsh cloud-localds curl openssl sha512sum fstrim ssh-keygen sshd python3; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done
mkdir -p "$OUT_DIR"
exec > >(tee "$OUT_DIR/t19.log") 2>&1
ln -sfn "$OUT_DIR" /root/t19-latest

section() { printf '\n== %s\n' "$*"; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
gaia() { "$BIN/gaia" "$@"; }
api() { gaia "$@" -json; }

cleanup() {
	[[ "${KEEP:-}" == 1 ]] && return 0
	virsh -q destroy "$DOM" >/dev/null 2>&1 || true
	virsh -q undefine "$DOM" --checkpoints-metadata >/dev/null 2>&1 || true
	rm -f "$VDA" "$VDB" "$SEED" "$LV_IMAGES"/*.gaia-*.scratch 2>/dev/null || true
	for u in "$UNIT" "$SB_UNIT" "$SINK_UNIT"; do
		systemctl stop "$u" >/dev/null 2>&1 || true
		systemctl reset-failed "$u" >/dev/null 2>&1 || true
	done
	rm -f "/run/systemd/system/$UNIT.service" "/run/systemd/system/$SB_UNIT.service"
	systemctl daemon-reload
	rm -rf "$SRV_HOME" "$SB_HOME" "$SB_WORK" "$WORK" /root/.config/gaia
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
		for u in "$UNIT" "$SB_UNIT" "$SINK_UNIT"; do
			journalctl -u "$u" --no-pager -o short-iso-precise >"$OUT_DIR/${u#gaia-t19-}.journal" 2>&1 || true
		done
	fi
	cleanup
	sync
	fstrim -a >/dev/null 2>&1 || true
}
trap on_exit EXIT

section "date / commit"
date -u +%Y-%m-%dT%H:%M:%SZ
git -C "$repo_root" rev-parse HEAD 2>/dev/null || echo "not a git checkout"
echo "output: $OUT_DIR; backup and test every $INTERVAL_S s; host $HOST_USER@$HOST_ADDR"

section "build"
owner=$(stat -c %U "$repo_root")
asowner() {
	if [[ "$owner" == root ]]; then "$@"; else runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" "$@"; fi
}
asowner make -C "$repo_root" -s build
asowner go build -C "$repo_root" -o "$BIN/notify-sink" ./lab/tools/notify-sink
go version -m "$BIN/gaia" | grep -E 'vcs\.(revision|modified)|kopia/kopia' || echo "no VCS stamp"
virsh -q version
[[ -e /dev/kvm ]] || { echo "UNKNOWN: /dev/kvm is missing in gaia-lab"; exit 3; }

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

# --- the sink (fixture) ------------------------------------------------------------------------------------------
section "notify-sink: SMTP $SINK_SMTP (STARTTLS + AUTH), HTTPS $SINK_HTTPS"
ca=$WORK/ca
install -d -m 0700 "$ca"
openssl ecparam -genkey -name prime256v1 -noout -out "$ca/ca.key" 2>/dev/null
openssl req -x509 -new -key "$ca/ca.key" -sha256 -days 7 -subj "/CN=gaia-t19 lab CA" -out "$ca/ca.crt"
openssl ecparam -genkey -name prime256v1 -noout -out "$ca/server.key" 2>/dev/null
openssl req -new -key "$ca/server.key" -subj "/CN=127.0.0.1" -out "$ca/server.csr"
printf 'subjectAltName=IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' >"$ca/ext"
openssl x509 -req -in "$ca/server.csr" -CA "$ca/ca.crt" -CAkey "$ca/ca.key" -CAcreateserial -days 7 -sha256 -extfile "$ca/ext" -out "$ca/server.crt" 2>/dev/null
cp "$ca/ca.crt" "$OUT_DIR/sink-ca.crt"
smtp_pw=$(head -c 18 /dev/urandom | base64 | tr -d '/+=')
printf 'SINK_SMTP_PASSWORD=%s\n' "$smtp_pw" | install -m 0600 /dev/stdin "$ca/sink.env"
mkdir -p "$OUT_DIR/sink"
systemd-run --unit="$SINK_UNIT" -p EnvironmentFile="$ca/sink.env" "$BIN/notify-sink" -dir "$OUT_DIR/sink" -smtp "$SINK_SMTP" \
	-https "$SINK_HTTPS" -cert "$ca/server.crt" -key "$ca/server.key" -smtp-user gaia
for ((i = 0; i < 30; i++)); do
	curl -fs --cacert "$ca/ca.crt" -X POST -d '{}' "https://$SINK_HTTPS/probe" -o /dev/null && break
	sleep 1
done
rm -f "$OUT_DIR/sink/hook/"*.json # the probe
echo "sink is up"

# --- gaia server and the sandbox helper, from the packaged units (as T16) ---------------------------------------
section "gaia server and sandbox helper"
useradd --system --home-dir "$SRV_HOME" --shell /usr/sbin/nologin "$SRV_USER"
install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME" "$SRV_HOME/data" "$SRV_HOME/repo"
install -m 0755 "$BIN/gaia" "$SRV_BIN"
sed -e "s|^ExecStart=.*|ExecStart=$SRV_BIN sandbox serve -group $SRV_USER -socket $SB_SOCK -images $SB_HOME/images -work-dir $SB_WORK -bundle-dir $SB_HOME/bundles|" \
	"$repo_root/packaging/systemd/gaia-sandbox.service" >"/run/systemd/system/$SB_UNIT.service"
pwfile="$SRV_HOME/admin.pw"
kitfile="$SRV_HOME/kit.pass"
head -c 24 /dev/urandom | base64 | install -m 0600 -o "$SRV_USER" -g "$SRV_USER" /dev/stdin "$pwfile"
head -c 24 /dev/urandom | base64 | install -m 0600 -o "$SRV_USER" -g "$SRV_USER" /dev/stdin "$kitfile"
runuser -u "$SRV_USER" -- "$SRV_BIN" server init -config "$SRV_HOME/server.json" -data-dir "$SRV_HOME/data" \
	-listen 127.0.0.1:8443 -hostname 127.0.0.1 -admin admin -password-file "$pwfile" -kit-passphrase-file "$kitfile" -json >"$OUT_DIR/server-init.json"
cert=$(jq -r .result.tls_certificate_sha256 "$OUT_DIR/server-init.json")
jq --arg s "$SB_SOCK" --arg i "$SB_HOME/images" '. + {sandbox_socket: $s, sandbox_images_dir: $i}' "$SRV_HOME/server.json" >"$WORK/server.json"
install -m 0640 -o "$SRV_USER" -g "$SRV_USER" "$WORK/server.json" "$SRV_HOME/server.json"
jq . "$SRV_HOME/server.json" | tee "$OUT_DIR/server.json"
sed -e "s|^User=.*|User=$SRV_USER|" -e "s|^Group=.*|Group=$SRV_USER|" \
	-e "s|^ExecStart=.*|ExecStart=$SRV_BIN server run -config $SRV_HOME/server.json|" \
	-e "s|^ReadWritePaths=.*|ReadWritePaths=$SRV_HOME -$SB_HOME/images|" \
	-e "s|gaia-sandbox.service|$SB_UNIT.service|" \
	"$repo_root/packaging/systemd/gaia.service" >"/run/systemd/system/$UNIT.service"
systemctl daemon-reload
systemctl start "$SB_UNIT"
for ((i = 0; i < 30; i++)); do [[ -S $SB_SOCK ]] && break; sleep 1; done
systemctl start "$UNIT"
SRV_STARTED=1
for ((i = 0; i < 30; i++)); do curl -fsk "$API/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
api login -server "$API" -user admin -fingerprint "$cert" -password-file "$pwfile" >/dev/null
api system >"$OUT_DIR/system.json"
jq -c '{version, schema_version, daily_report_at, notifications, ledger: .ledger.text}' "$OUT_DIR/system.json"

section "host: $HOST_USER over SSH"
useradd -m -s /usr/sbin/nologin -G libvirt,libvirt-qemu "$HOST_USER"
install -d -m 700 -o "$HOST_USER" -g "$HOST_USER" "/home/$HOST_USER/.ssh"
AUTH_KEYS=/home/$HOST_USER/.ssh/authorized_keys
jq -r .ssh_authorized_keys_line "$OUT_DIR/system.json" >"$AUTH_KEYS"
chown "$HOST_USER:" "$AUTH_KEYS"
chmod 600 "$AUTH_KEYS"
host_fp=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $2 }')
host_id=$(api hosts add name=gaia-lab address="$HOST_ADDR" ssh_user="$HOST_USER" | jq .id)
[[ $(api hosts scan "$host_id" | jq -r .fingerprint) == "$host_fp" ]] || { echo "UNKNOWN: scanned fingerprint differs"; exit 3; }
api hosts trust "$host_id" fingerprint="$host_fp" >/dev/null
api hosts check "$host_id" | jq -c .

# --- notification channels, as guide step 8c --------------------------------------------------------------------
section "channels (guide step 8c)"
# shellcheck disable=SC2016 # JSON, not shell
GAIA_SMTP_PASSWORD=$smtp_pw api channels add name=mail kind=smtp ca_file="$ca/ca.crt" \
	smtp="{\"host\":\"127.0.0.1\",\"port\":${SINK_SMTP##*:},\"username\":\"gaia\",\"from\":\"Gaia T19 <gaia@lab.test>\",\"to\":[\"ops@lab.test\"]}" |
	tee "$OUT_DIR/channel-mail.json" | jq -c '{id, name, kind, events, enabled}'
api channels add name=hook kind=webhook ca_file="$ca/ca.crt" webhook="{\"url\":\"https://$SINK_HTTPS/gaia\"}" >"$WORK/channel-hook.json"
jq -r .webhook_secret "$WORK/channel-hook.json" | install -m 0600 /dev/stdin "$ca/webhook.secret"
jq 'del(.webhook_secret)' "$WORK/channel-hook.json" | tee "$OUT_DIR/channel-hook.json" | jq -c '{id, name, kind, events, enabled}'
mail_id=$(jq .id "$OUT_DIR/channel-mail.json")
hook_id=$(jq .id "$OUT_DIR/channel-hook.json")
for id in "$mail_id" "$hook_id"; do
	api channels test "$id" | tee -a "$OUT_DIR/channel-tests.jsonl" | jq -c '{id, channel_id, event, state, attempts, last_error}'
done
[[ $(jq -sc 'map(.state) | unique' "$OUT_DIR/channel-tests.jsonl") == '["sent"]' ]] ||
	echo "WARNING: a test message was not sent (the gate will say so)"

# --- the guest (as T16, without the control line) ----------------------------------------------------------------
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
  - path: /etc/systemd/system/gaia-churn.service
    content: |
      [Unit]
      Description=Gaia T19 test workload
      RequiresMountsFor=/data
      [Service]
      ExecStart=/usr/local/sbin/gaia-churn
      [Install]
      WantedBy=multi-user.target
runcmd:
  - [/usr/local/sbin/gaia-setup]
  - [systemctl, daemon-reload]
  - [systemctl, enable, --now, gaia-churn.service]
  - [sh, -c, 'echo GAIA-READY > /dev/ttyS0']
EOF
cloud-localds "$SEED" "$WORK/user-data" "$WORK/meta-data"
rm -f "$SERIAL"
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
    <disk type='file' device='disk'><driver name='qemu' type='raw'/><source file='$SEED'/><target dev='vdc' bus='virtio'/><readonly/></disk>
    <serial type='file'><source path='$SERIAL'/><target port='0'/></serial>
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

# --- helpers -----------------------------------------------------------------------------------------------------
: >"$OUT_DIR/timeline.jsonl"
: >"$OUT_DIR/injections.jsonl"
last_seen=""
status_now() { gaia status "$vm_id" -json 2>/dev/null || true; }
wait_status() { # TARGET PHASE
	local target=$1 phase=$2 t0 s st
	t0=$(date +%s)
	while (($(date +%s) - t0 < PHASE_TIMEOUT_S)); do
		s=$(status_now)
		st=$(jq -r .status <<<"$s")
		if [[ "$st" != "$last_seen" ]]; then
			log "status $st: $(jq -r '.reasons[0].text' <<<"$s")"
			jq -c --arg phase "$phase" '{seen_at: (now | todate), phase: $phase, status, changed_at, reason: .reasons[0].text}' <<<"$s" >>"$OUT_DIR/timeline.jsonl"
			last_seen=$st
		fi
		[[ "$st" == "$target" ]] && return 0
		sleep 10
	done
	echo "UNKNOWN: phase $phase: status $last_seen, not $target, after $PHASE_TIMEOUT_S s"
	exit 3
}
wait_verification() {
	local id=$1
	while [[ $(api verifications get "$id" | tee "$WORK/ver.json" | jq -r .state) == running ]]; do sleep 2; done
	cat "$WORK/ver.json"
}
inject() { jq -nc --arg what "$1" '{at: (now | todate), injected: $what}' | tee -a "$OUT_DIR/injections.jsonl"; }
# delivered: waits until no message of Gaia's is still queued (the notifier looks every 5 s).
delivered() {
	local i
	for ((i = 0; i < 60; i++)); do
		[[ $(api notifications list limit=1000 | jq '[.[] | select(.state == "queued")] | length') == 0 ]] && return 0
		sleep 5
	done
	echo "WARNING: messages still queued after 300 s (the gate will say so)"
}

# --- events --------------------------------------------------------------------------------------------------------
section "repository, policy, VM (status: -> UNKNOWN -> OK)"
repo_id=$(GAIA_KIT_PASSPHRASE=$(cat "$kitfile") api repositories add name=main location="$SRV_HOME/repo" | jq .id)
api policies add name=t19 repository_id="$repo_id" backup_interval_s="$INTERVAL_S" test_interval_s="$INTERVAL_S" keep_last=8 \
	keep_daily=0 keep_weekly=0 rpo_target_s="$RPO_S" rto_target_s="$RTO_S" max_proof_age_s="$MAX_PROOF_S" check_template=linux-boot |
	tee "$OUT_DIR/policy.json" | jq -c .
policy_id=$(jq .id "$OUT_DIR/policy.json")
api vms add host_id="$host_id" name="$DOM" policy_id="$policy_id" | tee "$OUT_DIR/vm.json" | jq -c .
vm_id=$(jq .id "$OUT_DIR/vm.json")
wait_status UNKNOWN added
wait_status OK proven

idle() { # waits until the VM has no queued or running job
	for ((i = 0; i < 120; i++)); do
		[[ $(api jobs list vm_id="$vm_id" limit=20 | jq '[.[] | select(.state == "queued" or .state == "running")] | length') == 0 ]] && return 0
		sleep 5
	done
	echo "UNKNOWN: the VM's jobs did not end within 600 s"
	exit 3
}

section "job_failed: a backup while the host refuses Gaia's key"
idle
before=$(api jobs list vm_id="$vm_id" limit=20 | jq '[.[] | select(.kind == "backup") | .id] | max // 0')
mv "$AUTH_KEYS" "$AUTH_KEYS.off"
inject "host authorized_keys removed"
api vms backup "$vm_id" >/dev/null || true # (409 if the scheduler queued one first: that one is taken)
job=0
for ((i = 0; i < 300; i++)); do
	job=$(api jobs list vm_id="$vm_id" limit=20 | jq --argjson b "$before" '[.[] | select(.kind == "backup" and .id > $b and (.state == "failed" or .state == "ok"))] | (min_by(.id) | .id) // 0')
	((job > 0)) && break
	sleep 2
done
((job > 0)) || { echo "UNKNOWN: no backup ended within 600 s of removing the key"; exit 3; }
api jobs get "$job" | tee "$OUT_DIR/job-failed.json" | jq -c '{id, kind, state, error}'
mv "$AUTH_KEYS.off" "$AUTH_KEYS"
inject "host authorized_keys restored"
api vms set "$vm_id" policy_id=0 >/dev/null # no scheduled backup while the repository is damaged and verified
idle

section "integrity and quarantine: one byte of the oldest data pack (status: -> CRITICAL)"
pack=$(find "$SRV_HOME/repo" -type f -path "$SRV_HOME/repo/p*" -size +1M -printf '%T@ %p\n' | sort -n | head -1 | cut -d' ' -f2)
[[ -n "$pack" ]] || { echo "UNKNOWN: no data pack in the repository"; exit 3; }
size=$(stat -c %s "$pack")
off=$((size / 2))
before=$(od -An -tx1 -j "$off" -N1 "$pack" | tr -d ' ')
printf "%b" "\\x$(printf %02x $((0x$before ^ 0xff)))" | dd of="$pack" bs=1 seek="$off" count=1 conv=notrunc status=none
inject "pack ${pack#"$SRV_HOME"/repo/} byte $off $before -> $(od -An -tx1 -j "$off" -N1 "$pack" | tr -d ' ')"
v=$(api repositories verify "$repo_id" mode=full | jq .id)
wait_verification "$v" | tee "$OUT_DIR/verification-full.json" | jq -c '{id, mode, state, points_checked, error, affected: [.affected[] | {point_id, disk, contents}]}'
api quarantine list repository_id="$repo_id" | tee "$OUT_DIR/quarantine.json" | jq -c '.[] | {content_id, points}'
wait_status CRITICAL damaged

section "back to OK: the policy again, a full backup and a test"
api vms set "$vm_id" policy_id="$policy_id" >/dev/null
wait_status OK proven-again

section "ledger: a bundle's file changed behind Gaia's back, then put back"
bdir=$(find "$SRV_HOME/data/evidence/bundles" -mindepth 1 -maxdepth 1 -type d | sort | head -1)
cp -p "$bdir/result.json" "$WORK/result.json.orig"
echo " " >>"$bdir/result.json"
inject "bundle $(basename "$bdir"): result.json changed"
api ledger check >"$OUT_DIR/ledger-check-changed.json" || true # exit code: the verdict
jq -c '{verdict, entries, problems}' "$OUT_DIR/ledger-check-changed.json"
cp -p "$WORK/result.json.orig" "$bdir/result.json"
inject "bundle $(basename "$bdir"): result.json put back"
api ledger check >"$OUT_DIR/ledger-check-restored.json" || true # exit code: the verdict
jq -c '{verdict, entries, problems}' "$OUT_DIR/ledger-check-restored.json"

section "daily report"
api vms set "$vm_id" policy_id=0 >/dev/null # the ledger stops growing: no new test seals a bundle from here on
idle
api daily-reports run | tee "$OUT_DIR/daily-report.json" | jq -c '{id, generated_at, head_seq, head_sha256}'
delivered
gaia status || true

# --- gate (b): the ledger cut back after the daily report ---------------------------------------------------------
section "the daily report as received, and the ledger cut back after it"
python3 -I - "$OUT_DIR/sink" "$OUT_DIR" <<'PY'
import email, glob, json, os, sys
sink, out = sys.argv[1], sys.argv[2]
for p in sorted(glob.glob(os.path.join(sink, "mail", "*.eml"))):
    m = email.message_from_bytes(open(p, "rb").read())
    if m["X-Gaia-Event"] == "daily_report":
        for part in m.walk():
            if part.get_filename():
                open(os.path.join(out, "daily-report-mail.json"), "wb").write(part.get_payload(decode=True))
for p in sorted(glob.glob(os.path.join(sink, "hook", "*.json"))):
    h = json.load(open(p))
    if h["headers"].get("X-Gaia-Event") == "daily_report":
        open(os.path.join(out, "daily-report-hook.json"), "w").write(h["body"])
PY
ls -la "$OUT_DIR"/daily-report-{mail,hook}.json
ev=$WORK/export
mkdir -p "$ev"
cp -a "$SRV_HOME/data/evidence/ledger.jsonl" "$ev/ledger.jsonl"
cp -a "$SRV_HOME/data/evidence/bundles" "$ev/bundles"
cp "$SRV_HOME/data/keys/evidence-key.pub" "$OUT_DIR/anchor.pub"
verify() { # NAME ARGS...: gaia evidence verify of the export, the result in OUT_DIR/verify-NAME.json
	local name=$1
	shift
	gaia evidence verify -pubkey "$OUT_DIR/anchor.pub" -evidence-dir "$ev/bundles" "$@" -json >"$OUT_DIR/verify-$name.json" || true
	jq -c --arg n "$name" '{check: $n, verdict, findings: [.findings[] | {code, where}], heads: [.report.heads[] | {seq, result}]}' "$OUT_DIR/verify-$name.json"
}
verify intact-with-mail-head -ledger "$ev/ledger.jsonl" -head "$OUT_DIR/daily-report-mail.json"
entries=$(wc -l <"$ev/ledger.jsonl")
last_bundle=$(tail -1 "$ev/ledger.jsonl" | jq -r '.payload | fromjson | .bundle_id // empty')
head -n $((entries - 1)) "$ev/ledger.jsonl" >"$ev/cut.jsonl"
[[ -n "$last_bundle" ]] && rm -rf "${ev:?}/bundles/$last_bundle"
inject "export: ledger cut from $entries to $((entries - 1)) entries, bundle $last_bundle removed"
verify cut-alone -ledger "$ev/cut.jsonl"
verify cut-with-mail-head -ledger "$ev/cut.jsonl" -head "$OUT_DIR/daily-report-mail.json"
verify cut-with-hook-head -ledger "$ev/cut.jsonl" -head "$OUT_DIR/daily-report-hook.json"

section "the server's own ledger cut back the same way"
live=$SRV_HOME/data/evidence/ledger.jsonl
head -n $((entries - 1)) "$live" >"$WORK/live-cut.jsonl"
cat "$WORK/live-cut.jsonl" >"$live" # keeps owner and mode
[[ -n "$last_bundle" ]] && rm -rf "${SRV_HOME:?}/data/evidence/bundles/$last_bundle"
inject "server: ledger cut from $entries to $((entries - 1)) entries, bundle $last_bundle removed"
cut_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
api ledger check >"$OUT_DIR/ledger-check-cut.json" || true # exit code: the verdict
jq -c '{verdict, entries, problems}' "$OUT_DIR/ledger-check-cut.json"
delivered
gaia status || true

# --- records -----------------------------------------------------------------------------------------------------
section "records"
api notifications list limit=1000 >"$OUT_DIR/notifications.json"
jq -r '.[] | [.id, .channel_id, .event, .state, .attempts, .subject] | @tsv' "$OUT_DIR/notifications.json" | sort -n
api state-changes list entity=vm_status entity_id="$vm_id" >"$OUT_DIR/status-history.json"
api state-changes list entity=ledger >"$OUT_DIR/ledger-history.json"
api daily-reports get "$(jq .id "$OUT_DIR/daily-report.json")" | jq '.report' >"$OUT_DIR/daily-report-server.json"
api system >"$OUT_DIR/system-end.json"
api audit verify | tee "$OUT_DIR/audit-verify.json" | jq -c .
echo "sink: $(find "$OUT_DIR/sink/mail" -type f | wc -l) mails, $(find "$OUT_DIR/sink/hook" -type f | wc -l) webhook posts"

# What reached the sink: per message its event, and for webhooks whether the signature is right.
python3 -I - "$OUT_DIR/sink" "$ca/webhook.secret" >"$OUT_DIR/received.json" <<'PY'
import email, email.header, glob, hashlib, hmac, json, os, sys
sink, secret = sys.argv[1], open(sys.argv[2], "rb").read().strip()
out = {"mail": [], "hook": []}
for p in sorted(glob.glob(os.path.join(sink, "mail", "*.eml"))):
    m = email.message_from_bytes(open(p, "rb").read())
    subj = str(email.header.make_header(email.header.decode_header(m["Subject"] or "")))
    text = ""
    for part in m.walk():
        if part.get_content_type() == "text/plain":
            text = part.get_payload(decode=True).decode("utf-8", "replace")
    out["mail"].append({"file": os.path.basename(p), "event": m["X-Gaia-Event"], "subject": subj, "text": text[:600]})
for p in sorted(glob.glob(os.path.join(sink, "hook", "*.json"))):
    h = json.load(open(p))
    body = h["body"].encode()
    good = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
    ev = json.loads(body)
    out["hook"].append({"file": os.path.basename(p), "event": h["headers"].get("X-Gaia-Event"), "delivery": h["headers"].get("X-Gaia-Delivery"),
                        "signature_ok": hmac.compare_digest(good, h["headers"].get("X-Gaia-Signature", "")), "subject": ev.get("subject"),
                        "state_change": ev.get("state_change")})
json.dump(out, sys.stdout, indent=2)
PY
jq -c '{mail: [.mail[] | {event, subject}], hook: [.hook[] | {event, signature_ok}]}' "$OUT_DIR/received.json"

# --- gate --------------------------------------------------------------------------------------------------------
section "gate (CLAUDE.md T19)"
jq -n --slurpfile r "$OUT_DIR/received.json" --slurpfile n "$OUT_DIR/notifications.json" --slurpfile h "$OUT_DIR/status-history.json" \
	--slurpfile lh "$OUT_DIR/ledger-history.json" --slurpfile c "$OUT_DIR/ledger-check-cut.json" --slurpfile ct "$OUT_DIR/channel-tests.jsonl" \
	--slurpfile v1 "$OUT_DIR/verify-intact-with-mail-head.json" --slurpfile v2 "$OUT_DIR/verify-cut-alone.json" \
	--slurpfile v3 "$OUT_DIR/verify-cut-with-mail-head.json" --slurpfile v4 "$OUT_DIR/verify-cut-with-hook-head.json" \
	--slurpfile dr "$OUT_DIR/daily-report.json" --arg cut_at "$cut_at" '
	["status", "job_failed", "integrity", "quarantine", "ledger", "daily_report"] as $kinds
	| ($r[0]) as $got
	| def codes($v): [$v.findings[].code] | unique;
	def ts: sub("\\.[0-9]+"; "") | fromdateiso8601;
	($cut_at | ts) as $cut
	| [$got.hook[] | select(.event == "ledger" and .state_change.to == "CRITICAL" and (.state_change.at | ts) >= $cut)] as $after_cut
	| {
	  a_events: {
	    per_kind: [$kinds[] as $k | {event: $k, mail: ([$got.mail[] | select(.event == $k)] | length), webhook: ([$got.hook[] | select(.event == $k)] | length)}],
	    test_messages: [$ct[] | {channel_id, state}],
	    webhook_signatures_ok: ([$got.hook[] | .signature_ok] | all),
	    notifications: ($n[0] | group_by(.state) | map({state: .[0].state, count: length})),
	    status_changes: [$h[0][] | {at, from, to, reason}],
	    status_mails: [$got.mail[] | select(.event == "status") | .subject],
	    pass: (([$kinds[] as $k | ([$got.mail[] | select(.event == $k)] | length) > 0 and ([$got.hook[] | select(.event == $k)] | length) > 0] | all)
	           and ([$got.hook[] | .signature_ok] | all) and ($n[0] | all(.state == "sent")) and ([$ct[] | .state] | all(. == "sent")))},
	  b_truncation: {
	    daily_report: {id: $dr[0].id, head_seq: $dr[0].head_seq, head_sha256: $dr[0].head_sha256},
	    intact_with_mail_head: {verdict: $v1[0].verdict, findings: codes($v1[0])},
	    cut_alone: {verdict: $v2[0].verdict, findings: codes($v2[0])},
	    cut_with_mail_head: {verdict: $v3[0].verdict, findings: codes($v3[0]), heads: [$v3[0].report.heads[] | {seq, result, detail}]},
	    cut_with_hook_head: {verdict: $v4[0].verdict, findings: codes($v4[0])},
	    server_check: {verdict: $c[0].verdict, entries: $c[0].entries, problems: $c[0].problems},
	    ledger_event_after_cut: {webhook: ($after_cut | length), mail: ([$got.mail[] | select(.event == "ledger" and (.subject | test("CRITICAL")))] | length)},
	    ledger_history: [$lh[0][] | {at, from, to}],
	    pass: ($v1[0].verdict == "OK" and $v2[0].verdict == "OK" and $v3[0].verdict == "CRITICAL" and codes($v3[0]) == ["ledger_truncated"]
	           and $v4[0].verdict == "CRITICAL" and codes($v4[0]) == ["ledger_truncated"] and $c[0].verdict == "CRITICAL"
	           and ($c[0].problems | any(test("ledger_truncated"))) and ($lh[0] | last | .to == "CRITICAL" and (.at | ts) >= $cut)
	           and ($after_cut | length) >= 1)}
	}
	| . + {verdict: (if .a_events.pass and .b_truncation.pass then "OK" else "CRITICAL" end)}' | tee "$OUT_DIR/gate.json"

section "result"
verdict=$(jq -r .verdict "$OUT_DIR/gate.json")
echo "$verdict"
echo "(gate.json, received.json, notifications.json, sink/, verify-*.json, daily-report-*.json, server.journal in $OUT_DIR)"
case $verdict in
OK) exit 0 ;;
CRITICAL) exit 2 ;;
*) exit 3 ;;
esac
