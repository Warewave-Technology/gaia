#!/usr/bin/env bash
# Runs T15 on gaia-lab: gaia server backs up a real 2-disk VM on a schedule, over SSH only (ADR-007 §3, §4).
#
# Topology: gaia server runs on gaia-lab as its own system user under the packaged systemd unit (hardening as
# shipped). The KVM host is gaia-lab itself, reached over SSH at HOST_ADDR as the user gaia-backup, set up as
# docs/install.md step 10 says (groups libvirt + libvirt-qemu, restricted authorized_keys line, host key pinned
# after comparing the fingerprint). libvirt RPC and NBD both go through that SSH connection to unix sockets.
#
# The guest is Debian 12's cloud image with two disks: vda (system, DISK_GB) and vdb (data, DATA_GB, ext4 on
# /data). A cloud-init workload keeps writing to both. The policy backs it up every INTERVAL_S seconds.
# Cycle n waits for the n-th scheduled backup. In the cycles listed in REF_CYCLES the guest is paused
# REF_LEAD_S seconds before the backup is due, both disks are captured (qemu-img convert -U, raw, sha256), and
# the guest stays paused until the backup has created its checkpoint (T6 method); the point is then restored
# by gaia server (POST /points/{id}/restore-check) and its sha256 compared with the reference.
# Injected failures:
#   after cycle CKPT_DELETE_AFTER: the newest Gaia checkpoint is deleted on the host -> the next backup is full
#     and says why;
#   after cycle RESIZE_AFTER: vdb grows by 1 GiB (virsh blockresize) -> the next backup is full and says why;
#   after the last cycle: a sampled verification (must be ok), then one byte is flipped in the newest data pack
#     of the repository -> a full verification is critical and names the affected points.
# During every backup that can be caught running, `ss -ltnp` and `ss -xlp` are captured: no TCP listener may
# appear for NBD; the export is a unix socket.
# Gates (CLAUDE.md T15): (a) sampled points restore byte-identical to the paused references, (b) the three
# injected failures, (c) no TCP listener for NBD during a backup. Also recorded: at least 8 scheduled cycles,
# at most two Gaia checkpoints on the host after every backup, retention pruning, the scheduled verification.
#
# Usage (on gaia-lab, as root, after setup.sh):
#   systemd-run --unit=gaia-t15 --collect -E HOME=/root lab/lab-vm/t15-scheduled.sh
# Env: CYCLES (9), INTERVAL_S (900), REF_CYCLES ("1 3 5 7 9"), REF_LEAD_S (90), CKPT_DELETE_AFTER (4),
#      RESIZE_AFTER (6), KEEP_LAST (6), DISK_GB (8), DATA_GB (4), HOST_ADDR (first address of `hostname -I`),
#      VERIFY_IN_MIN (40: the scheduled full verification runs this many minutes after the start),
#      OUT_DIR (/root/t15-<UTC timestamp>), KEEP=1 keeps the server, repository and guest.
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

CYCLES="${CYCLES:-9}"
INTERVAL_S="${INTERVAL_S:-900}"
REF_CYCLES="${REF_CYCLES:-1 3 5 7 9}"
REF_LEAD_S="${REF_LEAD_S:-90}"
CKPT_DELETE_AFTER="${CKPT_DELETE_AFTER:-4}"
RESIZE_AFTER="${RESIZE_AFTER:-6}"
KEEP_LAST="${KEEP_LAST:-6}"
DISK_GB="${DISK_GB:-8}"
DATA_GB="${DATA_GB:-4}"
VERIFY_IN_MIN="${VERIFY_IN_MIN:-40}"
HOST_ADDR="${HOST_ADDR:-$(hostname -I | awk '{ print $1 }')}"
OUT_DIR="${OUT_DIR:-/root/t15-$(date -u +%Y%m%dT%H%M%SZ)}"
DOM=gaia-t15
IMAGES=/var/lib/gaia/images
CLOUD_URL=https://cloud.debian.org/images/cloud/bookworm/latest
CLOUD_IMG=debian-12-genericcloud-amd64.qcow2
LV_IMAGES=/var/lib/libvirt/images
VDA="$LV_IMAGES/$DOM-vda.qcow2"
VDB="$LV_IMAGES/$DOM-vdb.qcow2"
SEED="$LV_IMAGES/$DOM-seed.iso"
SERIAL=/var/log/libvirt/qemu/$DOM-serial.log
SRV_USER=gaia-t15
SRV_HOME=/var/lib/gaia-t15
SRV_BIN=/usr/local/sbin/gaia-t15
UNIT=gaia-t15-server
HOST_USER=gaia-backup
API=https://127.0.0.1:8443
WORK=/var/lib/gaia/t15
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
[[ $(uname -m) == x86_64 ]] || { echo "UNKNOWN: T15 runs on gaia-lab (x86_64)"; exit 3; }
for bin in go jq make kopia qemu-img virsh cloud-localds curl sha512sum fstrim ss ssh-keygen sshd; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done
mkdir -p "$OUT_DIR"
exec > >(tee "$OUT_DIR/t15.log") 2>&1
ln -sfn "$OUT_DIR" /root/t15-latest

section() { printf '\n== %s\n' "$*"; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
gaia() { "$BIN/gaia" "$@"; }
api() { gaia "$@" -json; }

cleanup() {
	[[ "${KEEP:-}" == 1 ]] && return 0
	virsh -q destroy "$DOM" >/dev/null 2>&1 || true
	virsh -q undefine "$DOM" --checkpoints-metadata >/dev/null 2>&1 || true
	rm -f "$VDA" "$VDB" "$SEED" "$LV_IMAGES"/*.gaia-*.scratch 2>/dev/null || true
	systemctl stop "$UNIT" >/dev/null 2>&1 || true
	rm -f "/run/systemd/system/$UNIT.service"
	systemctl daemon-reload
	rm -rf "$SRV_HOME" "$WORK" /root/.config/gaia
	userdel "$SRV_USER" >/dev/null 2>&1 || true
	pkill -u "$HOST_USER" >/dev/null 2>&1 || true # sshd sessions of the server's last connections
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
echo "cycles: $CYCLES every $INTERVAL_S s; references in cycles: $REF_CYCLES; checkpoint deleted after $CKPT_DELETE_AFTER;" \
	"vdb resized after $RESIZE_AFTER; keep_last $KEEP_LAST; host $HOST_USER@$HOST_ADDR"

section "build"
owner=$(stat -c %U "$repo_root")
if [[ "$owner" == root ]]; then
	make -C "$repo_root" -s build
else
	runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" make -C "$repo_root" -s build
fi
go version -m "$BIN/gaia" | grep -E 'vcs\.(revision|modified)|kopia/kopia|go-libvirt|x/crypto' || echo "no VCS stamp"
virsh -q version
[[ -e /dev/kvm ]] || { echo "UNKNOWN: /dev/kvm is missing in gaia-lab (see t6-kvm-backup.sh)"; exit 3; }

section "free space"
cleanup
mkdir -p "$IMAGES" "$WORK/ref"
# Two guest disks, one raw reference per disk at a time, the repository, a restore-check of the largest disk.
need_gib=$((3 * (DISK_GB + DATA_GB + 1) + 10))
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

# --- gaia server, installed as docs/install.md says, under the packaged unit -----------------------------------
section "gaia server"
useradd --system --home-dir "$SRV_HOME" --shell /usr/sbin/nologin "$SRV_USER"
install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME" "$SRV_HOME/data" "$SRV_HOME/repo"
install -m 0755 "$BIN/gaia" "$SRV_BIN"
pwfile="$SRV_HOME/admin.pw"
head -c 24 /dev/urandom | base64 | install -m 0600 -o "$SRV_USER" -g "$SRV_USER" /dev/stdin "$pwfile"
runuser -u "$SRV_USER" -- "$SRV_BIN" server init -config "$SRV_HOME/server.json" -data-dir "$SRV_HOME/data" \
	-listen 127.0.0.1:8443 -hostname 127.0.0.1 -admin admin -password-file "$pwfile" -json | tee "$OUT_DIR/server-init.json"
cert=$(jq -r .result.tls_certificate_sha256 "$OUT_DIR/server-init.json")
# The scheduled full verification: today, VERIFY_IN_MIN minutes from now (server local time).
verify_at=$(date -d "+$VERIFY_IN_MIN min" +%H:%M)
jq --arg at "$verify_at" --arg day "$(date +%A | tr '[:upper:]' '[:lower:]')" '. + {verify_at: $at, full_verify_day: $day}' \
	"$SRV_HOME/server.json" >"$SRV_HOME/server.json.new"
install -m 0640 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME/server.json.new" "$SRV_HOME/server.json"
rm -f "$SRV_HOME/server.json.new"
jq . "$SRV_HOME/server.json" | tee "$OUT_DIR/server.json"
sed -e "s|^User=.*|User=$SRV_USER|" -e "s|^Group=.*|Group=$SRV_USER|" \
	-e "s|^ExecStart=.*|ExecStart=$SRV_BIN server run -config $SRV_HOME/server.json|" \
	-e "s|^ReadWritePaths=.*|ReadWritePaths=$SRV_HOME|" \
	"$repo_root/packaging/systemd/gaia.service" >"/run/systemd/system/$UNIT.service"
diff "$repo_root/packaging/systemd/gaia.service" "/run/systemd/system/$UNIT.service" || true
systemctl daemon-reload
systemctl start "$UNIT"
SRV_STARTED=1
for ((i = 0; i < 30; i++)); do
	curl -fsk "$API/api/v1/health" >/dev/null 2>&1 && break
	sleep 1
done
api login -server "$API" -user admin -fingerprint "$cert" -password-file "$pwfile" >/dev/null
rm -f "$pwfile"
api system | tee "$OUT_DIR/system.json" | jq -c '{version, schema_version, ssh_public_key}'

# --- the KVM host side, as docs/install.md step 10 says ---------------------------------------------------------
section "host: user $HOST_USER, authorized_keys, host key"
useradd -m -s /usr/sbin/nologin -G libvirt,libvirt-qemu "$HOST_USER"
install -d -m 700 -o "$HOST_USER" -g "$HOST_USER" "/home/$HOST_USER/.ssh"
jq -r .ssh_authorized_keys_line "$OUT_DIR/system.json" >"/home/$HOST_USER/.ssh/authorized_keys"
chown "$HOST_USER:" "/home/$HOST_USER/.ssh/authorized_keys"
chmod 600 "/home/$HOST_USER/.ssh/authorized_keys"
cut -c1-80 "/home/$HOST_USER/.ssh/authorized_keys"
id "$HOST_USER"
host_fp=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $2 }')
echo "host key fingerprint (read on the host): $host_fp"
sshd -T 2>/dev/null | grep -iE '^(allowstreamlocalforwarding|allowtcpforwarding|passwordauthentication) ' || true

section "register the host"
api hosts add name=gaia-lab address="$HOST_ADDR" ssh_user="$HOST_USER" | tee "$OUT_DIR/host.json" | jq -c .
host_id=$(jq .id "$OUT_DIR/host.json")
api hosts scan "$host_id" | tee "$OUT_DIR/host-scan.json" | jq -c .
[[ $(jq -r .fingerprint "$OUT_DIR/host-scan.json") == "$host_fp" ]] || { echo "UNKNOWN: scanned fingerprint differs from the host's"; exit 3; }
rc=0
api hosts check "$host_id" >"$OUT_DIR/host-check-untrusted.json" 2>&1 || rc=$?
echo "check before trust: exit $rc $(jq -c . "$OUT_DIR/host-check-untrusted.json" 2>/dev/null || cat "$OUT_DIR/host-check-untrusted.json")"
((rc != 0)) || { echo "CRITICAL: an untrusted host was connected to"; exit 2; }
api hosts trust "$host_id" fingerprint="$host_fp" | jq -c '{id, name, ssh_host_key}'
api hosts check "$host_id" | tee "$OUT_DIR/host-check.json" | jq -c .

# --- the guest ---------------------------------------------------------------------------------------------------
section "guest ($DOM: vda ${DISK_GB} GiB system, vdb ${DATA_GB} GiB data)"
qemu-img convert -q -f qcow2 -O qcow2 "$IMAGES/$CLOUD_IMG" "$VDA"
qemu-img resize -q "$VDA" "${DISK_GB}G"
qemu-img create -q -f qcow2 "$VDB" "${DATA_GB}G"
chown libvirt-qemu:libvirt-qemu "$VDA" "$VDB"
cat >"$WORK/meta-data" <<EOF
instance-id: $DOM-$(date +%s)
local-hostname: $DOM
EOF
# The workload, every 2 s: on vda a 1 MiB block and four 64 KiB pieces of a 512 MiB file rewritten at random
# offsets; on vdb the same in a 1 GiB file, plus 256 KiB appended to a log that rotates at 256 MiB. All fsync'd.
cat >"$WORK/user-data" <<'EOF'
#cloud-config
write_files:
  - path: /usr/local/sbin/gaia-churn
    permissions: '0755'
    content: |
      #!/bin/bash
      set -eu
      blkid /dev/vdb >/dev/null 2>&1 || mkfs.ext4 -q -L gaia-data /dev/vdb
      mkdir -p /data && mountpoint -q /data || mount /dev/vdb /data
      a=/var/lib/gaia-churn/data b=/data/data
      mkdir -p /var/lib/gaia-churn
      [ -s "$a" ] || dd if=/dev/urandom of="$a" bs=1M count=512 conv=fsync status=none
      [ -s "$b" ] || dd if=/dev/urandom of="$b" bs=1M count=1024 conv=fsync status=none
      echo "GAIA-READY" > /dev/ttyS0
      total=0
      while true; do
        for f in "$a" "$b"; do
          mib=$(( $(stat -c %s "$f") / 1048576 ))
          dd if=/dev/urandom of="$f" bs=1M count=1 seek=$(( (RANDOM * 32768 + RANDOM) % mib )) conv=notrunc,fsync status=none
          for _ in 1 2 3 4; do
            dd if=/dev/urandom of="$f" bs=64K count=1 seek=$(( (RANDOM * 32768 + RANDOM) % (mib * 16) )) conv=notrunc,fsync status=none
          done
        done
        head -c 262144 /dev/urandom >> /data/app.log && sync /data/app.log
        [ "$(stat -c %s /data/app.log)" -lt 268435456 ] || mv /data/app.log /data/app.log.1
        total=$((total + 2 * (1048576 + 4 * 65536) + 262144))
        echo "GAIA-CHURN bytes=$total" > /dev/ttyS0
        sleep 2
      done
  - path: /etc/systemd/system/gaia-churn.service
    content: |
      [Unit]
      Description=Gaia T15 test workload
      [Service]
      ExecStart=/usr/local/sbin/gaia-churn
      [Install]
      WantedBy=multi-user.target
runcmd:
  - [systemctl, daemon-reload]
  - [systemctl, enable, --now, gaia-churn.service]
EOF
cloud-localds "$SEED" "$WORK/user-data" "$WORK/meta-data"
rm -f "$SERIAL"
# The cloud-init seed is a read-only third disk (vdc); Gaia leaves read-only disks out.
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
log "guest started, waiting for the workload (GAIA-READY on the serial console)"
for ((i = 0; ; i++)); do
	grep -q GAIA-READY "$SERIAL" 2>/dev/null && break
	((i < 240)) || { echo "UNKNOWN: no GAIA-READY after 1200 s"; tail -30 "$SERIAL" || true; exit 3; }
	sleep 5
done
log "guest ready"
churn() { grep -a -o 'GAIA-CHURN bytes=[0-9]*' "$SERIAL" | tail -1 | cut -d= -f2; }

# --- repository, policy, VM --------------------------------------------------------------------------------------
section "repository and policy"
api repositories add name=main location="$SRV_HOME/repo" | tee "$OUT_DIR/repository.json" | jq -c .
repo_id=$(jq .id "$OUT_DIR/repository.json")
api policies add name=t15 repository_id="$repo_id" backup_interval_s="$INTERVAL_S" test_interval_s=86400 keep_last="$KEEP_LAST" \
	keep_daily=0 keep_weekly=0 rpo_target_s=$((2 * INTERVAL_S)) rto_target_s=1800 max_proof_age_s=172800 check_template=linux-basic |
	tee "$OUT_DIR/policy.json" | jq -c .
policy_id=$(jq .id "$OUT_DIR/policy.json")

gaia_checkpoints() { virsh -q checkpoint-list "$DOM" --name 2>/dev/null | grep '^gaia-' || true; }
is_ref() { [[ " $REF_CYCLES " == *" $1 "* ]]; }

# capture_refs N: the guest is paused; raw copy and sha256 of both disks.
capture_refs() {
	local n=$1 t0 d
	t0=$(date +%s.%N)
	: >"$WORK/ref/c$n.jsonl"
	for d in vda vdb; do
		local f=$VDA
		[[ $d == vdb ]] && f=$VDB
		qemu-img convert -U -q -f qcow2 -O raw "$f" "$WORK/ref/c$n-$d.raw"
		"$BIN/imgtool" hash -file "$WORK/ref/c$n-$d.raw" | jq -c --arg d "$d" '{disk: $d, sha256, bytes}' >>"$WORK/ref/c$n.jsonl"
		rm -f "$WORK/ref/c$n-$d.raw"
	done
	log "cycle $n: references captured while paused ($(awk -v a="$t0" -v b="$(date +%s.%N)" 'BEGIN { printf "%.1f", b - a }') s)"
}

# ss_capture N: the listeners while a backup's NBD export exists.
ss_capture() {
	local n=$1
	{
		echo "## cycle $n $(date -u +%H:%M:%S.%N)"
		echo "# ss -ltnp"
		ss -ltnp
		echo "# ss -xlp (unix listeners of qemu)"
		ss -xlp | grep -E 'qemu|gaia-' || true
		echo "# ss -xp (connected unix sockets: the NBD export and who holds the other end)"
		ss -xp | grep -E 'gaia-[0-9]+\.sock|sshd' | grep -v LISTEN || true
	} >>"$OUT_DIR/ss.txt"
	ss -ltnp | awk 'NR > 1' | grep -c qemu-system || true
}

last_job_id() { api jobs list vm_id="$vm_id" limit=1 | jq '.[0].id // 0'; }

# wait_job_after ID: the id of the first backup job of the VM newer than ID, once it exists (at most two intervals).
wait_job_after() {
	local prev=$1 id end=$(($(date +%s) + 2 * INTERVAL_S))
	while (($(date +%s) < end)); do
		id=$(last_job_id)
		((id > prev)) && { echo "$id"; return; }
		sleep 1
	done
	echo "UNKNOWN: no backup job after job $prev within $((2 * INTERVAL_S)) s: the scheduler did not run" >&2
	exit 3
}

: >"$OUT_DIR/cycles.jsonl"
: >"$OUT_DIR/restore-checks.jsonl"
SS_TCP_QEMU=0 SS_SEEN=0
prev_job=0
section "cycles"
for ((n = 1; n <= CYCLES; n++)); do
	ref=false
	if ((n == 1)); then
		if is_ref 1; then
			virsh -q suspend "$DOM"
			capture_refs 1
			ref=true
		fi
		# The VM gets its policy now; the scheduler queues the first backup within 15 s.
		api vms add host_id="$host_id" name="$DOM" policy_id="$policy_id" | tee "$OUT_DIR/vm.json" | jq -c .
		vm_id=$(jq .id "$OUT_DIR/vm.json")
	else
		last_created=$(api jobs get "$prev_job" | jq -r .created_at)
		due=$(($(date -d "$last_created" +%s) + INTERVAL_S))
		if is_ref "$n"; then
			sleep_s=$((due - REF_LEAD_S - $(date +%s)))
			((sleep_s <= 0)) || { log "cycle $n: pausing the guest in ${sleep_s} s (backup due $(date -u -d "@$due" +%H:%M:%S))"; sleep "$sleep_s"; }
			virsh -q suspend "$DOM"
			capture_refs "$n"
			ref=true
		else
			log "cycle $n: backup due $(date -u -d "@$due" +%H:%M:%S)"
		fi
	fi
	written=$(churn)
	job=$(wait_job_after "$prev_job")
	# Until the checkpoint exists (the point in time is fixed), or the job ended.
	for ((i = 0; i < 3000; i++)); do # 10 min
		api jobs get "$job" >"$WORK/job.json"
		[[ -n $(jq -r .checkpoint "$WORK/job.json") ]] && break
		[[ $(jq -r .state "$WORK/job.json") == running || $(jq -r .state "$WORK/job.json") == queued ]] || break
		sleep 0.2
	done
	if [[ $(jq -r .state "$WORK/job.json") == running ]]; then
		tcp=$(ss_capture "$n")
		SS_SEEN=$((SS_SEEN + 1))
		SS_TCP_QEMU=$((SS_TCP_QEMU + tcp))
	fi
	$ref && virsh -q resume "$DOM"
	end=$(($(date +%s) + 2 * INTERVAL_S))
	for ((;;)); do
		api jobs get "$job" >"$WORK/job.json"
		case $(jq -r .state "$WORK/job.json") in ok | failed) break ;; esac
		(($(date +%s) < end)) || { $ref && virsh -q resume "$DOM"; echo "UNKNOWN: job $job did not end within $((2 * INTERVAL_S)) s"; exit 3; }
		sleep 1
	done
	cp "$WORK/job.json" "$OUT_DIR/job-c$n.json"
	host_cps=$(gaia_checkpoints | paste -sd, -)
	jq -c --argjson n "$n" --argjson ref "$ref" --arg cps "$host_cps" --arg written "${written:-0}" \
		'{cycle: $n, job: .id, reason: .reason, state, error, created_at, finished_at, kind: .detail.kind,
		  backup_reason: .detail.reason, point: .detail.point, parent_point: .detail.parent_point, checkpoint,
		  disks: [.detail.disks[] | {target, size_bytes, changed_bytes, read_bytes, nbd_payload_bytes, repo_read_bytes, written_bytes}],
		  check_problems: .detail.check_problems, checkpoints_deleted: .detail.checkpoints_deleted,
		  points_pruned: .detail.points_pruned, maintenance: .detail.maintenance,
		  host_gaia_checkpoints: ($cps | split(",") | map(select(. != ""))), timings_s: .detail.timings_s,
		  reference: $ref, guest_workload_bytes_total: ($written | tonumber)}' "$WORK/job.json" | tee -a "$OUT_DIR/cycles.jsonl"
	prev_job=$job
	[[ $(jq -r .state "$WORK/job.json") == ok ]] || { log "cycle $n: backup job $job failed: $(jq -r .error "$WORK/job.json")"; continue; }
	point=$(jq .detail.point "$WORK/job.json")

	if $ref; then
		rc=0
		api points restore-check "$point" >"$OUT_DIR/restore-check-c$n.json" || rc=$?
		jq -c --argjson n "$n" --argjson p "$point" --argjson rc "$rc" --slurpfile r <(cat "$WORK/ref/c$n.jsonl") '
			(if $rc == 0 then . else [] end) as $got
			| {cycle: $n, point: $p, disks: [$r[] as $ref | ($got | map(select(.target == $ref.disk)) | first) as $g
			    | {disk: $ref.disk, reference_sha256: $ref.sha256, restored_sha256: $g.sha256, bytes: $g.bytes, seconds: $g.seconds,
			       identical: (if $g == null then null else $g.sha256 == $ref.sha256 end)}]}' \
			"$OUT_DIR/restore-check-c$n.json" | tee -a "$OUT_DIR/restore-checks.jsonl"
	fi

	if ((n == CKPT_DELETE_AFTER)); then
		cp=$(jq -r .checkpoint "$WORK/job.json")
		log "injection: deleting checkpoint $cp on the host (virsh checkpoint-delete)"
		virsh checkpoint-delete "$DOM" "$cp"
		echo "{\"after_cycle\": $n, \"injected\": \"checkpoint-delete\", \"checkpoint\": \"$cp\"}" >>"$OUT_DIR/injections.jsonl"
	fi
	if ((n == RESIZE_AFTER)); then
		old=$(virsh -q domblkinfo "$DOM" vdb | awk '/Capacity/ { print $2 }')
		log "injection: growing vdb from $old bytes by 1 GiB (virsh blockresize)"
		virsh blockresize "$DOM" vdb "$((old + 1073741824))B"
		echo "{\"after_cycle\": $n, \"injected\": \"blockresize\", \"disk\": \"vdb\", \"from\": $old, \"to\": $((old + 1073741824))}" >>"$OUT_DIR/injections.jsonl"
	fi
done
gaia_checkpoints | tee "$OUT_DIR/host-checkpoints-end.txt"

# --- verifications -----------------------------------------------------------------------------------------------
wait_verification() {
	local id=$1
	for ((;;)); do
		api verifications get "$id" >"$WORK/ver.json"
		[[ $(jq -r .state "$WORK/ver.json") == running ]] || break
		sleep 2
	done
	cat "$WORK/ver.json"
}
section "verifications"
api verifications list repository_id="$repo_id" >"$OUT_DIR/verifications-scheduled.json"
jq -c '.[] | {id, mode, state, reason, started_at, points_checked, blocks_read, bytes_read}' "$OUT_DIR/verifications-scheduled.json"
v=$(api repositories verify "$repo_id" mode=sampled | jq .id)
wait_verification "$v" | tee "$OUT_DIR/verification-sampled.json" | jq -c .

# The newest data pack (pack blobs are p*; q* hold metadata): one byte in its middle flipped, as a disk would.
pack=$(find "$SRV_HOME/repo" -type f -path "$SRV_HOME/repo/p*" -printf '%T@ %p\n' | sort -n | tail -1 | cut -d' ' -f2)
[[ -n "$pack" ]] || { echo "UNKNOWN: no data pack in the repository"; exit 3; }
ls -la --time-style=full-iso "$pack"
size=$(stat -c %s "$pack")
off=$((size / 2))
before=$(od -An -tx1 -j "$off" -N1 "$pack" | tr -d ' ')
printf "%b" "\\x$(printf %02x $((0x$before ^ 0xff)))" | dd of="$pack" bs=1 seek="$off" count=1 conv=notrunc status=none
after=$(od -An -tx1 -j "$off" -N1 "$pack" | tr -d ' ')
log "injection: pack ${pack#"$SRV_HOME"/repo/} ($size bytes) byte $off $before -> $after"
jq -nc --arg pack "${pack#"$SRV_HOME"/repo/}" --argjson size "$size" --argjson off "$off" --arg b "$before" --arg a "$after" \
	'{after_cycle: "last", injected: "pack byte flip", pack: $pack, pack_bytes: $size, offset: $off, before: $b, after: $a}' >>"$OUT_DIR/injections.jsonl"
newest_point=$(jq -s 'map(select(.state == "ok")) | last | .point' "$OUT_DIR/cycles.jsonl")
# Kopia's own view (independent of Gaia): which contents live in that pack.
blob=${pack#"$SRV_HOME"/repo/}
blob=${blob//\//}
blob=${blob%.f}
mkdir -p "$WORK/kopia-cli"
if KOPIA_PASSWORD="$(cat "$SRV_HOME/data/secrets/repository-main.password")" kopia --config-file="$WORK/kopia-cli/k.config" \
	repository connect filesystem --path="$SRV_HOME/repo" --cache-directory="$WORK/kopia-cli/cache" --readonly >"$OUT_DIR/kopia-connect.log" 2>&1; then
	KOPIA_PASSWORD="$(cat "$SRV_HOME/data/secrets/repository-main.password")" kopia --config-file="$WORK/kopia-cli/k.config" \
		content list --json 2>/dev/null | jq -c --arg b "$blob" '[.[] | select(.packFile == $b) | .contentID]' |
		tee "$OUT_DIR/corrupt-pack-contents.json" | jq -c '{contents_in_pack: length, first: .[0:5]}'
else
	tail -5 "$OUT_DIR/kopia-connect.log"
	echo "WARNING: kopia CLI could not open the repository; no cross-check" | tee "$OUT_DIR/corrupt-pack-contents.json"
fi
v=$(api repositories verify "$repo_id" mode=full | jq .id)
wait_verification "$v" | tee "$OUT_DIR/verification-full-corrupt.json" | jq -c .
api points list vm_id="$vm_id" limit=100 >"$OUT_DIR/points.json"
jq -c '.[] | {id, kind, state, check_state, checkpoint, parent_id, reason}' "$OUT_DIR/points.json"

section "state changes and audit"
api state-changes list limit=1000 >"$OUT_DIR/state-changes.json"
jq -r '.[] | [.at, .entity, .entity_id, .from, .to, .reason] | @tsv' "$OUT_DIR/state-changes.json" | tail -60
api audit verify | tee "$OUT_DIR/audit-verify.json" | jq -c .
du -sb "$SRV_HOME/repo" | tee "$OUT_DIR/repo-bytes.txt"

# --- gate --------------------------------------------------------------------------------------------------------
section "gate (CLAUDE.md T15)"
jq -n --slurpfile c "$OUT_DIR/cycles.jsonl" --slurpfile r "$OUT_DIR/restore-checks.jsonl" \
	--slurpfile vs "$OUT_DIR/verifications-scheduled.json" --slurpfile vf "$OUT_DIR/verification-full-corrupt.json" \
	--slurpfile vsa "$OUT_DIR/verification-sampled.json" --slurpfile pts "$OUT_DIR/points.json" \
	--argjson cycles "$CYCLES" --argjson del "$CKPT_DELETE_AFTER" --argjson rsz "$RESIZE_AFTER" --argjson interval "$INTERVAL_S" \
	--argjson ss_seen "$SS_SEEN" --argjson ss_tcp "$SS_TCP_QEMU" --argjson newest "$newest_point" '
	def ts: sub("\\.[0-9]+"; "") | sub("Z$"; "Z") | fromdateiso8601;
	($c | map(select(.state == "ok"))) as $ok
	| ($c | map(select(.reason | startswith("schedule")))) as $sched
	| ($c | map(select(.cycle == $del + 1)) | first) as $after_del
	| ($c | map(select(.cycle == $rsz + 1)) | first) as $after_rsz
	| ($vf[0].affected // []) as $aff
	| {
	  cycles_planned: $cycles, cycles_ok: ($ok | length), scheduled_jobs: ($sched | length),
	  schedule_gaps_s: [range(1; $c | length) as $i | (($c[$i].created_at | ts) - ($c[$i - 1].created_at | ts))],
	  a_sampled_points_identical: {
	    points: [$r[] | {cycle, point, disks: [.disks[] | {disk, identical}]}],
	    pass: ($r | length > 0 and all(.disks | length == 2 and all(.identical == true)))},
	  b1_checkpoint_deleted_full: {cycle: $after_del.cycle, kind: $after_del.kind, backup_reason: $after_del.backup_reason,
	    pass: ($after_del.kind == "full" and ($after_del.backup_reason | test("is not on the host")))},
	  b2_corruption_critical: {verification: $vf[0].id, state: $vf[0].state, affected: $aff, newest_point: $newest,
	    affected_points_failed: [$pts[0][] | select(.id as $id | $aff | any(.point_id == $id)) | {id, check_state}],
	    pass: ($vf[0].state == "critical" and ($aff | length > 0) and ($aff | any(.point_id == $newest))
	           and ([$pts[0][] | select(.id as $id | $aff | any(.point_id == $id)) | .check_state] | all(. == "failed")))},
	  b3_resize_full: {cycle: $after_rsz.cycle, kind: $after_rsz.kind, backup_reason: $after_rsz.backup_reason,
	    pass: ($after_rsz.kind == "full" and ($after_rsz.backup_reason | test("vdb was resized")))},
	  c_no_tcp_nbd_listener: {backups_caught_running: $ss_seen, qemu_tcp_listeners: $ss_tcp, pass: ($ss_seen > 0 and $ss_tcp == 0)},
	  also: {
	    incrementals_otherwise: [$ok[] | select(.cycle != 1 and .cycle != $del + 1 and .cycle != $rsz + 1) | .kind] | all(. == "incremental"),
	    max_gaia_checkpoints_on_host: ([$ok[] | .host_gaia_checkpoints | length] | max),
	    points_pruned: ([$ok[] | .points_pruned[]] | length),
	    scheduled_verifications: [$vs[0][] | {id, mode, state, reason, points_checked}],
	    sampled_verification_before_corruption: {state: $vsa[0].state, points_checked: $vsa[0].points_checked}}
	}
	| . + {verdict: (if .cycles_ok < 8 or (.a_sampled_points_identical.points | length) == 0 or .b1_checkpoint_deleted_full.cycle == null
	                    or .b3_resize_full.cycle == null or .c_no_tcp_nbd_listener.backups_caught_running == 0 then "UNKNOWN"
	                 elif .a_sampled_points_identical.pass and .b1_checkpoint_deleted_full.pass and .b2_corruption_critical.pass
	                    and .b3_resize_full.pass and .c_no_tcp_nbd_listener.pass then "OK"
	                 else "CRITICAL" end)}' | tee "$OUT_DIR/gate.json"

section "summary"
printf '%-5s %-5s %-11s %-6s %14s %14s %8s %s\n' cycle job kind state vda_changed vdb_changed total_s host_checkpoints
jq -r '[.cycle, .job, (.kind // "-"), .state, (.disks[0].changed_bytes // 0), (.disks[1].changed_bytes // 0),
	((.timings_s.total // 0) * 10 | round / 10), (.host_gaia_checkpoints | join(","))] | @tsv' "$OUT_DIR/cycles.jsonl" |
	awk -F'\t' '{ printf "%-5s %-5s %-11s %-6s %14s %14s %8s %s\n", $1, $2, $3, $4, $5, $6, $7, $8 }'

section "result"
verdict=$(jq -r .verdict "$OUT_DIR/gate.json")
echo "$verdict"
echo "(gate.json, cycles.jsonl, restore-checks.jsonl, ss.txt, verification-*.json, server.journal in $OUT_DIR)"
case $verdict in
OK) exit 0 ;;
CRITICAL) exit 2 ;;
*) exit 3 ;;
esac
