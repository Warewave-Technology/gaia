#!/usr/bin/env bash
# T29: the upgrade from the release 0.1.2 to 0.2.0 on a clean Debian 12 machine, as the guide (docs/install.md) says.
# Run on gaia-lab, which is the operator's workstation here and the KVM host of the clean machine: a fresh VM from
# Debian's cloud image on a transient NAT network of its own (gaia-t29u, 192.168.229.0/24), so nothing else changes
# on gaia-lab except one line in /etc/hosts (the server's name, removed by cleanup).
#
#   (a) install.md step 2 for both releases: the signature (packaging/release/allowed_signers) and every checksum
#   (b) 0.1.2 from the guide (steps 3 to 8b): the package, init, the services, login, the repository, a state backup,
#       the recovery kit; an operator with a 12-character password (allowed before 0.2.0)
#   (c) step 12 to 0.2.0: the package upgrade restarts gaia, the database goes to the new schema, and the 0.1.x
#       command-line login no longer works (ADR-008 §9)
#   (d) "To 0.2.0" in step 12: public_url with a name, a new TLS certificate for it, the admin signs in again and
#       enrols a security key (in software, lab/tools/softkey), and the data of 0.1.2 is there: the repository, the
#       state backup, the user; a new state backup runs; the operator must change the short password (ADR-008 §2)
#
# Usage (on gaia-lab, as root):
#   systemd-run --unit=gaia-t29u --collect -E HOME=/root lab/lab-vm/t29-upgrade.sh run OLD_RELEASE_DIR NEW_RELEASE_DIR
#   lab/lab-vm/t29-upgrade.sh cleanup                  the VM, its disk, its network and the /etc/hosts line
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

D=/root/t29u
DOM=gaia-t29-upgrade
NET=gaia-t29u
MAC=52:54:00:29:00:10
IP=192.168.229.10
NAME=gaia-t29u.lab.test
LV_IMAGES=/var/lib/libvirt/images
DISK=$LV_IMAGES/$DOM.qcow2
SEED=$LV_IMAGES/$DOM-seed.img
CLOUD_IMG=/var/lib/gaia/images/debian-12-genericcloud-amd64.qcow2
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in jq virsh qemu-img cloud-localds ssh scp ssh-keygen sha256sum curl go; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
section() { printf '\n== %s\n' "$*"; }
sshopts=(-i "$D/ssh/id_ed25519" -o UserKnownHostsFile="$D/ssh/known_hosts" -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=10)
on() { ssh "${sshopts[@]}" "debian@$IP" sudo bash -s; } # stdin as root on the clean server (the guide's "as root")
put() { scp -q "${sshopts[@]}" "$1" "debian@$IP:$2"; }

cleanup() {
	virsh -q destroy "$DOM" 2>/dev/null || true
	virsh -q undefine "$DOM" 2>/dev/null || true
	virsh -q net-destroy "$NET" 2>/dev/null || true
	rm -f "$DISK" "$SEED"
	sed -i "/ $NAME\$/d" /etc/hosts
	fstrim -a 2>/dev/null || true
}

case "${1:-}" in
cleanup)
	cleanup
	exit 0
	;;
run) ;;
*)
	sed -n '2,19p' "$0"
	exit 3
	;;
esac
OLD=$(cd "${2:?OLD_RELEASE_DIR}" && pwd)
NEW=$(cd "${3:?NEW_RELEASE_DIR}" && pwd)
OUT=$D/out-$(date -u +%Y%m%dT%H%M%SZ)
mkdir -p "$OUT"
exec > >(tee -a "$OUT/t29u.log") 2>&1
log "releases $OLD -> $NEW, out $OUT, repository $(git -C "$repo_root" rev-parse --short=12 HEAD)"
avail=$(df --output=avail -B1G "$LV_IMAGES" | tail -1 | tr -d ' ')
((avail >= 20)) || { echo "UNKNOWN: $avail GiB free under $LV_IMAGES, the clean VM needs 20"; exit 3; }
free_mib=$(awk '/MemAvailable/ { print int($2 / 1024) }' /proc/meminfo)
((free_mib >= 3500)) || { echo "UNKNOWN: $free_mib MiB available, the clean VM needs 3072"; exit 3; }

section "(a) install.md step 2 for both releases"
verify() { (cd "$1" && ssh-keygen -Y verify -f "$repo_root/packaging/release/allowed_signers" -I release@gaia -n gaia-release -s SHA256SUMS.sig <SHA256SUMS && sha256sum -c SHA256SUMS); }
a_old=0 a_new=0
verify "$OLD" >"$OUT/a-old.txt" 2>&1 || a_old=$?
verify "$NEW" >"$OUT/a-new.txt" 2>&1 || a_new=$?
cat "$OUT/a-old.txt" "$OUT/a-new.txt"
V_OLD=$(sed -n 's/^version: //p' "$OLD/BUILDINFO.txt")
V_NEW=$(sed -n 's/^version: //p' "$NEW/BUILDINFO.txt")
DEB_OLD=gaia_${V_OLD}_amd64.deb DEB_NEW=gaia_${V_NEW}_amd64.deb
jq -n --argjson old "$a_old" --argjson new "$a_new" --arg vo "$V_OLD" --arg vn "$V_NEW" \
	--arg oo "$(head -1 "$OUT/a-old.txt")" --arg on "$(head -1 "$OUT/a-new.txt")" \
	'{old: {version: $vo, exit: $old, signature: $oo}, new: {version: $vn, exit: $new, signature: $on}}
	 | . + {pass: (.old.exit == 0 and .new.exit == 0 and (.old.signature | startswith("Good \"gaia-release\" signature"))
	              and (.new.signature | startswith("Good \"gaia-release\" signature")))}' | tee "$OUT/a.json"

section "the clean server: $DOM from $(basename "$CLOUD_IMG"), network $NET"
cleanup
grep -q "$(sha512sum "$CLOUD_IMG" | awk '{print $1}')" "$(dirname "$CLOUD_IMG")/SHA512SUMS" || { echo "UNKNOWN: $CLOUD_IMG is not in its SHA512SUMS"; exit 3; }
mkdir -p "$D/ssh" && : >"$D/ssh/known_hosts"
[[ -f "$D/ssh/id_ed25519" ]] || ssh-keygen -q -t ed25519 -N '' -C gaia-t29u -f "$D/ssh/id_ed25519"
virsh -q net-create /dev/stdin <<EOF
<network><name>$NET</name><forward mode='nat'/><bridge name='virbr-t29u' stp='on' delay='0'/>
  <ip address='192.168.229.1' netmask='255.255.255.0'><dhcp><range start='192.168.229.100' end='192.168.229.150'/>
  <host mac='$MAC' ip='$IP'/></dhcp></ip></network>
EOF
qemu-img convert -q -f qcow2 -O qcow2 "$CLOUD_IMG" "$DISK"
qemu-img resize -q "$DISK" 20G
cat >"$D/user-data" <<EOF
#cloud-config
hostname: gaia-t29u
ssh_authorized_keys:
  - $(cat "$D/ssh/id_ed25519.pub")
EOF
printf 'instance-id: %s-%s\nlocal-hostname: gaia-t29u\n' "$DOM" "$(date +%s)" >"$D/meta-data"
cloud-localds "$SEED" "$D/user-data" "$D/meta-data"
chown libvirt-qemu:libvirt-qemu "$DISK" "$SEED"
virsh -q define /dev/stdin <<EOF
<domain type='kvm'><name>$DOM</name><memory unit='MiB'>3072</memory><vcpu>2</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type></os><cpu mode='host-passthrough'/>
  <devices>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2' discard='unmap'/><source file='$DISK'/><target dev='vda' bus='virtio'/></disk>
    <disk type='file' device='disk'><driver name='qemu' type='raw'/><source file='$SEED'/><target dev='vdb' bus='virtio'/><readonly/></disk>
    <interface type='network'><source network='$NET'/><mac address='$MAC'/><model type='virtio'/></interface>
    <serial type='pty'/><console type='pty'/>
  </devices></domain>
EOF
virsh -q start "$DOM"
for ((i = 0; i < 60; i++)); do
	on <<<'cloud-init status --wait >/dev/null; true' 2>/dev/null && break
	sleep 5
done
echo "$IP $NAME" >>/etc/hosts # the workstation reaches the server by its name (WebAuthn needs one, ADR-008 §3)

section "(b) 0.1.2 from the guide, steps 3 to 8b"
rm -rf "$D/ws-old" "$D/ws-new" && mkdir -p "$D/ws-old/bin" "$D/ws-new/bin"
(cd "$D/ws-old" && tar xzf "$OLD/gaia-$V_OLD-linux-amd64.tar.gz" && install -m 0755 "gaia-$V_OLD-linux-amd64/gaia" bin/gaia)
(cd "$D/ws-new" && tar xzf "$NEW/gaia-$V_NEW-linux-amd64.tar.gz" && install -m 0755 "gaia-$V_NEW-linux-amd64/gaia" bin/gaia)
old() { HOME=$D/ws GAIA_CLI_CONFIG=$D/ws/cli.json "$D/ws-old/bin/gaia" "$@"; }
new() { HOME=$D/ws GAIA_CLI_CONFIG=$D/ws/cli.json "$D/ws-new/bin/gaia" "$@"; }
rm -rf "$D/ws" && mkdir -p "$D/ws"
put "$OLD/$DEB_OLD" /tmp/
put "$OLD/SHA256SUMS" /tmp/SHA256SUMS.old
head -c 24 /dev/urandom | base64 >"$D/admin.pw" && chmod 600 "$D/admin.pw"
head -c 24 /dev/urandom | base64 >"$D/kit.pass" && chmod 600 "$D/kit.pass"
# A password allowed before 0.2.0 that 0.2.0 refuses (shorter than 15, ADR-008 §2); random, never printed.
head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 12 >"$D/op1.pw" && chmod 600 "$D/op1.pw"
put "$D/admin.pw" /tmp/admin.pw
put "$D/kit.pass" /tmp/kit.pass
on <<EOF | tee "$OUT/b-step3.txt"
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
cd /tmp && cp SHA256SUMS.old SHA256SUMS && sha256sum -c --ignore-missing SHA256SUMS && apt-get update >/dev/null && apt-get -o DPkg::Lock::Timeout=300 install -y ./$DEB_OLD >/dev/null && gaia version
chown gaia /tmp/admin.pw /tmp/kit.pass
runuser -u gaia -- gaia server init -config /etc/gaia/server.json -data-dir /var/lib/gaia -hostname $IP -admin admin -password-file /tmp/admin.pw -kit-passphrase-file /tmp/kit.pass -json | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["tls_certificate_sha256"])' >/tmp/fp.txt
rm -f /tmp/admin.pw /tmp/kit.pass
systemctl daemon-reload && systemctl enable --now gaia-sandbox gaia && sleep 3 && systemctl is-active gaia-sandbox gaia
EOF
fp_old=$(on <<<'cat /tmp/fp.txt')
old login -server "https://$IP:8443" -user admin -fingerprint "$fp_old" -password-file "$D/admin.pw"
old system -json | tee "$OUT/b-system.json" | jq -c '{version, schema_version}'
GAIA_KIT_PASSPHRASE=$(cat "$D/kit.pass") old repositories add name=main location=/srv/gaia/main -json | jq -c '{id, name}' | tee "$OUT/b-repository.json"
old users add username=op1 role=operator -json <"$D/op1.pw" | jq -c '{id, username, role}' | tee "$OUT/b-user.json"
old state-backups run -json | jq -c '{id, state}'
for ((i = 0; i < 60; i++)); do
	sb=$(old state-backups list -json | jq -c '.[0]')
	[[ $(jq -r .state <<<"$sb") != running ]] && break
	sleep 5
done
echo "$sb" | jq -c '{id, state}' | tee "$OUT/b-state-backup.json"
mkdir -p "$D/kits" && (cd "$D/kits" && rm -f ./*.age && old kit save -passphrase-file "$D/kit.pass" >/dev/null)
old kit status -json | jq -c '.summary | {state, generation}' | tee "$OUT/b-kit.json"
jq -n --slurpfile sys "$OUT/b-system.json" --argjson sb "$sb" --slurpfile kit "$OUT/b-kit.json" --arg v "$V_OLD" \
	--arg active "$(tail -2 "$OUT/b-step3.txt" | paste -sd ' ')" \
	'{version: $sys[0].version, schema: $sys[0].schema_version, services: $active, state_backup: $sb.state, kit: $kit[0].state}
	 | . + {pass: ((.version | startswith($v + " (")) and .schema == 8 and .services == "active active" and .state_backup == "ok" and .kit == "confirmed")}' |
	tee "$OUT/b.json"

section "(c) install.md step 12 to $V_NEW"
put "$NEW/$DEB_NEW" /tmp/
put "$NEW/SHA256SUMS" /tmp/SHA256SUMS.new
on <<EOF | tee "$OUT/c-upgrade.txt"
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
cd /tmp && cp SHA256SUMS.new SHA256SUMS && sha256sum -c --ignore-missing SHA256SUMS && apt-get -o DPkg::Lock::Timeout=300 install -y ./$DEB_NEW
EOF
on <<<'sleep 5; systemctl is-active gaia-sandbox gaia; journalctl -u gaia --no-pager -o cat --since "-3min" | grep -E "migrat|schema|signin_problem|public_url|ignored" | tail -20' | tee "$OUT/c-journal.txt"
old_session=0
old whoami >"$OUT/c-old-login.txt" 2>&1 || old_session=$?
new_session=0
new whoami >"$OUT/c-old-login-new-cli.txt" 2>&1 || new_session=$?
cat "$OUT/c-old-login.txt" "$OUT/c-old-login-new-cli.txt"
jq -n --arg out "$(cat "$OUT/c-upgrade.txt")" --arg j "$(cat "$OUT/c-journal.txt")" --argjson old "$old_session" --argjson new "$new_session" \
	'{restarted: ($out | contains("gaia: restarted")), active: ($j | split("\n") | .[0:2] == ["active", "active"]),
	  old_cli_login_exit: $old, old_login_with_new_cli_exit: $new, journal: ($j | split("\n") | .[2:])}
	 | . + {pass: (.restarted and .active and .old_cli_login_exit != 0 and .old_login_with_new_cli_exit != 0)}' | tee "$OUT/c.json"

section "(d) \"To 0.2.0\": public_url, a certificate for the name, sign in again with a security key"
on <<EOF | tee "$OUT/d-name.txt"
set -euo pipefail
python3 - <<'PY'
import json
p = "/etc/gaia/server.json"
c = json.load(open(p))
c["public_url"] = "https://$NAME:8443"
json.dump(c, open(p, "w"), indent=2)
PY
runuser -u gaia -- gaia server tls -hostname $NAME,$IP | tail -3
systemctl restart gaia && sleep 3 && systemctl is-active gaia
EOF
fp_new=$(echo | openssl s_client -connect "$IP:8443" -servername "$NAME" 2>/dev/null | openssl x509 -noout -fingerprint -sha256 | sed 's/.*=//; s/://g' | tr 'A-F' 'a-f')
go build -C "$repo_root" -o "$D/softkey" ./lab/tools/softkey
"$D/softkey" login -cli-config "$D/ws/cli.json" -server "https://$NAME:8443" -fingerprint "$fp_new" -user admin \
	-password-file "$D/admin.pw" -key "$D/admin-key.json" | tee "$OUT/d-login.txt"
new whoami | tee "$OUT/d-whoami.txt"
new system -json | tee "$OUT/d-system.json" | jq -c '{version, schema_version, signin_problem}'
new repositories list -json | jq -c '[.[] | {id, name}]' | tee "$OUT/d-repositories.json"
new users list -json | jq -c '[.[] | {username, role}] | sort_by(.username)' | tee "$OUT/d-users.json"
new state-backups list -json | jq -c '[.[] | {id, state}]' | tee "$OUT/d-state-backups-before.json"
new state-backups run -json | jq -c '{id, state}'
for ((i = 0; i < 60; i++)); do
	sb2=$(new state-backups list -json | jq -c '.[0]')
	[[ $(jq -r .state <<<"$sb2") != running ]] && break
	sleep 5
done
echo "$sb2" | jq -c '{id, state}' | tee "$OUT/d-state-backup.json"
# The operator's 12-character password from 0.1.2: the right password, then a forced change (ADR-008 §2).
curl -sk --resolve "$NAME:8443:$IP" -H 'Content-Type: application/json' \
	--data "$(jq -nc --arg p "$(cat "$D/op1.pw")" '{username: "op1", password: $p}')" "https://$NAME:8443/api/v1/login" |
	jq -c '{stage, password_change}' | tee "$OUT/d-op1.json"
jq -n --slurpfile sys "$OUT/d-system.json" --slurpfile repos "$OUT/d-repositories.json" --slurpfile users "$OUT/d-users.json" \
	--slurpfile before "$OUT/d-state-backups-before.json" --argjson sb "$sb2" --slurpfile op1 "$OUT/d-op1.json" \
	--slurpfile brepo "$OUT/b-repository.json" --slurpfile bsb "$OUT/b-state-backup.json" --arg v "$V_NEW" \
	--arg who "$(cat "$OUT/d-whoami.txt")" \
	'{version: $sys[0].version, schema: $sys[0].schema_version, signin_problem: ($sys[0].signin_problem // ""), whoami: $who,
	  repository_kept: ($repos[0] | any(.id == $brepo[0].id and .name == "main")), users: $users[0],
	  state_backup_of_0_1_2_kept: ($before[0] | any(.id == $bsb[0].id and .state == "ok")), new_state_backup: $sb.state, op1: $op1[0]}
	 | . + {pass: ((.version | startswith($v + " (")) and .schema == 16 and .signin_problem == "" and (.whoami | contains("admin"))
	              and .repository_kept and (.users | any(.username == "op1")) and .state_backup_of_0_1_2_kept and .new_state_backup == "ok"
	              and .op1.stage == "change_password" and .op1.password_change == "too_short")}' | tee "$OUT/d.json"

section "gate (T29: the upgrade from $V_OLD)"
jq -n --slurpfile a "$OUT/a.json" --slurpfile b "$OUT/b.json" --slurpfile c "$OUT/c.json" --slurpfile d "$OUT/d.json" \
	--arg from "$V_OLD" --arg to "$V_NEW" '{from: $from, to: $to, a: $a[0].pass, b: $b[0].pass, c: $c[0].pass, d: $d[0].pass}
	| . + {verdict: (if .a and .b and .c and .d then "OK" else "CRITICAL" end)}' | tee "$OUT/gate.json"
log "out: $OUT (the VM stays until: lab/lab-vm/t29-upgrade.sh cleanup)"
[[ $(jq -r .verdict "$OUT/gate.json") == OK ]] || exit 2
