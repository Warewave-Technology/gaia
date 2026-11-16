#!/usr/bin/env bash
# Runs the T17 disaster-recovery drill (ADR-007 B5) from gaia-lab. gaia-lab is the KVM host of the protected VM and
# the operator's workstation (the gaia command, the saved recovery kit). The two Gaia servers are Proxmox VMs made
# by lab/proxmox/t17-servers.sh:
#
#   prepare      builds the M1 package (schema 4, $M1_REV) and this one (make dist, install guide step 2) and the
#                SSH key gaia-lab uses to reach the servers; prints the public key for t17-servers.sh create
#   part1 A_IP   server A, installed from the M1 guide on a fresh VM with its repository on its own disk, protects a
#                2-disk VM until it is OK; then it is upgraded to this version (guide step 12: the upgrade path from
#                schema 4), its first recovery kit is written and saved on the workstation (step 8b), and it keeps
#                backing up and testing; right after a backup ends, A loses power (sysrq o: no shutdown, no sync)
#   (Proxmox)    t17-servers.sh create B, move-disk A B, lose A, start B
#   part2 B_IP   server B, a fresh VM with A's repository disk: install guide step 13 with the saved kit only; then
#                the next backup, the next restore test, the ledger across the rebuild, and the kit opened with the
#                standalone age tool; writes gate.json
#   cleanup      removes the protected VM, the host user and the drill's directory (servers: t17-servers.sh lose)
#
# Usage (on gaia-lab, as root):  lab/lab-vm/t17-drill.sh prepare
#   systemd-run --unit=gaia-t17-1 --collect -E HOME=/root lab/lab-vm/t17-drill.sh part1 <A ip>
#   systemd-run --unit=gaia-t17-2 --collect -E HOME=/root lab/lab-vm/t17-drill.sh part2 <B ip>
# Env: INTERVAL_S (300: backups), TEST_INTERVAL_S (600), RPO_S (1200), RTO_S (600), MAX_PROOF_S (1800),
#      PHASE_TIMEOUT_S (2400), HOST_ADDR (gaia-lab's address).
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

INTERVAL_S="${INTERVAL_S:-300}"
TEST_INTERVAL_S="${TEST_INTERVAL_S:-600}"
RPO_S="${RPO_S:-1200}"
RTO_S="${RTO_S:-600}"
MAX_PROOF_S="${MAX_PROOF_S:-1800}"
PHASE_TIMEOUT_S="${PHASE_TIMEOUT_S:-2400}"
HOST_ADDR="${HOST_ADDR:-$(hostname -I | awk '{ print $1 }')}"
M1_REV=4d95f1a
D=/root/t17
OUT=$D/out
WS=$D/ws # the workstation: HOME of the gaia command, and where the kit is saved
KEY=$D/ssh/id_ed25519
DOM=gaia-t17-web
IMAGES=/var/lib/gaia/images
CLOUD_IMG=debian-12-genericcloud-amd64.qcow2
LV_IMAGES=/var/lib/libvirt/images
VDA="$LV_IMAGES/$DOM-vda.qcow2"
VDB="$LV_IMAGES/$DOM-vdb.qcow2"
SEED="$LV_IMAGES/$DOM-seed.iso"
SERIAL=/var/log/libvirt/qemu/$DOM-serial.log
HOST_USER=gaia-backup
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in go jq make qemu-img virsh cloud-localds ssh scp ssh-keygen script python3; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done

section() { printf '\n== %s\n' "$*"; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
stamp() { jq -nc --arg e "$1" '{event: $e, at: (now | todate)}' | tee -a "$OUT/events.jsonl"; }
# ws: the gaia command on the workstation (the CLI of the version the server runs).
ws() { HOME=$WS "$WS/bin/gaia" "$@"; }
api() { ws "$@" -json; }
sshopts=(-i "$KEY" -o UserKnownHostsFile="$D/ssh/known_hosts" -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=10)
# on IP: runs stdin as root on a server (the guide's "as root" steps).
on() { ssh "${sshopts[@]}" "gaia@$1" sudo bash -s; }
put() { scp -q "${sshopts[@]}" "$2" "gaia@$1:$3"; }
state() { printf '%s=%q\n' "$1" "$2" >>"$D/state.env"; }

pkg_of() { ls "$1"/dist/gaia-*-linux-amd64.tar.gz; }

# wait_ssh IP: until the server answers and cloud-init is done.
wait_ssh() {
	local i
	for ((i = 0; i < 60; i++)); do
		on "$1" <<<'cloud-init status --wait >/dev/null; true' 2>/dev/null && return 0
		sleep 5
	done
	echo "UNKNOWN: $1 does not answer over SSH"
	exit 3
}

# install_gaia IP PACKAGE: install guide step 3 (as root on the server), for the package given.
install_gaia() {
	local ip=$1 pkg=$2 v
	v=$(basename "$pkg" -linux-amd64.tar.gz)
	put "$ip" "$pkg" /tmp/
	on "$ip" <<EOF
set -euo pipefail
cd /tmp && tar xzf $v-linux-amd64.tar.gz && cd $v-linux-amd64 && sha256sum -c SHA256SUMS
id gaia >/dev/null 2>&1 || useradd --system --user-group --home-dir /var/lib/gaia --shell /usr/sbin/nologin gaia
install -d -o gaia -g gaia -m 0750 /var/lib/gaia /etc/gaia /srv/gaia
install -m 0755 gaia /usr/local/bin/gaia
install -m 0644 gaia.service /etc/systemd/system/gaia.service
DEBIAN_FRONTEND=noninteractive apt-get -qq update
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-clients dnsmasq-base iproute2 >/dev/null
install -m 0644 gaia-sandbox.service /etc/systemd/system/gaia-sandbox.service
EOF
}

# mount_repo IP FORMAT: the repository disk (Proxmox serial gaia-repo) on /srv/gaia; FORMAT=1 makes the file system.
mount_repo() {
	on "$1" <<EOF
set -euo pipefail
dev=/dev/\$(lsblk -dno NAME,SERIAL | awk '\$2 == "gaia-repo" { print \$1 }')
[[ "\$dev" != /dev/ ]] || { echo "no disk with serial gaia-repo"; lsblk; exit 1; }
if [[ "$2" == 1 ]]; then mkfs.ext4 -q -L gaia-repo "\$dev"; fi
mkdir -p /srv/gaia
grep -q gaia-repo /etc/fstab || echo 'LABEL=gaia-repo /srv/gaia ext4 defaults,nofail 0 2' >>/etc/fstab
systemctl daemon-reload
mount /srv/gaia
df -h /srv/gaia | tail -1
EOF
}

workstation_cli() { # PACKAGE: the gaia command of that version on the workstation (guide step 6)
	local v
	v=$(basename "$1" -linux-amd64.tar.gz)
	tar -C "$D" -xzf "$1"
	install -d "$WS/bin"
	install -m 0755 "$D/$v-linux-amd64/gaia" "$WS/bin/gaia"
}

# status_now / wait_status TARGET PHASE (as T16), and the jobs of the VM.
last_seen=""
status_now() { ws status "$vm_id" -json 2>/dev/null || true; }
wait_status() {
	local target=$1 phase=$2 t0 s st
	t0=$(date +%s)
	while (($(date +%s) - t0 < PHASE_TIMEOUT_S)); do
		s=$(status_now)
		st=$(jq -r .status <<<"$s" 2>/dev/null || echo "?")
		if [[ "$st" != "$last_seen" ]]; then
			log "status $st: $(jq -r '.reasons[0].text' <<<"$s" 2>/dev/null)"
			jq -c --arg phase "$phase" '{seen_at: (now | todate), phase: $phase, status, changed_at, reason: .reasons[0].text}' <<<"$s" >>"$OUT/timeline.jsonl" || true
			last_seen=$st
		fi
		[[ "$st" == "$target" ]] && return 0
		sleep 10
	done
	echo "UNKNOWN: phase $phase: status $last_seen, not $target, after $PHASE_TIMEOUT_S s"
	exit 3
}
# wait_jobs KIND N: until N jobs of KIND have ended (ok or failed) after job ID $since.
wait_jobs() {
	local kind=$1 n=$2 t0 got
	t0=$(date +%s)
	while (($(date +%s) - t0 < PHASE_TIMEOUT_S)); do
		got=$(api jobs list vm_id="$vm_id" limit=200 | jq --arg k "$kind" --argjson s "${since:-0}" '[.[] | select(.kind == $k and .id > $s and (.state == "ok" or .state == "failed"))] | length')
		((got >= n)) && return 0
		sleep 10
	done
	echo "UNKNOWN: fewer than $n $kind jobs ended after job ${since:-0} in $PHASE_TIMEOUT_S s"
	exit 3
}
newest_job() { api jobs list vm_id="$vm_id" limit=1 | jq '.[0].id // 0'; }

# guest: the protected VM on gaia-lab, two disks (vda system, vdb data mounted by fstab), writing a little all the time.
make_guest() {
	local work=$D/guest
	mkdir -p "$work"
	qemu-img convert -q -f qcow2 -O qcow2 "$IMAGES/$CLOUD_IMG" "$VDA"
	qemu-img resize -q "$VDA" 4G
	qemu-img create -q -f qcow2 "$VDB" 1G
	chown libvirt-qemu:libvirt-qemu "$VDA" "$VDB"
	printf 'instance-id: %s-%s\nlocal-hostname: %s\n' "$DOM" "$(date +%s)" "$DOM" >"$work/meta-data"
	cat >"$work/user-data" <<'EOF'
#cloud-config
write_files:
  - path: /usr/local/sbin/gaia-churn
    permissions: '0755'
    content: |
      #!/bin/bash
      set -eu
      blkid /dev/vdb >/dev/null 2>&1 || mkfs.ext4 -q -L gaia-data /dev/vdb
      grep -q gaia-data /etc/fstab || echo 'LABEL=gaia-data /data ext4 defaults 0 2' >> /etc/fstab
      mkdir -p /data && mount -a
      while true; do
        head -c 65536 /dev/urandom >> /data/app.log && sync /data/app.log
        [ "$(stat -c %s /data/app.log)" -lt 268435456 ] || mv /data/app.log /data/app.log.1
        sleep 2
      done
  - path: /etc/systemd/system/gaia-churn.service
    content: |
      [Unit]
      Description=Gaia T17 test workload
      [Service]
      ExecStart=/usr/local/sbin/gaia-churn
      [Install]
      WantedBy=multi-user.target
runcmd:
  - [systemctl, daemon-reload]
  - [systemctl, enable, --now, gaia-churn.service]
  - [sh, -c, 'echo GAIA-READY > /dev/ttyS0']
EOF
	cloud-localds "$SEED" "$work/user-data" "$work/meta-data"
	rm -f "$SERIAL"
	cat >"$work/$DOM.xml" <<EOF
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
	virsh -q define "$work/$DOM.xml" >/dev/null
	virsh -q start "$DOM"
	local i
	for ((i = 0; ; i++)); do
		grep -q GAIA-READY "$SERIAL" 2>/dev/null && break
		((i < 240)) || { echo "UNKNOWN: no GAIA-READY after 1200 s"; exit 3; }
		sleep 5
	done
	log "guest $DOM ready"
}

snapshot() { # NAME: what the server knows now, through the API
	local n=$1
	api status >"$OUT/$n-status.json" || true
	api points list vm_id="$vm_id" limit=100 >"$OUT/$n-points.json"
	api tests list vm_id="$vm_id" limit=100 >"$OUT/$n-tests.json"
	api jobs list vm_id="$vm_id" limit=200 >"$OUT/$n-jobs.json"
	api evidence list limit=100 >"$OUT/$n-evidence.json"
	api state-backups list limit=100 >"$OUT/$n-state-backups.json"
	api audit verify >"$OUT/$n-audit-verify.json"
	api state-changes list limit=1000 >"$OUT/$n-state-changes.json"
	local ev
	ev=$(jq '.[0].id // empty' "$OUT/$n-evidence.json")
	if [[ -n "$ev" ]]; then
		rm -rf "$OUT/$n-export" && mkdir -p "$OUT/$n-export"
		HOME=$WS "$WS/bin/gaia" evidence export "$ev" -o "$OUT/$n-export.tar.gz" >/dev/null
		tar -C "$OUT/$n-export" -xzf "$OUT/$n-export.tar.gz" && rm "$OUT/$n-export.tar.gz"
	fi
}

cmd=${1:-}
case "$cmd" in
prepare)
	rm -rf "$D" && install -d -m 0700 "$D" "$D/ssh" "$D/secrets" "$OUT" "$WS"
	section "packages (install guide step 2)"
	git -C "$repo_root" worktree remove --force /root/gaia-m1 2>/dev/null || true
	git -C "$repo_root" worktree add --detach /root/gaia-m1 "$M1_REV" >/dev/null
	rm -rf "$repo_root/dist"
	make -C /root/gaia-m1 dist | tail -1
	make -C "$repo_root" dist | tail -1
	ssh-keygen -q -t ed25519 -N '' -C "gaia-lab t17 drill" -f "$KEY"
	head -c 24 /dev/urandom | base64 >"$D/secrets/admin.pw"
	head -c 24 /dev/urandom | base64 >"$D/secrets/kit.pass"
	chmod 600 "$D/secrets/"*
	git -C "$repo_root" log --oneline -1 | tee "$OUT/revision.txt"
	echo "public key for t17-servers.sh create:"
	cat "$KEY.pub"
	;;

part1)
	A=${2:?A_IP}
	exec > >(tee -a "$OUT/part1.log") 2>&1
	: >"$D/state.env"
	state A "$A"
	OLD=$(pkg_of /root/gaia-m1)
	NEW=$(pkg_of "$repo_root")
	stamp part1-start
	section "server A ($A): M1 package $(basename "$OLD"), repository on its own disk"
	wait_ssh "$A"
	mount_repo "$A" 1
	install_gaia "$A" "$OLD"
	put "$A" "$D/secrets/admin.pw" /tmp/admin.pw
	# Guide step 4 (M1: no recovery kit yet), step 5.
	on "$A" <<EOF | tee "$OUT/a-init.txt"
set -euo pipefail
chown gaia /tmp/admin.pw
runuser -u gaia -- gaia server init -config /etc/gaia/server.json -data-dir /var/lib/gaia -hostname $A -admin admin -password-file /tmp/admin.pw -json
rm -f /tmp/admin.pw
systemctl daemon-reload && systemctl enable --now gaia-sandbox gaia
# M1's units start gaia before the helper has made its images directory (found in T17, fixed by Type=notify), so
# that directory stays read-only for the server: an M1 operator restarts gaia once the helper's socket is there.
for i in \$(seq 30); do [ -S /run/gaia-sandbox/sandbox.sock ] && break; sleep 1; done
systemctl restart gaia
EOF
	fp=$(jq -r '.result.tls_certificate_sha256' "$OUT/a-init.txt")
	workstation_cli "$OLD"
	sleep 3
	ws login -server "https://$A:8443" -user admin -fingerprint "$fp" -password-file "$D/secrets/admin.pw"
	api system | tee "$OUT/a-system-m1.json" | jq -c '{version, schema_version}'
	stamp a-installed-m1

	section "repository, host, VM (guide steps 7 and 10)"
	repo_id=$(api repositories add name=main location=/srv/gaia/main | jq .id)
	userdel -r "$HOST_USER" 2>/dev/null || true
	useradd -m -s /usr/sbin/nologin -G libvirt,libvirt-qemu "$HOST_USER"
	install -d -m 700 -o "$HOST_USER" -g "$HOST_USER" "/home/$HOST_USER/.ssh"
	jq -r .ssh_authorized_keys_line "$OUT/a-system-m1.json" >"/home/$HOST_USER/.ssh/authorized_keys"
	chown "$HOST_USER:" "/home/$HOST_USER/.ssh/authorized_keys" && chmod 600 "/home/$HOST_USER/.ssh/authorized_keys"
	host_fp=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $2 }')
	host_id=$(api hosts add name=gaia-lab address="$HOST_ADDR" ssh_user="$HOST_USER" | jq .id)
	[[ $(api hosts scan "$host_id" | jq -r .fingerprint) == "$host_fp" ]] || { echo "UNKNOWN: scanned fingerprint differs"; exit 3; }
	api hosts trust "$host_id" fingerprint="$host_fp" >/dev/null
	virsh -q destroy "$DOM" 2>/dev/null || true
	virsh -q undefine "$DOM" --checkpoints-metadata 2>/dev/null || true
	make_guest
	api hosts check "$host_id" | jq -c .
	policy_id=$(api policies add name=t17 repository_id="$repo_id" backup_interval_s="$INTERVAL_S" test_interval_s="$TEST_INTERVAL_S" keep_last=8 \
		keep_daily=0 keep_weekly=0 rpo_target_s="$RPO_S" rto_target_s="$RTO_S" max_proof_age_s="$MAX_PROOF_S" check_template=linux-boot | jq .id)
	vm_id=$(api vms add host_id="$host_id" name="$DOM" policy_id="$policy_id" | jq .id)
	state vm_id "$vm_id"
	wait_status OK "A on M1"
	since=0 wait_jobs backup 2
	stamp a-ok-on-m1
	snapshot a-m1

	section "upgrade A to this version (guide step 12), first recovery kit (step 8b)"
	install_gaia "$A" "$NEW" # unpacks the package and installs the program and the units
	on "$A" <<<'systemctl daemon-reload && systemctl try-restart gaia-sandbox && systemctl restart gaia && sleep 3 && systemctl is-active gaia-sandbox gaia && systemctl show -p Type gaia-sandbox'
	workstation_cli "$NEW"
	api system | tee "$OUT/a-system-upgraded.json" | jq -c '{version, schema_version, recovery_kit}'
	ws status | tee "$OUT/a-status-before-kit.txt" || true
	install -d -m 0700 "$WS/kits"
	(cd "$WS/kits" && ws kit regenerate -passphrase-file "$D/secrets/kit.pass" -json >"$OUT/a-kit-regenerate.json")
	(cd "$WS/kits" && ws kit save -passphrase-file "$D/secrets/kit.pass" | tee "$OUT/a-kit-save.txt")
	api kit status | tee "$OUT/a-kit-status.json" | jq -c .summary
	state KIT "$WS/kits/gaia-recovery-kit-$(jq .summary.generation "$OUT/a-kit-status.json").age"
	stamp a-upgraded-kit-saved

	section "A on this version: backups and restore tests go on"
	since=$(newest_job)
	wait_jobs backup 1
	wait_jobs restore_test 1
	wait_status OK "A upgraded"
	snapshot a-before-loss
	section "power cut right after the next backup ends"
	since=$(newest_job)
	wait_jobs backup 1
	on "$A" <<<'echo 1 >/proc/sys/kernel/sysrq; echo o >/proc/sysrq-trigger' || true
	stamp a-power-cut
	jq -c '[.[] | select(.kind == "backup")][0] | {id, state, finished_at}' <(api jobs list vm_id="$vm_id" limit=5 2>/dev/null || echo '[]') ||
		true
	log "A is off. Next, on Proxmox: t17-servers.sh create 9105 gaia-t17-b <keys>; move-disk 9104 9105; lose 9104; start 9105"
	;;

part2)
	B=${2:?B_IP}
	exec > >(tee -a "$OUT/part2.log") 2>&1
	# shellcheck disable=SC1091
	. "$D/state.env"
	state B "$B"
	NEW=$(pkg_of "$repo_root")
	stamp part2-start
	section "server B ($B): guide step 13 (install, repository, recovery kit)"
	wait_ssh "$B"
	install_gaia "$B" "$NEW"
	mount_repo "$B" 0
	on "$B" <<<'chown -R gaia:gaia /srv/gaia/main && ls -la /srv/gaia /srv/gaia/main | head -8'
	kit=$KIT # saved on the workstation in part 1
	put "$B" "$kit" /tmp/
	put "$B" "$D/secrets/kit.pass" /tmp/kit.pass
	kb=$(basename "$kit")
	on "$B" <<EOF | tee "$OUT/b-restore-state.json"
set -euo pipefail
chown gaia /tmp/$kb /tmp/kit.pass
runuser -u gaia -- gaia server restore-state -kit /tmp/$kb -config /etc/gaia/server.json -hostname $B -passphrase-file /tmp/kit.pass -json || echo "exit \$?" >&2
rm -f /tmp/kit.pass
EOF
	stamp b-restored
	fp=$(jq -r '.result.tls_certificate_sha256' "$OUT/b-restore-state.json")
	t_start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	on "$B" <<<'systemctl daemon-reload && systemctl enable --now gaia-sandbox gaia && sleep 3 && systemctl is-active gaia-sandbox gaia'
	ws login -server "https://$B:8443" -user admin -fingerprint "$fp" -password-file "$D/secrets/admin.pw"
	api system | tee "$OUT/b-system.json" | jq -c '{version, schema_version, recovery_kit}'
	ws status | tee "$OUT/b-status-after-restore.txt" || true
	api status >"$OUT/b-status-after-restore.json" || true
	(cd "$WS/kits" && ws kit save -passphrase-file "$D/secrets/kit.pass" | tee "$OUT/b-kit-save.txt")
	api kit status >"$OUT/b-kit-status.json"
	on "$B" <<EOF
rm -f /tmp/$kb
EOF
	snapshot b-restored
	stamp b-running

	# The schedule was overdue while no server ran: B queues the next backup and test itself.
	section "the next backup and restore test on B"
	since=$(api jobs list vm_id="$vm_id" limit=200 | jq --arg t "$t_start" '[.[] | select(.created_at < $t) | .id] | max // 0')
	state since_b "$since"
	wait_jobs backup 1
	api jobs list vm_id="$vm_id" limit=200 | jq --argjson s "$since" '[.[] | select(.kind == "backup" and .id > $s)] | last' >"$OUT/b-next-backup.json"
	jq -c '{id, state, error, reason: .detail.reason, parent: .detail.parent_point, checkpoint: .detail.checkpoint}' "$OUT/b-next-backup.json"
	wait_jobs restore_test 1
	wait_status OK "B after its first test"
	snapshot b-after
	on "$B" <<'EOF' >"$OUT/b-ledger-verify.json" || true
runuser -u gaia -- gaia evidence verify -ledger /var/lib/gaia/evidence/ledger.jsonl -evidence-dir /var/lib/gaia/evidence/bundles -pubkey /var/lib/gaia/keys/evidence-key.pub -json
EOF
	jq -c '{verdict, entries: .report.entries, intact: .report.intact, keys: .report.keys}' "$OUT/b-ledger-verify.json"
	stamp b-verified

	section "the saved kit, opened with the standalone age tool"
	command -v age >/dev/null || DEBIAN_FRONTEND=noninteractive apt-get install -y -qq age >/dev/null
	age --version | tee "$OUT/age-version.txt"
	plain=$(mktemp)
	akit=$KIT # the kit B was rebuilt from
	# age reads a passphrase only from a terminal: script(1) gives it one.
	{ sleep 2; cat "$D/secrets/kit.pass"; } | script -qec "age -d -o $plain $akit" /dev/null >/dev/null
	jq '{file: $f, format, generation, created_at, reason, hostname, evidence_key_id, state_repository,
		repositories: [.repositories[] | {name, kind, location, password: (if (.password | length) > 0 then "<present>" else "<missing>" end)}],
		evidence_private_key: (if (.evidence_private_key | startswith("-----BEGIN")) then "<PEM present>" else "<missing>" end),
		ssh_private_key: (if (.ssh_private_key | length) > 0 then "<present>" else "<missing>" end)}' --arg f "$(basename "$akit")" "$plain" |
		tee "$OUT/age-open.json"
	shred -u "$plain"

	section "gate (CLAUDE.md T17)"
	jq -n --slurpfile r "$OUT/b-restore-state.json" --slurpfile sb "$OUT/a-before-loss-status.json" --slurpfile sa "$OUT/b-status-after-restore.json" \
		--slurpfile pa "$OUT/a-before-loss-points.json" --slurpfile pb "$OUT/b-restored-points.json" --slurpfile nb "$OUT/b-next-backup.json" \
		--slurpfile lv "$OUT/b-ledger-verify.json" --slurpfile ao "$OUT/age-open.json" --slurpfile ks "$OUT/b-kit-status.json" \
		--rawfile la "$OUT/a-before-loss-export/$(ls "$OUT/a-before-loss-export")/ledger.jsonl" \
		--rawfile lb "$OUT/b-after-export/$(ls "$OUT/b-after-export")/ledger.jsonl" '
	($r[0].result) as $res
	| ($la | split("\n") | map(select(. != ""))) as $ledA
	| ($lb | split("\n") | map(select(. != ""))) as $ledB
	| ([($ledA | length), ($ledB | length)] | min) as $n
	| {
	  restore: {verdict: $r[0].verdict, problems: $res.problems, state_backup: $res.state_backup, catalog: $res.catalog,
	            evidence: $res.evidence, ledger: $res.ledger, kit_used: $res.recovery_kit_generation_used, kit_new: $res.recovery_kit_generation,
	            migrated: $res.migrated_versions, sessions_ended: $res.sessions_ended},
	  catalog_back: {points_on_a_at_last_look: ($pa[0] | length), points_restored: ($pb[0] | length),
	                 hosts: $res.catalog.hosts, vms: $res.catalog.vms, restore_tests: $res.catalog.restore_tests,
	                 status_before: [$sb[0][] | {vm, status, changed_at}], status_after: [$sa[0][] | {vm, status, changed_at}],
	                 same_status: ([$sb[0][] | {vm, status}] == [$sa[0][] | {vm, status}])},
	  next_backup: {id: $nb[0].id, state: $nb[0].state, kind: $nb[0].detail.kind, reason: $nb[0].detail.reason, parent_point: $nb[0].detail.parent_point,
	                checkpoint: $nb[0].detail.checkpoint, error: $nb[0].error},
	  ledger: {verdict: $lv[0].verdict, intact: $lv[0].report.intact, entries: $lv[0].report.entries, keys: $lv[0].report.keys,
	           entries_seen_on_a: ($ledA | length), entries_restored: $res.ledger.entries, entries_on_b: ($ledB | length),
	           a_and_b_agree: ($ledA[0:$n] == $ledB[0:$n])},
	  age: {opened: ($ao[0].format == "gaia-recovery-kit/1"), generation: $ao[0].generation},
	  kit_after: $ks[0].summary
	}
	| . + {pass: (.restore.verdict != "CRITICAL" and .catalog_back.same_status and .catalog_back.vms == 1 and .catalog_back.points_restored >= 1
	              and .next_backup.state == "ok" and .ledger.intact == true and .ledger.a_and_b_agree and .age.opened)}
	| . + {verdict: (if .pass then "OK" else "CRITICAL" end)}' | tee "$OUT/gate.json"
	stamp part2-end
	[[ $(jq -r .verdict "$OUT/gate.json") == OK ]] || exit 2
	;;

cleanup)
	virsh -q destroy "$DOM" 2>/dev/null || true
	virsh -q undefine "$DOM" --checkpoints-metadata 2>/dev/null || true
	rm -f "$VDA" "$VDB" "$SEED" "$SERIAL"
	pkill -u "$HOST_USER" 2>/dev/null || true
	userdel -r "$HOST_USER" 2>/dev/null || true
	git -C "$repo_root" worktree remove --force /root/gaia-m1 2>/dev/null || true
	rm -rf "$D"
	;;
*)
	sed -n '2,24p' "$0"
	exit 3
	;;
esac
