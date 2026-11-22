#!/usr/bin/env bash
# fixture: a Gaia server on gaia-lab with real data for the T27 screens (CLAUDE.md T27 step 1). Not part of the
# product. Its own server (gaia-t27, packaged units, https://gaia-lab.test:8448) and sandbox unit (2 slots) protect
# the T25 fixture's guests (lab/lab-vm/t25-guests.sh) on gaia-lab over SSH, on schedules short enough that a few hours
# give every screen something to show: six Linux VMs (two policies, tagged), the Windows guest (a policy whose RTO
# target it misses), and one VM without a policy.
#
# Usage (on gaia-lab, as root): lab/lab-vm/t27-lab.sh up | status | token FILE | down
#   token FILE   signs the admin in (a WebAuthn key in software, lab/tools/softkey) and writes the CLI session's token
#                to FILE (0600), for the screenshot and gate tools (lab/scale/ui-gate.mjs)
set -euo pipefail
SRV_USER=gaia-t27
SRV_HOME=/var/lib/gaia-t27
SRV_BIN=/usr/local/sbin/gaia-t27
UNIT=gaia-t27-server
SB_UNIT=gaia-t27-sandbox
SB_HOME=/var/lib/gaia-t27-sandbox
SB_SOCK=/run/gaia-t27-sandbox/sandbox.sock
SB_WORK=/var/lib/libvirt/images/gaia-t27-sandbox
HOST_USER=gaia-t27-backup
PUBLIC=https://gaia-lab.test:8448
WORK=/root/t27w
LIN=(gaia-t25-lin-1 gaia-t25-lin-2 gaia-t25-lin-3 gaia-t25-lin-4 gaia-t25-lin-5 gaia-t25-lin-6)
WIN=gaia-t25-win
NOPOL=gaia-t25-iso-1
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
((EUID == 0)) || { echo "run as root"; exit 3; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
api() { HOME=$WORK "$BIN/gaia" "$@" -json; }
# sk METHOD PATH [JSON]: a call that may need a step-up, made with the admin's key in software (the CLI cannot touch it)
sk() { "$BIN/softkey" call -cli-config "$WORK/.config/gaia/cli.json" -key "$WORK/admin-key.json" "$@"; }
fp() { jq -r .result.tls_certificate_sha256 "$WORK/server-init.json"; }
signin() {
	"$BIN/softkey" login -cli-config "$WORK/.config/gaia/cli.json" -server "$PUBLIC" -fingerprint "$(fp)" -user admin \
		-password-file "$WORK/admin.pw" -key "$WORK/admin-key.json" >&2
}

down() {
	for u in "$UNIT" "$SB_UNIT"; do systemctl stop "$u" 2>/dev/null || true; done
	rm -f "/run/systemd/system/$UNIT.service" "/run/systemd/system/$SB_UNIT.service"
	systemctl daemon-reload
	pkill -u "$HOST_USER" 2>/dev/null || true
	userdel -r "$HOST_USER" 2>/dev/null || true
	userdel "$SRV_USER" 2>/dev/null || true
	rm -rf "$SRV_HOME" "$SB_HOME" "$SB_WORK" "$SRV_BIN" "$WORK"
	for vm in "${LIN[@]}" "$WIN" "$NOPOL"; do
		for c in $(virsh checkpoint-list "$vm" --name 2>/dev/null); do virsh -q checkpoint-delete "$vm" "$c" 2>/dev/null || true; done
	done
	fstrim -a 2>/dev/null || true
}
case "${1:-}" in
down) down; exit 0 ;;
status)
	jq -n --arg u "$PUBLIC" --arg s "$(systemctl is-active "$UNIT" || true)" --arg b "$(systemctl is-active "$SB_UNIT" || true)" \
		--arg fp "$(fp 2>/dev/null || true)" --arg df "$(df -h --output=avail "$SRV_HOME" 2>/dev/null | tail -1 | tr -d ' ')" \
		'{url: $u, server: $s, sandbox: $b, tls_certificate_sha256: $fp, repository_free: $df}'
	exit 0 ;;
token)
	out=${2:?token FILE}
	signin
	(umask 077 && jq -r .token "$WORK/.config/gaia/cli.json" >"$out")
	echo "token written to $out" >&2
	exit 0 ;;
up) ;;
*) echo "usage: $0 up | status | token FILE | down"; exit 3 ;;
esac

grep -q " gaia-lab.test\$" /etc/hosts || echo "$(hostname -I | awk '{ print $1 }') gaia-lab.test" >>/etc/hosts
free_gb=$(df --output=avail -BG /var/lib | tail -1 | tr -dc 0-9)
((free_gb >= 30)) || { echo "UNKNOWN: ${free_gb} GB free under /var/lib, the repository wants 30"; exit 3; }
down
mkdir -p "$WORK"
owner=$(stat -c %U "$repo_root")
asowner() { if [[ "$owner" == root ]]; then "$@"; else runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" "$@"; fi; }
asowner make -C "$repo_root" -s build
asowner go build -C "$repo_root" -o "$BIN/softkey" ./lab/tools/softkey
useradd --system --home-dir "$SRV_HOME" --shell /usr/sbin/nologin "$SRV_USER"
install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME" "$SRV_HOME/data" "$SRV_HOME/repo"
install -m 0755 "$BIN/gaia" "$SRV_BIN"
head -c 24 /dev/urandom | base64 | sed 's/^/t27 lab /' | install -m 0600 /dev/stdin "$WORK/admin.pw"
head -c 24 /dev/urandom | base64 | install -m 0600 /dev/stdin "$WORK/kit.pass"
install -m 0600 -o "$SRV_USER" "$WORK/admin.pw" "$SRV_HOME/admin.pw"
install -m 0600 -o "$SRV_USER" "$WORK/kit.pass" "$SRV_HOME/kit.pass"
runuser -u "$SRV_USER" -- "$SRV_BIN" server init -config "$SRV_HOME/server.json" -data-dir "$SRV_HOME/data" -listen :8448 \
	-hostname gaia-lab.test -public-url "$PUBLIC" -admin admin -password-file "$SRV_HOME/admin.pw" -kit-passphrase-file "$SRV_HOME/kit.pass" \
	-json >"$WORK/server-init.json"
rm -f "$SRV_HOME/admin.pw" "$SRV_HOME/kit.pass"
jq --arg s "$SB_SOCK" --arg i "$SB_HOME/images" '. + {sandbox_socket: $s, sandbox_images_dir: $i}' "$SRV_HOME/server.json" >"$WORK/server.json"
install -m 0640 -o "$SRV_USER" -g "$SRV_USER" "$WORK/server.json" "$SRV_HOME/server.json"
sed -e "s|^ExecStart=.*|ExecStart=$SRV_BIN sandbox serve -group $SRV_USER -socket $SB_SOCK -images $SB_HOME/images -work-dir $SB_WORK -bundle-dir $SB_HOME/bundles -slots 2 -memory-mib 3584|" \
	"$repo_root/packaging/systemd/gaia-sandbox.service" >"/run/systemd/system/$SB_UNIT.service"
sed -e "s|^User=.*|User=$SRV_USER|" -e "s|^Group=.*|Group=$SRV_USER|" \
	-e "s|^ExecStart=.*|ExecStart=$SRV_BIN server run -config $SRV_HOME/server.json|" \
	-e "s|^ReadWritePaths=.*|ReadWritePaths=$SRV_HOME -$SB_HOME/images|" \
	-e "s|gaia-sandbox.service gaia-convert.service|$SB_UNIT.service|" \
	"$repo_root/packaging/systemd/gaia.service" >"/run/systemd/system/$UNIT.service"
systemctl daemon-reload
systemctl start "$SB_UNIT"
for ((i = 0; i < 30; i++)); do [[ -S $SB_SOCK ]] && break; sleep 1; done
systemctl start "$UNIT"
for ((i = 0; i < 30; i++)); do curl -fsk "$PUBLIC/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
signin
api system | jq -c '{version, schema_version, public_url}' >&2
jq -n --arg l "$SRV_HOME/repo/main" --rawfile k "$WORK/kit.pass" '{name: "main", location: $l, kit_passphrase: ($k | rtrimstr("\n"))}' >"$WORK/repo.json"
sk POST /api/v1/repositories "$(cat "$WORK/repo.json")" | jq -c '{id, name}' >&2
rm -f "$WORK/repo.json"
useradd -m -s /usr/sbin/nologin -G libvirt,libvirt-qemu "$HOST_USER"
install -d -m 700 -o "$HOST_USER" -g "$HOST_USER" "/home/$HOST_USER/.ssh"
api system | jq -r .ssh_authorized_keys_line | install -m 0600 -o "$HOST_USER" -g "$HOST_USER" /dev/stdin "/home/$HOST_USER/.ssh/authorized_keys"
host_fp=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $2 }')
host_id=$(sk POST /api/v1/hosts "{\"name\":\"gaia-lab\",\"address\":\"127.0.0.1\",\"ssh_user\":\"$HOST_USER\"}" | jq .id)
[[ $(api hosts scan "$host_id" | jq -r .fingerprint) == "$host_fp" ]] || { echo "UNKNOWN: scanned fingerprint differs"; exit 3; }
api hosts trust "$host_id" fingerprint="$host_fp" >/dev/null
pol() { api policies add name="$1" repository_id=1 backup_interval_s="$2" test_interval_s="$3" keep_last=12 keep_daily=7 keep_weekly=0 \
	rpo_target_s="$4" rto_target_s="$5" max_proof_age_s="$6" check_template="$7" | jq .id; }
prod=$(pol std-linux 1800 3600 3600 600 7200 linux-boot)
test=$(pol dev-linux 3600 10800 14400 900 21600 linux-boot)
win=$(pol std-windows 7200 21600 14400 300 43200 windows-basic)
i=0
for vm in "${LIN[@]}"; do
	p=$prod tags='["env:prod","os:linux"]'
	((i >= 3)) && p=$test tags='["env:test","os:linux"]'
	id=$(api vms add host_id="$host_id" name="$vm" policy_id="$p" | jq .id)
	sk PUT "/api/v1/vms/$id/tags" "{\"tags\":$tags}" >/dev/null
	i=$((i + 1))
done
id=$(api vms add host_id="$host_id" name="$WIN" policy_id="$win" | jq .id)
sk PUT "/api/v1/vms/$id/tags" '{"tags":["env:prod","os:windows"]}' >/dev/null
api vms add host_id="$host_id" name="$NOPOL" | jq -c '{id, name}' >&2
log "up: $PUBLIC; the first backups and tests start at the scheduler's next tick"
"$0" status
