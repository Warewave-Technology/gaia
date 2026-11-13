#!/usr/bin/env bash
# Creates the two Phase 1 lab VMs on a Proxmox host:
#
#   gaia-esxi  nested ESXi 8 (Track A). 4 vCPU, 16 GiB RAM, 200 GiB disk, UEFI.
#   gaia-lab   Debian 12 runner for the spikes, govc and Gaia builds. 4 vCPU, 8 GiB RAM, 200 GiB disk.
#
# Both VMs live on the same Proxmox host, next to (not inside) the House lab, and are
# powered on only while a test runs. Re-running is safe: a VMID that already holds the
# VM of the same name is skipped; a VMID used by anything else stops the script.
#
# Usage (as root on the Proxmox host):
#   ESXI_ISO=gaia-esxi-ks.iso lab/proxmox/create-vms.sh
# Env: STORAGE (local-lvm), ISO_STORAGE (local), BRIDGE (vmbr0), ESXI_VMID (9101), LAB_VMID (9102),
#      SSH_PUBKEY (~/.ssh/id_ed25519.pub) for the lab VM's cloud-init user "gaia".
set -euo pipefail

STORAGE="${STORAGE:-local-lvm}"
ISO_STORAGE="${ISO_STORAGE:-local}"
BRIDGE="${BRIDGE:-vmbr0}"
ESXI_VMID="${ESXI_VMID:-9101}"
LAB_VMID="${LAB_VMID:-9102}"
ESXI_ISO="${ESXI_ISO:?name of the remastered ESXi ISO in ${ISO_STORAGE}:iso/}"
SSH_PUBKEY="${SSH_PUBKEY:-$HOME/.ssh/id_ed25519.pub}"
# A dated Debian build, not "latest", so every lab is built from the same bytes.
DEBIAN_BUILD="20261112-2660"
DEBIAN_IMG="debian-12-genericcloud-amd64-${DEBIAN_BUILD}.qcow2"
DEBIAN_IMG_URL="https://cloud.debian.org/images/cloud/bookworm/${DEBIAN_BUILD}/${DEBIAN_IMG}"
DEBIAN_IMG_SHA512="485a390d1c5e3a9f57ebb903654c21044c5d5b5b94de126d28b9d0cd960112dc0536afd1217c45036c331659543c8c8acb5f56dff706c9530f9fe39e2908e512"

command -v qm >/dev/null || { echo "run this on a Proxmox host"; exit 1; }

# Nested virtualization must be on, or ESXi will not run at all.
nested=$(cat /sys/module/kvm_intel/parameters/nested 2>/dev/null || cat /sys/module/kvm_amd/parameters/nested 2>/dev/null || echo N)
if [[ "$nested" != "Y" && "$nested" != "1" ]]; then
	echo "nested virtualization is off. Enable it, e.g. for Intel:"
	echo "  echo 'options kvm-intel nested=Y' > /etc/modprobe.d/kvm-intel.conf && reboot"
	exit 1
fi

# exists VMID NAME: true if VMID holds NAME, false if free, exits if it holds something else.
exists() {
	local name
	qm status "$1" >/dev/null 2>&1 || return 1
	name=$(qm config "$1" | awk '$1 == "name:" { print $2 }')
	[[ "$name" == "$2" ]] || { echo "VMID $1 is already used by '$name', not $2; set a different VMID"; exit 1; }
}

if exists "$ESXI_VMID" gaia-esxi; then
	echo "gaia-esxi ($ESXI_VMID) exists, skipping"
else
	# ESXi needs: host CPU (VT-x/AMD-V passthrough), SATA disk, vmxnet3 NIC, q35.
	# UEFI because legacy BIOS boot is deprecated in ESXi 8; no pre-enrolled keys, so Secure Boot is off.
	qm create "$ESXI_VMID" --name gaia-esxi --ostype other --machine q35 \
		--bios ovmf --efidisk0 "${STORAGE}:1,efitype=4m,pre-enrolled-keys=0" \
		--cpu host --sockets 1 --cores 4 --memory 16384 --balloon 0 \
		--sata0 "${STORAGE}:200,ssd=1" \
		--sata1 "${ISO_STORAGE}:iso/${ESXI_ISO},media=cdrom" \
		--net0 "vmxnet3,bridge=${BRIDGE}" \
		--boot "order=sata0;sata1" --onboot 0
	echo "gaia-esxi created. Start it; the install is unattended (watch the console):"
	echo "  qm start $ESXI_VMID"
fi

if exists "$LAB_VMID" gaia-lab; then
	echo "gaia-lab ($LAB_VMID) exists, skipping"
else
	[[ -f "$SSH_PUBKEY" ]] || { echo "SSH_PUBKEY $SSH_PUBKEY not found"; exit 1; }
	img="/var/lib/vz/template/${DEBIAN_IMG}"
	if [[ ! -f "$img" ]]; then
		wget -q -O "$img.part" "$DEBIAN_IMG_URL"
		mv "$img.part" "$img"
	fi
	echo "${DEBIAN_IMG_SHA512}  ${img}" | sha512sum -c --quiet || { echo "checksum mismatch: $img"; rm -f "$img"; exit 1; }
	qm create "$LAB_VMID" --name gaia-lab --ostype l26 --machine q35 \
		--cpu host --cores 4 --memory 8192 \
		--scsihw virtio-scsi-single --net0 "virtio,bridge=${BRIDGE}" \
		--serial0 socket --vga serial0 --onboot 0
	qm set "$LAB_VMID" --scsi0 "${STORAGE}:0,import-from=${img},iothread=1,discard=on"
	qm resize "$LAB_VMID" scsi0 200G
	qm set "$LAB_VMID" --ide2 "${STORAGE}:cloudinit" --boot order=scsi0 \
		--ciuser gaia --sshkeys "$SSH_PUBKEY" --ipconfig0 ip=dhcp
	echo "gaia-lab created. Start it, then run lab/lab-vm/setup.sh inside:"
	echo "  qm start $LAB_VMID"
fi
