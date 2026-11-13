#!/usr/bin/env bash
# Creates the two Phase 1 lab VMs on a Proxmox host:
#
#   gaia-esxi  nested ESXi 8 (Track A). 4 vCPU, 16 GiB RAM, 200 GiB disk.
#   gaia-lab   Debian 12 runner for the spikes, govc and Gaia builds. 4 vCPU, 8 GiB RAM, 200 GiB disk.
#
# Both VMs live on the same Proxmox host, next to (not inside) the House lab, and are
# powered on only while a test runs. Re-running is safe: existing VMIDs are skipped.
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
DEBIAN_IMG_URL="https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2"

command -v qm >/dev/null || { echo "run this on a Proxmox host"; exit 1; }

# Nested virtualization must be on, or ESXi will not run at all.
nested=$(cat /sys/module/kvm_intel/parameters/nested 2>/dev/null || cat /sys/module/kvm_amd/parameters/nested 2>/dev/null || echo N)
if [[ "$nested" != "Y" && "$nested" != "1" ]]; then
	echo "nested virtualization is off. Enable it, e.g. for Intel:"
	echo "  echo 'options kvm-intel nested=Y' > /etc/modprobe.d/kvm-intel.conf && reboot"
	exit 1
fi

exists() { qm status "$1" >/dev/null 2>&1; }

if exists "$ESXI_VMID"; then
	echo "gaia-esxi ($ESXI_VMID) exists, skipping"
else
	# ESXi needs: host CPU (VT-x passthrough), SATA disk, vmxnet3 NIC, q35.
	qm create "$ESXI_VMID" --name gaia-esxi --ostype other --machine q35 --bios seabios \
		--cpu host --sockets 1 --cores 4 --memory 16384 --balloon 0 \
		--sata0 "${STORAGE}:200,ssd=1" \
		--sata1 "${ISO_STORAGE}:iso/${ESXI_ISO},media=cdrom" \
		--net0 "vmxnet3,bridge=${BRIDGE}" \
		--boot "order=sata0;sata1" --onboot 0
	echo "gaia-esxi created. Start it; the install is unattended (watch the console):"
	echo "  qm start $ESXI_VMID"
fi

if exists "$LAB_VMID"; then
	echo "gaia-lab ($LAB_VMID) exists, skipping"
else
	[[ -f "$SSH_PUBKEY" ]] || { echo "SSH_PUBKEY $SSH_PUBKEY not found"; exit 1; }
	img="/var/lib/vz/template/gaia-debian-12.qcow2"
	[[ -f "$img" ]] || wget -q -O "$img" "$DEBIAN_IMG_URL"
	qm create "$LAB_VMID" --name gaia-lab --ostype l26 --machine q35 \
		--cpu host --cores 4 --memory 8192 \
		--scsihw virtio-scsi-single --net0 "virtio,bridge=${BRIDGE}" \
		--serial0 socket --vga serial0 --agent enabled=1 --onboot 0
	qm set "$LAB_VMID" --scsi0 "${STORAGE}:0,import-from=${img},iothread=1,discard=on"
	qm resize "$LAB_VMID" scsi0 200G
	qm set "$LAB_VMID" --ide2 "${STORAGE}:cloudinit" --boot order=scsi0 \
		--ciuser gaia --sshkeys "$SSH_PUBKEY" --ipconfig0 ip=dhcp
	echo "gaia-lab created. Start it, then run lab/lab-vm/setup.sh inside:"
	echo "  qm start $LAB_VMID"
fi
