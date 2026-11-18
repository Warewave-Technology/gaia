#!/usr/bin/env bash
# T22 gate: the signed release installs from the guide (docs/install.md) on a clean Debian 12 machine. Run on
# gaia-lab, which is the operator's workstation here and the KVM host of the clean machine: a fresh VM from Debian's
# cloud image on a transient NAT network of its own (gaia-t22, 192.168.222.0/24), so nothing else on gaia-lab changes.
#
#   (a) docs/limitations.md: every item names why it waits (scripts/check-limitations.sh)
#   (b) install.md step 2: the release key printed in the guide is the repository's; the signature and every checksum
#       verify; and three negative controls fail: a changed SHA256SUMS, a signature by another key, a changed package
#   (c) steps 3 to 8b on the clean VM as the guide says: the package installs, `gaia version` names the release, the
#       services run, login with the init's certificate sha256, the repository, a state backup, the recovery kit
#       saved and opened with the standalone age tool
#   (d) the same version installed again (apt-get install --reinstall): the package stops and starts gaia, which
#       answers with the same data
#   (e) apt-get purge keeps Gaia's data (step 14) and a new install of the package serves it again
#
# Usage (on gaia-lab, as root):
#   systemd-run --unit=gaia-t22 --collect -E HOME=/root lab/lab-vm/t22-release.sh run RELEASE_DIR
#   lab/lab-vm/t22-release.sh cleanup                  the VM, its disk and its network
# RELEASE_DIR holds the release files (install.md step 2) as they would be downloaded.
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

D=/root/t22
DOM=gaia-t22-server
NET=gaia-t22
MAC=52:54:00:22:00:10
IP=192.168.222.10
LV_IMAGES=/var/lib/libvirt/images
DISK=$LV_IMAGES/$DOM.qcow2
SEED=$LV_IMAGES/$DOM-seed.img
CLOUD_IMG=/var/lib/gaia/images/debian-12-genericcloud-amd64.qcow2
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in jq virsh qemu-img cloud-localds ssh scp ssh-keygen sha256sum age script; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh; age: apt-get install age)"; exit 3; }
done

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
section() { printf '\n== %s\n' "$*"; }
sshopts=(-i "$D/ssh/id_ed25519" -o UserKnownHostsFile="$D/ssh/known_hosts" -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=10)
on() { ssh "${sshopts[@]}" "debian@$IP" sudo bash -s; } # stdin as root on the clean server (the guide's "as root")
put() { scp -q "${sshopts[@]}" "$1" "debian@$IP:$2"; }
ws() { HOME=$D/ws "$D/ws/bin/gaia" "$@"; } # the gaia command on the workstation (step 6)

cleanup() {
	virsh -q destroy "$DOM" 2>/dev/null || true
	virsh -q undefine "$DOM" 2>/dev/null || true
	virsh -q net-destroy "$NET" 2>/dev/null || true
	rm -f "$DISK" "$SEED"
	fstrim -a 2>/dev/null || true
}

case "${1:-}" in
cleanup)
	cleanup
	exit 0
	;;
run) ;;
*)
	sed -n '2,22p' "$0"
	exit 3
	;;
esac
REL=$(cd "${2:?RELEASE_DIR}" && pwd)
OUT=$D/out-$(date -u +%Y%m%dT%H%M%SZ)
mkdir -p "$OUT"
exec > >(tee -a "$OUT/t22.log") 2>&1
log "release $REL, out $OUT, repository $(git -C "$repo_root" rev-parse --short=12 HEAD)"
avail=$(df --output=avail -B1G "$LV_IMAGES" | tail -1 | tr -d ' ')
((avail >= 20)) || { echo "UNKNOWN: $avail GiB free under $LV_IMAGES, the clean VM needs 20"; exit 3; }

section "(a) docs/limitations.md"
lim_out=$("$repo_root/scripts/check-limitations.sh" 2>&1) && lim_exit=0 || lim_exit=$?
echo "$lim_out"
jq -n --arg out "$lim_out" --argjson exit "$lim_exit" '{exit: $exit, output: ($out | split("\n")), pass: ($exit == 0)}' >"$OUT/a.json"

section "(b) install.md step 2: the release key from the guide, the signature, the checksums"
W=$D/workstation && rm -rf "$W" && mkdir -p "$W" && cp -p "$REL"/* "$W/"
# The guide's key: the line in the code block after "Gaia's release key is this one line".
awk '/Gaia.s release key is this one line/ { f = 1 } f && /^```$/ { if (in_block) exit; in_block = 1; next } in_block { print }' \
	"$repo_root/docs/install.md" >"$W/allowed_signers"
grep -v '^#' "$repo_root/packaging/release/allowed_signers" >"$OUT/repo-signer.txt"
guide_is_repo=false
cmp -s "$W/allowed_signers" "$OUT/repo-signer.txt" && guide_is_repo=true
verify() { # DIR: step 2's command, as the guide prints it
	(cd "$1" && ssh-keygen -Y verify -f allowed_signers -I release@gaia -n gaia-release -s SHA256SUMS.sig <SHA256SUMS && sha256sum -c SHA256SUMS)
}
good_out=$(verify "$W" 2>&1) && good=0 || good=$?
echo "$good_out" | tee "$OUT/b-verify.txt"
# Negative controls, each on its own copy.
neg() { # NAME: a copy of the release changed as NAME says; prints the exit code of step 2's command on it
	local n=$1 c=$D/neg-$1
	rm -rf "$c" && cp -a "$W" "$c"
	case "$n" in
	sums) sed -i '1s/^./0/; 1s/^00/11/' "$c/SHA256SUMS" ;;
	key)
		ssh-keygen -q -t ed25519 -N '' -C other -f "$c/other" >/dev/null
		rm -f "$c/SHA256SUMS.sig" && ssh-keygen -q -Y sign -f "$c/other" -n gaia-release "$c/SHA256SUMS" >/dev/null 2>&1
		;;
	package) printf 'x' | dd of="$(ls "$c"/gaia_*_amd64.deb)" bs=1 seek=4096 conv=notrunc status=none ;;
	esac
	local e=0
	verify "$c" >"$OUT/b-neg-$n.txt" 2>&1 || e=$?
	echo "$e"
}
jq -n --argjson guide_is_repo "$guide_is_repo" --argjson good "$good" --arg out "$good_out" \
	--argjson sums "$(neg sums)" --argjson key "$(neg key)" --argjson package "$(neg package)" \
	'{guide_key_is_repository_key: $guide_is_repo, verify_exit: $good, verify_output: ($out | split("\n")),
	  negative_exit: {changed_sums: $sums, other_key: $key, changed_package: $package}}
	 | . + {pass: (.guide_key_is_repository_key and .verify_exit == 0 and (.verify_output[0] | startswith("Good \"gaia-release\" signature for release@gaia"))
	              and ([.negative_exit[]] | all(. != 0)))}' | tee "$OUT/b.json"
DEB=$(basename "$(ls "$W"/gaia_*_amd64.deb)")
VERSION=$(sed -n 's/^version: //p' "$W/BUILDINFO.txt")

section "the clean server: $DOM from $(basename "$CLOUD_IMG"), network $NET"
cleanup
grep -q "$(sha512sum "$CLOUD_IMG" | awk '{print $1}')" "$(dirname "$CLOUD_IMG")/SHA512SUMS" || { echo "UNKNOWN: $CLOUD_IMG is not in its SHA512SUMS"; exit 3; }
sha512sum "$CLOUD_IMG" | tee "$OUT/cloud-image.sha512"
mkdir -p "$D/ssh" && : >"$D/ssh/known_hosts"
[[ -f "$D/ssh/id_ed25519" ]] || ssh-keygen -q -t ed25519 -N '' -C gaia-t22 -f "$D/ssh/id_ed25519"
virsh -q net-create /dev/stdin <<EOF
<network><name>$NET</name><forward mode='nat'/><bridge name='virbr-t22' stp='on' delay='0'/>
  <ip address='192.168.222.1' netmask='255.255.255.0'><dhcp><range start='192.168.222.100' end='192.168.222.150'/>
  <host mac='$MAC' ip='$IP'/></dhcp></ip></network>
EOF
qemu-img convert -q -f qcow2 -O qcow2 "$CLOUD_IMG" "$DISK"
qemu-img resize -q "$DISK" 20G
cat >"$D/user-data" <<EOF
#cloud-config
hostname: $DOM
ssh_authorized_keys:
  - $(cat "$D/ssh/id_ed25519.pub")
EOF
printf 'instance-id: %s-%s\nlocal-hostname: %s\n' "$DOM" "$(date +%s)" "$DOM" >"$D/meta-data"
cloud-localds "$SEED" "$D/user-data" "$D/meta-data"
chown libvirt-qemu:libvirt-qemu "$DISK" "$SEED"
# The seed is a read-only virtio disk: Debian's cloud kernel reads no SATA CD-ROM (T16).
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
# shellcheck disable=SC2016 # expands on the server
on <<<'. /etc/os-release && echo "$PRETTY_NAME, $(uname -r), $(dpkg -l | grep -c "^ii") packages"' | tee "$OUT/c-os.txt"

section "(c) install.md steps 3 to 8b on the clean server"
put "$W/$DEB" /tmp/
put "$W/SHA256SUMS" /tmp/
head -c 24 /dev/urandom | base64 >"$D/admin.pw" && chmod 600 "$D/admin.pw"
head -c 24 /dev/urandom | base64 >"$D/kit.pass" && chmod 600 "$D/kit.pass"
put "$D/admin.pw" /tmp/admin.pw
put "$D/kit.pass" /tmp/kit.pass
# Step 3, as the guide prints it (DEBIAN_FRONTEND only keeps apt from asking, as in any script).
on <<EOF | tee "$OUT/c-step3.txt"
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
cd /tmp && sha256sum -c --ignore-missing SHA256SUMS && apt-get update && apt-get install -y ./$DEB && gaia version
EOF
on <<'EOF' >"$OUT/c-installed.txt"
set -euo pipefail
echo "status=$(dpkg-query -W -f '${Status}' gaia)"
echo "version=$(dpkg-query -W -f '${Version}' gaia)"
echo "user=$(id gaia)"
echo "dirs=$(stat -c '%n %U:%G %a' /var/lib/gaia /etc/gaia /srv/gaia | paste -sd ';')"
echo "units=$(systemctl show -p FragmentPath --value gaia gaia-sandbox gaia-convert | paste -sd ';')"
echo "enabled=$(systemctl is-enabled gaia gaia-sandbox gaia-convert 2>&1 | paste -sd ';' || true)"
echo "binary=$(gaia version)"
EOF
jq -R 'capture("^(?<k>[a-z]+)=(?<v>.*)$") | {(.k): .v}' "$OUT/c-installed.txt" | jq -s add | tee "$OUT/c-installed.json"
# Step 4 (the passwords from files instead of the terminal), step 5.
on <<EOF | tee "$OUT/c-init.json"
set -euo pipefail
chown gaia /tmp/admin.pw /tmp/kit.pass
runuser -u gaia -- gaia server init -config /etc/gaia/server.json -data-dir /var/lib/gaia -hostname $IP -admin admin -password-file /tmp/admin.pw -kit-passphrase-file /tmp/kit.pass -json
rm -f /tmp/admin.pw /tmp/kit.pass
EOF
on <<'EOF' | tee "$OUT/c-step5.txt"
systemctl daemon-reload && systemctl enable --now gaia-sandbox gaia && sleep 3 && systemctl is-active gaia-sandbox gaia
journalctl -u gaia --no-pager -n 20 | grep -c 'gaia server is listening'
EOF
# Step 6: the workstation's gaia command from the release, login with the init's certificate sha256.
rm -rf "$D/ws" && mkdir -p "$D/ws/bin"
(cd "$D" && tar xzf "$W/gaia-$VERSION-linux-amd64.tar.gz" && install -m 0755 "gaia-$VERSION-linux-amd64/gaia" "$D/ws/bin/gaia")
ws version | tee "$OUT/c-ws-version.txt"
fp=$(jq -r '.result.tls_certificate_sha256' "$OUT/c-init.json")
ws login -server "https://$IP:8443" -user admin -fingerprint "$fp" -password-file "$D/admin.pw"
ws whoami
ws system -json | tee "$OUT/c-system.json" | jq -c '{version, schema_version}'
# Step 7, 8, 8b.
GAIA_KIT_PASSPHRASE=$(cat "$D/kit.pass") ws repositories add name=main location=/srv/gaia/main -json | tee "$OUT/c-repository.json" | jq -c '{id, name, kind}'
ws state-backups run -json | tee "$OUT/c-state-backup.json" | jq -c '{id, state}'
for ((i = 0; i < 60; i++)); do
	sb=$(ws state-backups list -json | jq -c '.[0]')
	[[ $(jq -r .state <<<"$sb") != running ]] && break
	sleep 5
done
echo "$sb" | tee "$OUT/c-state-backup-done.json"
mkdir -p "$D/kits" && (cd "$D/kits" && rm -f ./*.age && ws kit save -passphrase-file "$D/kit.pass" | tee "$OUT/c-kit-save.txt")
kit=$(ls "$D"/kits/*.age)
age_ok=false
{ sleep 2; cat "$D/kit.pass"; } | script -qec "age -d -o $D/kit.json $kit" /dev/null >/dev/null && jq -e .repositories "$D/kit.json" >/dev/null && age_ok=true
rm -f "$D/kit.json"
ws kit status -json | tee "$OUT/c-kit-status.json" | jq -c '{state}'
jq -n --slurpfile inst "$OUT/c-installed.json" --slurpfile sys "$OUT/c-system.json" --slurpfile repo "$OUT/c-repository.json" \
	--argjson sb "$sb" --slurpfile kit "$OUT/c-kit-status.json" --argjson age "$age_ok" --arg version "$VERSION" \
	--arg active "$(sed -n 1,2p "$OUT/c-step5.txt" | paste -sd ' ')" \
	'{package: $inst[0], services: $active, version: $sys[0].version, schema: $sys[0].schema_version, repository: ($repo[0] | {id, name, splitter, hash}),
	  state_backup: ($sb | {id, state}), kit: $kit[0].state, kit_opens_with_age: $age}
	 | . + {pass: (.package.status == "install ok installed" and .package.version == $version and (.package.binary | startswith("gaia " + $version + " ("))
	              and (.package.units | split(";") | all(startswith("/lib/systemd/system/"))) and .services == "active active"
	              and (.version | startswith($version + " (")) and .schema == 8 and .state_backup.state == "ok" and .kit == "confirmed" and .kit_opens_with_age)}' \
	| tee "$OUT/c.json"

section "(d) the same version again: apt-get install --reinstall"
users_before=$(ws users list -json | jq -c '[.[].username] | sort')
on <<EOF | tee "$OUT/d-reinstall.txt"
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
cd /tmp && apt-get install -y --reinstall ./$DEB && sleep 3 && systemctl is-active gaia-sandbox gaia
EOF
for ((i = 0; i < 30; i++)); do ws system -json >"$OUT/d-system.json" 2>/dev/null && break; sleep 2; done
jq -n --slurpfile sys "$OUT/d-system.json" --argjson before "$users_before" --argjson after "$(ws users list -json | jq -c '[.[].username] | sort')" \
	--arg out "$(cat "$OUT/d-reinstall.txt")" \
	'{stopped: ($out | contains("gaia: stopping gaia")), restarted: ($out | contains("gaia: restarted")), active: ($out | split("\n") | .[-2:] == ["active", "active"]),
	  version: $sys[0].version, users_same: ($before == $after)}
	 | . + {pass: (.stopped and .restarted and .active and .users_same)}' | tee "$OUT/d.json"

section "(e) apt-get purge keeps Gaia's data (step 14), and a new install serves it"
on <<EOF | tee "$OUT/e-purge.txt"
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get purge -y gaia >/dev/null
echo "binary: \$(test -e /usr/bin/gaia && echo present || echo gone)"
echo "unit: \$(test -e /lib/systemd/system/gaia.service && echo present || echo gone)"
echo "data: \$(test -s /var/lib/gaia/gaia.db && test -s /etc/gaia/server.json && test -d /srv/gaia/main && echo kept || echo lost)"
echo "user: \$(id -u gaia >/dev/null 2>&1 && echo kept || echo gone)"
cd /tmp && apt-get install -y ./$DEB >/dev/null && systemctl enable --now gaia-sandbox gaia && sleep 3 && systemctl is-active gaia
EOF
for ((i = 0; i < 30; i++)); do ws whoami >/dev/null 2>&1 && break; sleep 2; done
ws whoami >"$OUT/e-whoami.txt" 2>&1 || true
jq -n --arg out "$(cat "$OUT/e-purge.txt")" --arg who "$(cat "$OUT/e-whoami.txt")" \
	'{purge: ($out | split("\n")), session_still_valid: ($who | contains("admin"))}
	 | . + {pass: ((.purge | index("binary: gone")) != null and (.purge | index("unit: gone")) != null and (.purge | index("data: kept")) != null
	              and (.purge | index("user: kept")) != null and .purge[-1] == "active" and .session_still_valid)}' | tee "$OUT/e.json"

section "gate (CLAUDE.md T22)"
jq -n --slurpfile a "$OUT/a.json" --slurpfile b "$OUT/b.json" --slurpfile c "$OUT/c.json" --slurpfile d "$OUT/d.json" --slurpfile e "$OUT/e.json" \
	--arg version "$VERSION" '{version: $version, a: $a[0].pass, b: $b[0].pass, c: $c[0].pass, d: $d[0].pass, e: $e[0].pass}
	| . + {verdict: (if .a and .b and .c and .d and .e then "OK" else "CRITICAL" end)}' | tee "$OUT/gate.json"
log "out: $OUT (the VM stays until: lab/lab-vm/t22-release.sh cleanup)"
[[ $(jq -r .verdict "$OUT/gate.json") == OK ]] || exit 2
