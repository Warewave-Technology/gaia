#!/usr/bin/env bash
# Runs the T2 gate from gaia-lab: vsphere-probe against the nested free ESXi, through the
# remote vSphere API, the way a backup product would talk to it. Output goes verbatim into the T2 report.
#
#   run A  vsphere-probe creates its own throwaway VM (CreateVM_Task) and probes it.
#   run B  only if run A had no VM (CreateVM_Task refused): a VM is made locally on the host
#          over SSH (vmkfstools + vim-cmd solo/registervm, the host's own shell, not the API),
#          then vsphere-probe -vm probes the backup calls on it through the API as before.
#          The VM is removed over SSH afterwards.
#
# Usage (on gaia-lab, as root, after setup.sh):  lab/lab-vm/t2-probe.sh 192.0.2.10
# Env: GAIA_VSPHERE_PASSWORD (prompted for if unset; never printed), GAIA_VSPHERE_USER (root),
#      DATASTORE for run B (default: the host's first datastore), OUT_DIR (/root/t2-<UTC timestamp>).
# Exit: the gate verdict, 0 OK / 1 WARNING / 2 CRITICAL / 3 UNKNOWN (also when the harness failed).
set -euo pipefail

ESXI_IP="${1:?ESXi host IP}"
GAIA_VSPHERE_USER="${GAIA_VSPHERE_USER:-root}"
OUT_DIR="${OUT_DIR:-/root/t2-$(date -u +%Y%m%dT%H%M%SZ)}"
LOCAL_VM=gaia-probe-local
export GAIA_VSPHERE_URL="https://${ESXI_IP}/sdk" GAIA_VSPHERE_USER
export GOVC_URL="$GAIA_VSPHERE_URL" GOVC_USERNAME="$GAIA_VSPHERE_USER" GOVC_INSECURE=1
# Any unexpected failure is a harness failure: UNKNOWN, never a verdict the run did not reach.
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in go govc jq ssh make; do command -v "$bin" >/dev/null || { echo "MISSING: $bin (run setup.sh)"; exit 3; }; done
if [[ -z "${GAIA_VSPHERE_PASSWORD:-}" ]]; then
	read -rsp "ESXi ${GAIA_VSPHERE_USER} password: " GAIA_VSPHERE_PASSWORD
	echo
fi
export GAIA_VSPHERE_PASSWORD GOVC_PASSWORD="$GAIA_VSPHERE_PASSWORD"

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
probe="$repo_root/bin/vsphere-probe"
mkdir -p "$OUT_DIR"
work="$(mktemp -d)"

section() { printf '\n== %s\n' "$*"; }
# The edition is evidence; a license key is not and must not reach a public report.
mask() { sed -E 's/\b([A-Z0-9]{5})(-[A-Z0-9]{5}){3}-([A-Z0-9]{5})\b/\1-*****-*****-*****-\3/g'; }
vm_step() { jq -r '[.steps[] | select(.id == "vm") | .status][0] // "MISSING"' "$1"; }

# SSH to the host with the same password, asked for by ssh through SSH_ASKPASS so it never
# appears on a command line. One connection is shared by every call (ControlMaster), and a
# wrong password is tried once only: ESXi locks root after 5 failed logins.
cat >"$work/askpass" <<'EOF'
#!/bin/sh
printf '%s\n' "$GAIA_VSPHERE_PASSWORD"
EOF
chmod 700 "$work/askpass"
ssh_opts=(-o ControlMaster=auto -o "ControlPath=$work/ssh.sock" -o ControlPersist=600
	-o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$work/known_hosts"
	-o NumberOfPasswordPrompts=1 -o PubkeyAuthentication=no -o ConnectTimeout=10)
esxi_sh() { # esxi_sh ARGS... < script: runs the script with the host's /bin/sh
	# shellcheck disable=SC2029 # expanding here is the point: the quoted args become the remote command
	SSH_ASKPASS="$work/askpass" SSH_ASKPASS_REQUIRE=force \
		ssh "${ssh_opts[@]}" "${GAIA_VSPHERE_USER}@${ESXI_IP}" "sh -s -- $(printf '%q ' "$@")"
}
# shellcheck disable=SC2329 # called from the EXIT trap
ssh_close() { ssh "${ssh_opts[@]}" -O exit "${GAIA_VSPHERE_USER}@${ESXI_IP}" 2>/dev/null || true; }
trap 'ssh_close; rm -rf "$work"' EXIT

# Removes the local VM and its files, whatever state an earlier run left them in.
remove_local_vm() {
	esxi_sh "$LOCAL_VM" "$ds" <<'EOF'
id=$(vim-cmd vmsvc/getallvms 2>/dev/null | awk -v n="$1" '$2 == n { print $1 }')
if [ -n "$id" ]; then
	vim-cmd vmsvc/snapshot.removeall "$id"
	vim-cmd vmsvc/unregister "$id"
fi
rm -rf "/vmfs/volumes/$2/$1"
echo "removed: $1 (vmid ${id:-none})"
EOF
}

section "date / commit"
date -u +%Y-%m-%dT%H:%M:%SZ
git -C "$repo_root" rev-parse HEAD 2>/dev/null || echo "not a git checkout"
echo "output: $OUT_DIR"

# Build from the checkout as its owner (see setup.sh), so the probe that runs is the one committed.
section "build"
owner=$(stat -c %U "$repo_root")
if [[ "$owner" == root ]]; then
	make -C "$repo_root" -s build
else
	runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" make -C "$repo_root" -s build
fi
go version -m "$probe" | grep -E 'vcs\.(revision|modified)' || echo "no VCS stamp in $probe"

# One login first; without it the probe and SSH would each spend a failed attempt on a wrong password.
section "login (govc about)"
if ! govc about; then
	echo "UNKNOWN: login failed; nothing else was tried, to avoid an ESXi account lockout"
	exit 3
fi
if [[ -n "${DATASTORE:-}" ]]; then
	ds="$DATASTORE"
else
	ds=$(govc ls /ha-datacenter/datastore | awk 'NR == 1')
	ds="${ds##*/}"
fi
[[ -n "$ds" ]] || { echo "UNKNOWN: no datastore found"; exit 3; }
echo "datastore: $ds"

section "run A: vsphere-probe -insecure (creates gaia-probe through the API)"
rc_a=0
"$probe" -insecure -json "$OUT_DIR/probe-a.json" | mask || rc_a=${PIPESTATUS[0]}
echo "exit: $rc_a"
[[ -s "$OUT_DIR/probe-a.json" ]] || { echo "UNKNOWN: run A wrote no JSON"; exit 3; }
section "run A: JSON"
mask <"$OUT_DIR/probe-a.json"
echo

rc=$rc_a
if [[ "$(vm_step "$OUT_DIR/probe-a.json")" == PASS ]]; then
	section "run B: not needed (run A had a VM)"
else
	section "run B: local VM $LOCAL_VM over SSH (vmkfstools + vim-cmd solo/registervm)"
	remove_local_vm || { echo "UNKNOWN: SSH to ${ESXI_IP} failed"; exit 3; }
	if ! esxi_sh "$LOCAL_VM" "$ds" <<'EOF'; then
set -e
dir="/vmfs/volumes/$2/$1"
mkdir "$dir"
vmkfstools -c 1G -d thin "$dir/$1.vmdk"
cat >"$dir/$1.vmx" <<VMX
.encoding = "UTF-8"
config.version = "8"
virtualHW.version = "19"
displayName = "$1"
guestOS = "other-64"
memSize = "128"
numvcpus = "1"
scsi0.present = "TRUE"
scsi0.virtualDev = "pvscsi"
scsi0:0.present = "TRUE"
scsi0:0.deviceType = "scsi-hardDisk"
scsi0:0.fileName = "$1.vmdk"
VMX
echo "registered: vmid $(vim-cmd solo/registervm "$dir/$1.vmx")"
EOF
		echo "UNKNOWN: could not create $LOCAL_VM"
		remove_local_vm || true
		exit 3
	fi

	section "run B: vsphere-probe -insecure -datastore $ds -vm $LOCAL_VM"
	rc_b=0
	"$probe" -insecure -datastore "$ds" -vm "$LOCAL_VM" -json "$OUT_DIR/probe-b.json" | mask || rc_b=${PIPESTATUS[0]}
	echo "exit: $rc_b"

	section "run B: cleanup over SSH"
	remove_local_vm || echo "WARNING: cleanup failed; remove $LOCAL_VM by hand"

	[[ -s "$OUT_DIR/probe-b.json" ]] || { echo "UNKNOWN: run B wrote no JSON"; exit 3; }
	section "run B: JSON"
	mask <"$OUT_DIR/probe-b.json"
	echo
	rc=$rc_b
	# Run B probes the backup calls only if it found its VM; otherwise it measured nothing.
	[[ "$(vm_step "$OUT_DIR/probe-b.json")" == PASS ]] || rc=3
fi

section "result"
case $rc in
0) echo "OK" ;;
1) echo "WARNING" ;;
2) echo "CRITICAL" ;;
*) echo "UNKNOWN" ;;
esac
echo "(gate verdict from run $([[ -f "$OUT_DIR/probe-b.json" ]] && echo B || echo A); JSON in $OUT_DIR)"
exit "$rc"
