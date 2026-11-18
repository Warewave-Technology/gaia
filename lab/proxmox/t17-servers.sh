#!/usr/bin/env bash
# fixture: the Gaia servers of the T17 disaster-recovery drill and the T23 MVP acceptance (names gaia-t17-* and
# gaia-t23-*), on the Proxmox host. Server A protects a VM and
# is lost; server B is a fresh machine that takes over with the recovery kit and A's repository disk, which survives
# A (like a SAN volume or a second disk moved to the new machine).
#
# Usage (as root on the Proxmox host):
#   t17-servers.sh create VMID NAME PUBKEYS [repo]   a Debian 12 cloud VM (4 vCPU, 4 GiB, 20 GiB), not started; with
#                                                    "repo", also a 20 GiB repository disk (scsi2, empty)
#   t17-servers.sh start VMID                        starts it and prints its IPv4
#   t17-servers.sh move-disk FROM TO                 moves the repository disk (scsi2) from VM FROM to VM TO (both off)
#   t17-servers.sh lose VMID                         power cut (no shutdown) and destroy; refuses unless VMID's
#                                                    name starts with gaia-t17-
#   t17-servers.sh ip VMID                           prints the VM's IPv4
# PUBKEYS is a file of SSH public keys for the cloud-init user gaia. Env: STORAGE (local-lvm), BRIDGE (vmbr0), and the
# size: CORES (4), MEMORY (MiB, 4096), ROOT_GB (20), REPO_GB (20). VM 9105 (server B, the M2 upgrade path, PO decision
# 2026-11-17) is never lost.
set -euo pipefail

STORAGE="${STORAGE:-local-lvm}"
BRIDGE="${BRIDGE:-vmbr0}"
CORES="${CORES:-4}"
MEMORY="${MEMORY:-4096}"
ROOT_GB="${ROOT_GB:-20}"
REPO_GB="${REPO_GB:-20}"
here="$(cd "$(dirname "$0")" && pwd)"
# The same dated image as the lab VM (create-vms.sh).
DEBIAN_IMG=/var/lib/vz/template/debian-12-genericcloud-amd64-20261112-2660.qcow2
DEBIAN_IMG_SHA512="485a390d1c5e3a9f57ebb903654c21044c5d5b5b94de126d28b9d0cd960112dc0536afd1217c45036c331659543c8c8acb5f56dff706c9530f9fe39e2908e512"

command -v qm >/dev/null || { echo "run this on a Proxmox host"; exit 3; }

name_of() { qm config "$1" | awk '$1 == "name:" { print $2 }'; }
ours() { [[ "$(name_of "$1")" == gaia-t17-* || "$(name_of "$1")" == gaia-t23-* ]] || { echo "VM $1 is '$(name_of "$1")', not a gaia-t17- or gaia-t23- VM: refusing"; exit 3; }; }

ip_of() {
	local i
	for ((i = 0; i < 60; i++)); do
		"$here/find-ip.sh" "$1" 2>/dev/null && return 0
		sleep 5
	done
	echo "no IPv4 for VM $1 after 300 s" >&2
	return 3
}

case "${1:-}" in
create)
	vmid=${2:?VMID} name=${3:?NAME} keys=${4:?PUBKEYS}
	[[ "$name" == gaia-t17-* || "$name" == gaia-t23-* ]] || { echo "NAME must start with gaia-t17- or gaia-t23-"; exit 3; }
	if qm status "$vmid" >/dev/null 2>&1; then echo "VMID $vmid is in use ($(name_of "$vmid"))"; exit 3; fi
	echo "${DEBIAN_IMG_SHA512}  ${DEBIAN_IMG}" | sha512sum -c --quiet
	qm create "$vmid" --name "$name" --ostype l26 --machine q35 --cpu host --cores "$CORES" --memory "$MEMORY" \
		--scsihw virtio-scsi-single --net0 "virtio,bridge=${BRIDGE}" --serial0 socket --vga serial0 --onboot 0
	qm set "$vmid" --scsi0 "${STORAGE}:0,import-from=${DEBIAN_IMG},iothread=1,discard=on" >/dev/null
	qm resize "$vmid" scsi0 "${ROOT_GB}G"
	# virtio-scsi, not IDE: Debian's cloud kernel has no AHCI driver (create-vms.sh).
	qm set "$vmid" --scsi1 "${STORAGE}:cloudinit" --boot order=scsi0 --ciuser gaia --sshkeys "$keys" --ipconfig0 ip=dhcp >/dev/null
	if [[ "${5:-}" == repo ]]; then
		qm set "$vmid" --scsi2 "${STORAGE}:${REPO_GB},iothread=1,discard=on,serial=gaia-repo" >/dev/null
	fi
	qm config "$vmid" | grep -E '^(name|cores|memory|scsi[0-9]):'
	;;
start)
	vmid=${2:?VMID}
	ours "$vmid"
	qm start "$vmid"
	ip_of "$vmid"
	;;
move-disk)
	from=${2:?FROM} to=${3:?TO}
	ours "$from"
	ours "$to"
	qm disk move "$from" scsi2 --target-vmid "$to" --target-disk scsi2
	qm config "$to" | grep '^scsi2:'
	;;
lose)
	vmid=${2:?VMID}
	[[ "$vmid" != 9105 ]] || { echo "VM 9105 is server B, the M2 upgrade path (PO decision 2026-11-17): refusing"; exit 3; }
	ours "$vmid"
	qm stop "$vmid" --skiplock 1 --timeout 10 2>/dev/null || true
	qm destroy "$vmid" --purge 1
	echo "VM $vmid destroyed"
	;;
ip)
	ip_of "${2:?VMID}"
	;;
*)
	sed -n '2,17p' "$0"
	exit 3
	;;
esac
