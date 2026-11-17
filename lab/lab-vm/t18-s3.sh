#!/usr/bin/env bash
# Runs T18 on gaia-lab: T15's scheduled-backup gate with the repository in S3 (MinIO), the quarantine of a damaged
# content (ADR-007 B3) and the backups after it, and the login protection.
#
# fixture: MinIO, built from source at a pinned commit (it publishes no binaries any more), runs on gaia-lab as its own
# system user under a transient unit, on HOST_ADDR:9000 with TLS: a CA made for this run signs its certificate, and
# Gaia is given that CA (s3_ca_file). The bucket gaia-t18 holds the repository under main/.
#
# Otherwise as T15 (lab/lab-vm/t15-scheduled.sh): gaia server on gaia-lab as its own user under the packaged unit; the
# KVM host is gaia-lab over SSH, as the host user gaia-t18-backup (not gaia-backup: that one belongs to server B, the
# M2 upgrade-path VM, whose backups go on during this run); a 2-disk guest with a write workload; the policy backs it
# up every INTERVAL_S seconds; in REF_CYCLES the guest is paused before the backup, both disks are captured as raw
# references, and the point is restored by the server and compared (T6 method).
# Injected failures:
#   after cycle CKPT_DELETE_AFTER: the newest Gaia checkpoint is deleted on the host -> the next backup is full, says why;
#   after cycle RESIZE_AFTER: vdb grows by 1 GiB -> the next backup is full and says why;
#   after cycle CORRUPT_AFTER: the VM's policy is detached (no backup during the checks), a sampled verification
#     (must be ok), then one byte is flipped on MinIO's own disk inside the oldest data pack (written by the first full
#     backup, so every later point shares its blocks) -> a full verification is critical, names the affected points,
#     quarantines the damaged content, and every affected VM must back up full next; the policy is attached again;
#   the cycles after it: the next backup is full with the quarantine as its reason, and its points (and the next
#     incremental's) restore byte-identical to the paused references; a last full verification is ok, and a forced
#     full maintenance runs with the quarantine in place.
# During every backup that can be caught running, `ss` is captured: no TCP listener may appear for NBD.
# Last, login protection against this server's API: wrong passwords for a user, for an unknown name, an admin
# unlock, and the per-address limit; every attempt must be in the audit log.
# Gates (CLAUDE.md T18): (a) after a damaged content is found, the next backup's points restore byte-identical,
# (b) the T15 gate on an S3 repository (>= 4 cycles before the corruption), (c) repeated bad logins are throttled
# and audited.
#
# Usage (on gaia-lab, as root, after setup.sh):
#   systemd-run --unit=gaia-t18 --collect -E HOME=/root lab/lab-vm/t18-s3.sh
# Env: CYCLES (6), INTERVAL_S (900), REF_CYCLES ("1 4 5 6"), REF_LEAD_S (90), CKPT_DELETE_AFTER (2), RESIZE_AFTER (3),
#      CORRUPT_AFTER (4), KEEP_LAST (8), DISK_GB (8), DATA_GB (4), HOST_ADDR (first address of `hostname -I`),
#      OUT_DIR (/root/t18-<UTC timestamp>), KEEP=1 keeps the server, MinIO and the guest.
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

CYCLES="${CYCLES:-6}"
INTERVAL_S="${INTERVAL_S:-900}"
REF_CYCLES="${REF_CYCLES:-1 4 5 6}"
REF_LEAD_S="${REF_LEAD_S:-90}"
CKPT_DELETE_AFTER="${CKPT_DELETE_AFTER:-2}"
RESIZE_AFTER="${RESIZE_AFTER:-3}"
CORRUPT_AFTER="${CORRUPT_AFTER:-4}"
KEEP_LAST="${KEEP_LAST:-8}"
DISK_GB="${DISK_GB:-8}"
DATA_GB="${DATA_GB:-4}"
HOST_ADDR="${HOST_ADDR:-$(hostname -I | awk '{ print $1 }')}"
OUT_DIR="${OUT_DIR:-/root/t18-$(date -u +%Y%m%dT%H%M%SZ)}"
MINIO_VERSION=v0.0.0-20260212201848-7aac2a2c5b7c # github.com/minio/minio at 7aac2a2c (2026-02-12), as scripts/test-minio.sh
DOM=gaia-t18
IMAGES=/var/lib/gaia/images
CLOUD_URL=https://cloud.debian.org/images/cloud/bookworm/latest
CLOUD_IMG=debian-12-genericcloud-amd64.qcow2
LV_IMAGES=/var/lib/libvirt/images
VDA="$LV_IMAGES/$DOM-vda.qcow2"
VDB="$LV_IMAGES/$DOM-vdb.qcow2"
SEED="$LV_IMAGES/$DOM-seed.iso"
SERIAL=/var/log/libvirt/qemu/$DOM-serial.log
SRV_USER=gaia-t18
SRV_HOME=/var/lib/gaia-t18
SRV_BIN=/usr/local/sbin/gaia-t18
UNIT=gaia-t18-server
HOST_USER=gaia-t18-backup
MINIO_USER=gaia-t18-minio
MINIO_HOME=/var/lib/gaia-t18-minio
MINIO_UNIT=gaia-t18-minio
MINIO_BIN=/var/lib/gaia/minio-$MINIO_VERSION/minio
S3_URL=https://$HOST_ADDR:9000
BUCKET=gaia-t18
API=https://127.0.0.1:8443
WORK=/var/lib/gaia/t18
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
[[ $(uname -m) == x86_64 ]] || { echo "UNKNOWN: T18 runs on gaia-lab (x86_64)"; exit 3; }
for bin in go jq make kopia qemu-img virsh cloud-localds curl openssl sha512sum fstrim ss ssh-keygen sshd; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done
mkdir -p "$OUT_DIR"
exec > >(tee "$OUT_DIR/t18.log") 2>&1
ln -sfn "$OUT_DIR" /root/t18-latest

section() { printf '\n== %s\n' "$*"; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
gaia() { "$BIN/gaia" "$@"; }
api() { gaia "$@" -json; }

cleanup() {
	[[ "${KEEP:-}" == 1 ]] && return 0
	virsh -q destroy "$DOM" >/dev/null 2>&1 || true
	virsh -q undefine "$DOM" --checkpoints-metadata >/dev/null 2>&1 || true
	rm -f "$VDA" "$VDB" "$SEED" "$LV_IMAGES"/*.gaia-*.scratch 2>/dev/null || true
	for u in "$UNIT" "$MINIO_UNIT"; do
		systemctl stop "$u" >/dev/null 2>&1 || true
		systemctl reset-failed "$u" >/dev/null 2>&1 || true
	done
	rm -f "/run/systemd/system/$UNIT.service"
	systemctl daemon-reload
	rm -rf "$SRV_HOME" "$MINIO_HOME" "$WORK" /root/.config/gaia
	userdel "$SRV_USER" >/dev/null 2>&1 || true
	userdel "$MINIO_USER" >/dev/null 2>&1 || true
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
		journalctl -u "$MINIO_UNIT" --no-pager -o short-iso-precise >"$OUT_DIR/minio.journal" 2>&1 || true
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
	"vdb resized after $RESIZE_AFTER; corruption after $CORRUPT_AFTER; keep_last $KEEP_LAST; host $HOST_USER@$HOST_ADDR; S3 $S3_URL/$BUCKET"

section "build"
owner=$(stat -c %U "$repo_root")
if [[ "$owner" == root ]]; then
	make -C "$repo_root" -s build
else
	runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" make -C "$repo_root" -s build
fi
go version -m "$BIN/gaia" | grep -E 'vcs\.(revision|modified)|kopia/kopia|minio-go|go-libvirt' || echo "no VCS stamp"
if [[ ! -x "$MINIO_BIN" ]]; then
	GOBIN=$(dirname "$MINIO_BIN") CGO_ENABLED=0 go install "github.com/minio/minio@$MINIO_VERSION"
fi
"$MINIO_BIN" --version | head -3
virsh -q version
[[ -e /dev/kvm ]] || { echo "UNKNOWN: /dev/kvm is missing in gaia-lab (see t6-kvm-backup.sh)"; exit 3; }

section "free space"
cleanup
mkdir -p "$IMAGES" "$WORK/ref"
# Two guest disks, one raw reference per disk at a time, the repository in MinIO, a restore-check of the largest disk.
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

# --- MinIO (fixture) ---------------------------------------------------------------------------------------------
section "MinIO on $S3_URL (TLS, CA of this run)"
useradd --system --home-dir "$MINIO_HOME" --shell /usr/sbin/nologin "$MINIO_USER"
install -d -m 0750 -o "$MINIO_USER" -g "$MINIO_USER" "$MINIO_HOME" "$MINIO_HOME/data" "$MINIO_HOME/certs"
ca=$MINIO_HOME/ca
install -d -m 0700 "$ca"
openssl ecparam -genkey -name prime256v1 -noout -out "$ca/ca.key" 2>/dev/null
openssl req -x509 -new -key "$ca/ca.key" -sha256 -days 7 -subj "/CN=gaia-t18 lab CA" -out "$ca/ca.crt"
openssl ecparam -genkey -name prime256v1 -noout -out "$ca/server.key" 2>/dev/null
openssl req -new -key "$ca/server.key" -subj "/CN=$HOST_ADDR" -out "$ca/server.csr"
printf 'subjectAltName=IP:%s\nextendedKeyUsage=serverAuth\n' "$HOST_ADDR" >"$ca/ext"
openssl x509 -req -in "$ca/server.csr" -CA "$ca/ca.crt" -CAkey "$ca/ca.key" -CAcreateserial -days 7 -sha256 -extfile "$ca/ext" -out "$ca/server.crt" 2>/dev/null
install -m 0644 -o "$MINIO_USER" "$ca/server.crt" "$MINIO_HOME/certs/public.crt"
install -m 0600 -o "$MINIO_USER" "$ca/server.key" "$MINIO_HOME/certs/private.key"
cp "$ca/ca.crt" "$OUT_DIR/s3-ca.crt"
openssl x509 -in "$ca/server.crt" -noout -subject -issuer -ext subjectAltName | tee "$OUT_DIR/s3-cert.txt"
S3_KEY=gaia-t18
S3_SECRET=$(head -c 24 /dev/urandom | base64 | tr -d '/+=')
printf 'MINIO_ROOT_USER=%s\nMINIO_ROOT_PASSWORD=%s\n' "$S3_KEY" "$S3_SECRET" | install -m 0600 /dev/stdin "$ca/minio.env"
systemd-run --unit="$MINIO_UNIT" --uid="$MINIO_USER" --gid="$MINIO_USER" -p EnvironmentFile="$ca/minio.env" \
	"$MINIO_BIN" server --address "$HOST_ADDR:9000" --console-address 127.0.0.1:9001 --certs-dir "$MINIO_HOME/certs" "$MINIO_HOME/data"
for ((i = 0; i < 60; i++)); do
	curl -fs --cacert "$ca/ca.crt" "$S3_URL/minio/health/live" >/dev/null && break
	sleep 1
done
curl -fsS --cacert "$ca/ca.crt" "$S3_URL/minio/health/live" -o /dev/null -w 'MinIO health: %{http_code}\n'
curl -fsS --cacert "$ca/ca.crt" --aws-sigv4 "aws:amz:us-east-1:s3" --user "$S3_KEY:$S3_SECRET" -X PUT "$S3_URL/$BUCKET" -o /dev/null \
	-w "bucket $BUCKET: %{http_code}\n"

# --- gaia server, installed as docs/install.md says, under the packaged unit -----------------------------------
section "gaia server"
useradd --system --home-dir "$SRV_HOME" --shell /usr/sbin/nologin "$SRV_USER"
install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME" "$SRV_HOME/data"
install -m 0755 "$BIN/gaia" "$SRV_BIN"
pwfile="$SRV_HOME/admin.pw"
kitfile="$SRV_HOME/kit.pass"
head -c 24 /dev/urandom | base64 | install -m 0600 -o "$SRV_USER" -g "$SRV_USER" /dev/stdin "$pwfile"
head -c 24 /dev/urandom | base64 | install -m 0600 -o "$SRV_USER" -g "$SRV_USER" /dev/stdin "$kitfile"
runuser -u "$SRV_USER" -- "$SRV_BIN" server init -config "$SRV_HOME/server.json" -data-dir "$SRV_HOME/data" \
	-listen 127.0.0.1:8443 -hostname 127.0.0.1 -admin admin -password-file "$pwfile" -kit-passphrase-file "$kitfile" -json |
	tee "$OUT_DIR/server-init.json" | jq -c '{verdict, result: (.result | {tls_certificate_sha256, evidence_key_id})}'
cert=$(jq -r .result.tls_certificate_sha256 "$OUT_DIR/server-init.json")
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
api system | tee "$OUT_DIR/system.json" | jq -c '{version, schema_version, migrations: [.migrations[].name]}'

# --- the KVM host side, as docs/install.md step 10 says ---------------------------------------------------------
section "host: user $HOST_USER, authorized_keys, host key"
useradd -m -s /usr/sbin/nologin -G libvirt,libvirt-qemu "$HOST_USER"
install -d -m 700 -o "$HOST_USER" -g "$HOST_USER" "/home/$HOST_USER/.ssh"
jq -r .ssh_authorized_keys_line "$OUT_DIR/system.json" >"/home/$HOST_USER/.ssh/authorized_keys"
chown "$HOST_USER:" "/home/$HOST_USER/.ssh/authorized_keys"
chmod 600 "/home/$HOST_USER/.ssh/authorized_keys"
host_fp=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $2 }')
host_id=$(api hosts add name=gaia-lab address="$HOST_ADDR" ssh_user="$HOST_USER" | jq .id)
[[ $(api hosts scan "$host_id" | jq -r .fingerprint) == "$host_fp" ]] || { echo "UNKNOWN: scanned fingerprint differs from the host's"; exit 3; }
api hosts trust "$host_id" fingerprint="$host_fp" | jq -c '{id, name, ssh_host_key}'
api hosts check "$host_id" | tee "$OUT_DIR/host-check.json" | jq -c .

# --- the guest (as T15) ------------------------------------------------------------------------------------------
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
      Description=Gaia T18 test workload
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

# --- repository (S3, guide step 7b), policy, VM ------------------------------------------------------------------
section "repository in S3 and policy"
GAIA_S3_SECRET_ACCESS_KEY=$S3_SECRET GAIA_KIT_PASSPHRASE=$(cat "$kitfile") \
	api repositories add name=main kind=s3 location="$S3_URL/$BUCKET/main" s3_access_key_id="$S3_KEY" s3_ca_file="$ca/ca.crt" |
	tee "$OUT_DIR/repository.json" | jq -c .
repo_id=$(jq .id "$OUT_DIR/repository.json")
# exit 1 (WARNING) until a kit is saved off the server; this run does not save one
api kit status >"$OUT_DIR/kit-status.json" || true
jq -c .summary "$OUT_DIR/kit-status.json" || cat "$OUT_DIR/kit-status.json"
grep -c "$S3_SECRET" "$SRV_HOME/data/secrets/repository-main.s3.json" >/dev/null && echo "access key stored in secrets/repository-main.s3.json ($(stat -c %a "$SRV_HOME/data/secrets/repository-main.s3.json"))"
api policies add name=t18 repository_id="$repo_id" backup_interval_s="$INTERVAL_S" test_interval_s=86400 keep_last="$KEEP_LAST" \
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
		ss -xp | grep -E 'gaia-[0-9]+-[0-9]+\.sock|gaia-[0-9]+\.sock|sshd' | grep -v LISTEN || true
	} >>"$OUT_DIR/ss.txt"
	ss -ltnp | awk 'NR > 1' | grep -c qemu-system || true
}

last_job_id() { api jobs list vm_id="$vm_id" limit=20 | jq '[.[] | select(.kind == "backup")][0].id // 0'; }

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

wait_verification() {
	local id=$1
	for ((;;)); do
		api verifications get "$id" >"$WORK/ver.json"
		[[ $(jq -r .state "$WORK/ver.json") == running ]] || break
		sleep 2
	done
	cat "$WORK/ver.json"
}

# corrupt: the policy is detached, a sampled verification, one byte flipped on MinIO's disk in the oldest data pack,
# a full verification; then the policy is attached again by the next cycle.
corrupt() {
	section "verifications and the damaged content (after cycle $1)"
	api vms set "$vm_id" policy_id=0 | jq -c '{id, name, policy_id}'
	detached=true
	api verifications list repository_id="$repo_id" >"$OUT_DIR/verifications-before.json"
	local v pdir part size off before after blob
	v=$(api repositories verify "$repo_id" mode=sampled | jq .id)
	wait_verification "$v" | tee "$OUT_DIR/verification-sampled.json" | jq -c '{id, mode, state, points_checked, blocks_read, error}'
	# MinIO keeps an object as data/<bucket>/<key>/xl.meta, and a large one's bytes in <uuid>/part.1 beside it: the
	# oldest data pack (p...) with a part file of at least 1 MiB.
	part=""
	while read -r _ pdir; do
		part=$(find "$pdir" -name part.1 -type f -size +1M | head -1)
		[[ -n "$part" ]] && break
	done < <(find "$MINIO_HOME/data/$BUCKET/main" -mindepth 1 -maxdepth 1 -type d -name 'p*' -printf '%T@ %p\n' | sort -n)
	[[ -n "$part" ]] || { echo "UNKNOWN: no data pack with a part file in MinIO"; exit 3; }
	size=$(stat -c %s "$part")
	off=$((size / 2))
	before=$(od -An -tx1 -j "$off" -N1 "$part" | tr -d ' ')
	printf "%b" "\\x$(printf %02x $((0x$before ^ 0xff)))" | dd of="$part" bs=1 seek="$off" count=1 conv=notrunc status=none
	after=$(od -An -tx1 -j "$off" -N1 "$part" | tr -d ' ')
	blob=$(basename "$pdir")
	log "injection: MinIO object main/$blob, file ${part#"$MINIO_HOME"/data/} ($size bytes), byte $off $before -> $after"
	jq -nc --arg blob "$blob" --arg file "${part#"$MINIO_HOME"/data/}" --argjson size "$size" --argjson off "$off" --arg b "$before" --arg a "$after" \
		--argjson c "$1" '{after_cycle: $c, injected: "byte flip on the object storage disk", pack: $blob, file: $file, file_bytes: $size,
		offset: $off, before: $b, after: $a}' >>"$OUT_DIR/injections.jsonl"
	newest_point=$(jq -s 'map(select(.state == "ok")) | last | .point' "$OUT_DIR/cycles.jsonl")
	# Kopia's own view (independent of Gaia): which contents live in that pack.
	mkdir -p "$WORK/kopia-cli"
	if KOPIA_PASSWORD="$(cat "$SRV_HOME/data/secrets/repository-main.password")" kopia --config-file="$WORK/kopia-cli/k.config" \
		repository connect s3 --bucket="$BUCKET" --prefix=main/ --endpoint="$HOST_ADDR:9000" --access-key="$S3_KEY" \
		--secret-access-key="$S3_SECRET" --root-ca-pem-path="$ca/ca.crt" --cache-directory="$WORK/kopia-cli/cache" --readonly \
		>"$OUT_DIR/kopia-connect.log" 2>&1; then
		KOPIA_PASSWORD="$(cat "$SRV_HOME/data/secrets/repository-main.password")" kopia --config-file="$WORK/kopia-cli/k.config" \
			content list --json 2>/dev/null | jq -c --arg b "$blob" '[.[] | select(.packFile == $b) | .contentID]' |
			tee "$OUT_DIR/corrupt-pack-contents.json" | jq -c '{contents_in_pack: length, first: .[0:5]}'
	else
		tail -5 "$OUT_DIR/kopia-connect.log"
		echo "WARNING: kopia CLI could not open the repository; no cross-check" | tee "$OUT_DIR/corrupt-pack-contents.json"
	fi
	v=$(api repositories verify "$repo_id" mode=full | jq .id)
	wait_verification "$v" | tee "$OUT_DIR/verification-full-corrupt.json" | jq -c '{id, mode, state, points_checked, points_skipped, blocks_read, error,
		affected: [.affected[] | {point_id, disk, contents, problem: .problems[0][0:200]}]}'
	api quarantine list repository_id="$repo_id" | tee "$OUT_DIR/quarantine.json" | jq -c '.[] | {content_id, pack, reason: .reason[0:160], points, verification_id}'
	api vms get "$vm_id" | tee "$OUT_DIR/vm-after-quarantine.json" | jq -c '{id, name, full_backup_reason}'
	api audit list limit=1000 | jq -c '[.[] | select(.action == "repository quarantine")]' | tee "$OUT_DIR/audit-quarantine.json" | jq -c '.[] | {seq, at, username, target, detail: .detail[0:200]}'
	api points list vm_id="$vm_id" limit=100 >"$OUT_DIR/points-after-quarantine.json"
	jq -c '.[] | {id, kind, check_state}' "$OUT_DIR/points-after-quarantine.json"
}

: >"$OUT_DIR/cycles.jsonl"
: >"$OUT_DIR/restore-checks.jsonl"
: >"$OUT_DIR/injections.jsonl"
SS_TCP_QEMU=0 SS_SEEN=0
prev_job=0
detached=false
newest_point=null
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
		if $detached; then # after the corruption checks: the policy comes back once the guest is paused
			api vms set "$vm_id" policy_id="$policy_id" | jq -c '{id, name, policy_id, full_backup_reason}'
			detached=false
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
		(($(date +%s) < end)) || { echo "UNKNOWN: job $job did not end within $((2 * INTERVAL_S)) s"; exit 3; }
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
	if ((n == CORRUPT_AFTER)); then
		corrupt "$n"
	fi
done
gaia_checkpoints | tee "$OUT_DIR/host-checkpoints-end.txt"

section "after the quarantine: a full verification, a forced full maintenance"
v=$(api repositories verify "$repo_id" mode=full | jq .id)
wait_verification "$v" | tee "$OUT_DIR/verification-full-end.json" | jq -c '{id, mode, state, points_checked, points_skipped, blocks_read, error}'
rc=0
api repositories maintenance "$repo_id" force=true >"$OUT_DIR/maintenance-forced.json" 2>&1 || rc=$?
jq -c --argjson rc "$rc" '{rc: $rc, mode, referenced_contents, quarantined_contents, unresolved_points, seconds,
	gc: (.gc | {InUseContentCount, DeletedContentCount, RecoveredContentCount})}' "$OUT_DIR/maintenance-forced.json" 2>/dev/null ||
	{ echo "maintenance: exit $rc"; cat "$OUT_DIR/maintenance-forced.json"; }
echo "{\"rc\": $rc}" >"$OUT_DIR/maintenance-forced-rc.json"
api points list vm_id="$vm_id" limit=100 >"$OUT_DIR/points.json"
jq -c '.[] | {id, kind, state, check_state, checkpoint, parent_id, reason}' "$OUT_DIR/points.json"
api vms get "$vm_id" | tee "$OUT_DIR/vm-end.json" | jq -c '{id, name, full_backup_reason}'

# --- login protection (gate c) ------------------------------------------------------------------------------------
section "login protection"
op_pw=$(head -c 18 /dev/urandom | base64)
GAIA_PASSWORD=$op_pw api users add username=t18op role=operator | tee "$OUT_DIR/login-user.json" | jq -c '{id, username, role}'
op_id=$(jq .id "$OUT_DIR/login-user.json")
admin_pw=$(cat "$pwfile")
login_from=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
login_after=$(api audit verify | jq .entries) # the audit log is read oldest first, at most 1000 entries a page
: >"$OUT_DIR/logins.jsonl"
attempt() { # STEP USER PASSWORD-KIND
	local step=$1 user=$2 kind=$3 pw code
	case $kind in right) pw=$op_pw ;; admin) pw=$admin_pw ;; *) pw="wrong-$RANDOM-$RANDOM" ;; esac
	code=$(jq -nc --arg u "$user" --arg p "$pw" '{username: $u, password: $p}' |
		curl -sk -o "$WORK/login-body.json" -D "$WORK/login-head.txt" -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$API/api/v1/login")
	jq -nc --arg step "$step" --arg user "$user" --arg kind "$kind" --argjson code "$code" \
		--arg retry "$(awk 'tolower($1) == "retry-after:" { print $2 }' "$WORK/login-head.txt" | tr -d '\r')" \
		--slurpfile body "$WORK/login-body.json" \
		'{step: $step, user: $user, password: $kind, status: $code, retry_after: $retry, error: ($body[0].error // null), at: (now | todate)}' |
		tee -a "$OUT_DIR/logins.jsonl"
}
for i in 1 2 3 4 5; do attempt "user-wrong-$i" t18op wrong; done
attempt user-right-while-locked t18op right
for i in 1 2 3 4 5; do attempt "unknown-wrong-$i" nobody-t18 wrong; done
attempt unknown-while-locked nobody-t18 wrong
api users unlock "$op_id" | jq -c '{id, username, failed_logins, locked_until}'
attempt user-right-after-unlock t18op right
for i in $(seq 1 10); do attempt "address-wrong-$i" "nobody-$i" wrong; done
attempt address-admin-right admin admin
api audit list after="$login_after" limit=1000 | jq -c --arg from "$login_from" '[.[] | select(.action == "POST /api/v1/login" and .at >= $from)
	| {seq, at, username, target, outcome, remote_addr, detail}]' >"$OUT_DIR/audit-logins.json"
jq -r '.[] | [.seq, .at, .target, .outcome, .detail] | @tsv' "$OUT_DIR/audit-logins.json"
api state-changes list entity=user_lockout entity_id="$op_id" limit=100 | tee "$OUT_DIR/lockout-changes.json" | jq -c '.[] | {at, entity_id, from, to, reason, actor}'
api audit verify | tee "$OUT_DIR/audit-verify.json" | jq -c .
api state-changes list limit=1000 >"$OUT_DIR/state-changes.json"

# --- gate --------------------------------------------------------------------------------------------------------
section "gate (CLAUDE.md T18)"
jq -n --slurpfile c "$OUT_DIR/cycles.jsonl" --slurpfile r "$OUT_DIR/restore-checks.jsonl" \
	--slurpfile vf "$OUT_DIR/verification-full-corrupt.json" --slurpfile vsa "$OUT_DIR/verification-sampled.json" \
	--slurpfile ve "$OUT_DIR/verification-full-end.json" --slurpfile pq "$OUT_DIR/points-after-quarantine.json" \
	--slurpfile q "$OUT_DIR/quarantine.json" --slurpfile aq "$OUT_DIR/audit-quarantine.json" --slurpfile m "$OUT_DIR/maintenance-forced-rc.json" \
	--slurpfile lg <(jq -s . "$OUT_DIR/logins.jsonl") --slurpfile al "$OUT_DIR/audit-logins.json" --slurpfile lc "$OUT_DIR/lockout-changes.json" \
	--argjson del "$CKPT_DELETE_AFTER" --argjson rsz "$RESIZE_AFTER" --argjson cor "$CORRUPT_AFTER" --argjson interval "$INTERVAL_S" \
	--argjson ss_seen "$SS_SEEN" --argjson ss_tcp "$SS_TCP_QEMU" --argjson newest "$newest_point" --argjson op "$op_id" '
	def ts: sub("\\.[0-9]+"; "") | sub("Z$"; "Z") | fromdateiso8601;
	def step($s): $lg[0] | map(select(.step == $s)) | first;
	($c | map(select(.cycle <= $cor))) as $before
	| ($before | map(select(.state == "ok"))) as $ok
	| ($c | map(select(.cycle == $del + 1)) | first) as $after_del
	| ($c | map(select(.cycle == $rsz + 1)) | first) as $after_rsz
	| ($c | map(select(.cycle == $cor + 1)) | first) as $after_cor
	| ($c | map(select(.cycle > $cor + 1 and .state == "ok"))) as $later
	| ($vf[0].affected // []) as $aff
	| ($r | map(select(.cycle <= $cor))) as $r_before
	| ($r | map(select(.cycle > $cor))) as $r_after
	| {
	  b_t15_on_s3: {
	    cycles_before_corruption: ($before | length), cycles_ok: ($ok | length),
	    schedule_gaps_s: [range(1; $before | length) as $i | (($before[$i].created_at | ts) - ($before[$i - 1].created_at | ts))],
	    a_sampled_points_identical: {points: [$r_before[] | {cycle, point, disks: [.disks[] | {disk, identical}]}],
	      pass: ($r_before | length > 0 and all(.disks | length == 2 and all(.identical == true)))},
	    b1_checkpoint_deleted_full: {cycle: $after_del.cycle, kind: $after_del.kind, backup_reason: $after_del.backup_reason,
	      pass: ($after_del.kind == "full" and ($after_del.backup_reason | test("is not on the host")))},
	    b2_corruption_critical: {verification: $vf[0].id, state: $vf[0].state, newest_point: $newest,
	      affected: [$aff[] | {point_id, disk, contents}],
	      affected_points_failed: [$pq[0][] | select(.id as $id | $aff | any(.point_id == $id)) | {id, check_state}],
	      pass: ($vf[0].state == "critical" and ($aff | length > 0)
	             and ([$pq[0][] | select(.id as $id | $aff | any(.point_id == $id)) | .check_state] | length > 0 and all(. == "failed")))},
	    b3_resize_full: {cycle: $after_rsz.cycle, kind: $after_rsz.kind, backup_reason: $after_rsz.backup_reason,
	      pass: ($after_rsz.kind == "full" and ($after_rsz.backup_reason | test("vdb was resized")))},
	    c_no_tcp_nbd_listener: {backups_caught_running: $ss_seen, qemu_tcp_listeners: $ss_tcp, pass: ($ss_seen > 0 and $ss_tcp == 0)},
	    sampled_verification_before_corruption: {state: $vsa[0].state, points_checked: $vsa[0].points_checked}},
	  a_after_damage: {
	    quarantined: [$q[0][] | {content_id, pack, points, reason: .reason[0:120]}],
	    audit_entries: ($aq[0] | length),
	    next_backup: {cycle: $after_cor.cycle, kind: $after_cor.kind, backup_reason: $after_cor.backup_reason, state: $after_cor.state},
	    later_backups: [$later[] | {cycle, kind, state}],
	    points_identical: [$r_after[] | {cycle, point, disks: [.disks[] | {disk, identical}]}],
	    last_full_verification: {state: $ve[0].state, points_checked: $ve[0].points_checked, points_skipped: $ve[0].points_skipped},
	    forced_maintenance_rc: $m[0].rc,
	    pass: (($q[0] | length) > 0 and ($aq[0] | length) > 0 and $after_cor.state == "ok" and $after_cor.kind == "full"
	           and ($after_cor.backup_reason | test("quarantined damaged contents"))
	           and ($r_after | length >= 1 and any(.cycle == $cor + 1) and all(.disks | length == 2 and all(.identical == true)))
	           and $ve[0].state == "ok" and $m[0].rc == 0)},
	  c_logins: {
	    attempts: ($lg[0] | length), audited: ($al[0] | length),
	    user_wrong: [range(1; 6) as $i | step("user-wrong-\($i)").status],
	    user_right_while_locked: step("user-right-while-locked") | {status, retry_after, error},
	    unknown_wrong: [range(1; 6) as $i | step("unknown-wrong-\($i)").status],
	    unknown_while_locked: step("unknown-while-locked") | {status, retry_after, error},
	    user_right_after_unlock: step("user-right-after-unlock").status,
	    address_admin_right: step("address-admin-right") | {status, retry_after, error},
	    lockout_changes: [$lc[0][] | select(.entity_id == $op) | {from, to, reason, actor}],
	    pass: ((step("user-wrong-5").status == 401) and ([range(1; 6) as $i | step("user-wrong-\($i)").status] | all(. == 401))
	           and step("user-right-while-locked").status == 429 and step("user-right-while-locked").retry_after != ""
	           and ([range(1; 6) as $i | step("unknown-wrong-\($i)").status] | all(. == 401)) and step("unknown-while-locked").status == 429
	           and step("user-right-after-unlock").status == 200
	           and step("address-admin-right").status == 429 and (step("address-admin-right").error | test("from 127.0.0.1"))
	           and ($al[0] | length) == ($lg[0] | length)
	           and ([$lc[0][] | select(.entity_id == $op) | .to] | any(startswith("locked until")) and any(. == "unlocked")))}
	}
	| . + {verdict: (if .b_t15_on_s3.cycles_ok < 4 or (.b_t15_on_s3.a_sampled_points_identical.points | length) == 0
	                    or .b_t15_on_s3.b1_checkpoint_deleted_full.cycle == null or .b_t15_on_s3.b3_resize_full.cycle == null
	                    or .b_t15_on_s3.c_no_tcp_nbd_listener.backups_caught_running == 0 or .a_after_damage.next_backup.cycle == null then "UNKNOWN"
	                 elif .b_t15_on_s3.a_sampled_points_identical.pass and .b_t15_on_s3.b1_checkpoint_deleted_full.pass
	                    and .b_t15_on_s3.b2_corruption_critical.pass and .b_t15_on_s3.b3_resize_full.pass
	                    and .b_t15_on_s3.c_no_tcp_nbd_listener.pass and .a_after_damage.pass and .c_logins.pass then "OK"
	                 else "CRITICAL" end)}' | tee "$OUT_DIR/gate.json"

section "summary"
printf '%-5s %-5s %-11s %-6s %14s %14s %8s %s\n' cycle job kind state vda_changed vdb_changed total_s host_checkpoints
jq -r '[.cycle, .job, (.kind // "-"), .state, (.disks[0].changed_bytes // 0), (.disks[1].changed_bytes // 0),
	((.timings_s.total // 0) * 10 | round / 10), (.host_gaia_checkpoints | join(","))] | @tsv' "$OUT_DIR/cycles.jsonl" |
	awk -F'\t' '{ printf "%-5s %-5s %-11s %-6s %14s %14s %8s %s\n", $1, $2, $3, $4, $5, $6, $7, $8 }'

section "result"
verdict=$(jq -r .verdict "$OUT_DIR/gate.json")
echo "$verdict"
echo "(gate.json, cycles.jsonl, restore-checks.jsonl, logins.jsonl, quarantine.json, verification-*.json, ss.txt, server.journal in $OUT_DIR)"
case $verdict in
OK) exit 0 ;;
CRITICAL) exit 2 ;;
*) exit 3 ;;
esac
