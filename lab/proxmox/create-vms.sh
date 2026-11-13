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
#      SSH_PUBKEY (~/.ssh/id_ed25519.pub) for the lab VM's cloud-init user "gaia",
#      LAB_IP (empty = DHCP) with LAB_PREFIX (24), LAB_GATEWAY and LAB_DNS (default: this host's
#      default gateway and first non-loopback nameserver). ESXi's address is set in the ISO
#      (lab/esxi/remaster-iso.sh, ESXI_IP).
set -euo pipefail

STORAGE="${STORAGE:-local-lvm}"
ISO_STORAGE="${ISO_STORAGE:-local}"
BRIDGE="${BRIDGE:-vmbr0}"
ESXI_VMID="${ESXI_VMID:-9101}"
LAB_VMID="${LAB_VMID:-9102}"
ESXI_ISO="${ESXI_ISO:?name of the remastered ESXi ISO in ${ISO_STORAGE}:iso/}"
SSH_PUBKEY="${SSH_PUBKEY:-$HOME/.ssh/id_ed25519.pub}"
LAB_IP="${LAB_IP:-}"
LAB_PREFIX="${LAB_PREFIX:-24}"
LAB_GATEWAY="${LAB_GATEWAY:-$(ip -4 route show default 2>/dev/null | awk '{ print $3; exit }')}"
LAB_DNS="${LAB_DNS:-$(awk '$1 == "nameserver" && $2 !~ /^127\./ { print $2; exit }' /etc/resolv.conf 2>/dev/null)}"
# A dated Debian build, not "latest", so every lab is built from the same bytes.
DEBIAN_BUILD="20261112-2660"
DEBIAN_IMG="debian-12-genericcloud-amd64-${DEBIAN_BUILD}.qcow2"
DEBIAN_IMG_URL="https://cloud.debian.org/images/cloud/bookworm/${DEBIAN_BUILD}/${DEBIAN_IMG}"
DEBIAN_IMG_SHA512="485a390d1c5e3a9f57ebb903654c21044c5d5b5b94de126d28b9d0cd960112dc0536afd1217c45036c331659543c8c8acb5f56dff706c9530f9fe39e2908e512"

command -v qm >/dev/null || { echo "run this on a Proxmox host"; exit 1; }

# A fixed address keeps the lab VM reachable over the operator's VPN, which routes only known IPs.
lab_net=(--ipconfig0 ip=dhcp)
if [[ -n "$LAB_IP" ]]; then
	ipv4='^([0-9]{1,3}\.){3}[0-9]{1,3}$'
	for v in LAB_IP LAB_GATEWAY LAB_DNS; do
		[[ "${!v}" =~ $ipv4 ]] || { echo "$v='${!v}' is not an IPv4 address (set it explicitly)"; exit 1; }
	done
	lab_net=(--ipconfig0 "ip=${LAB_IP}/${LAB_PREFIX},gw=${LAB_GATEWAY}" --nameserver "$LAB_DNS")
fi

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
	# ESXi needs: host CPU (VT-x/AMD-V passthrough), SATA disk, vmxnet3 NIC.
	# The CD is on IDE, with the i440fx machine type: ESXi 8's vmw_ahci sees QEMU's SATA disk
	# but not a SATA CD-ROM, so the installer never finds KS.CFG (seen in T1 on q35:
	# /vmfs/devices/cdrom/ empty, "cannot find kickstart file on cd-rom").
	# UEFI because legacy BIOS boot is deprecated in ESXi 8; no pre-enrolled keys, so Secure Boot is off.
	qm create "$ESXI_VMID" --name gaia-esxi --ostype other --machine pc \
		--bios ovmf --efidisk0 "${STORAGE}:1,efitype=4m,pre-enrolled-keys=0" \
		--cpu host --sockets 1 --cores 4 --memory 16384 --balloon 0 \
		--sata0 "${STORAGE}:200,ssd=1" \
		--ide2 "${ISO_STORAGE}:iso/${ESXI_ISO},media=cdrom" \
		--net0 "vmxnet3,bridge=${BRIDGE}" \
		--boot "order=sata0;ide2" --onboot 0
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
	# The cloud-init drive goes on virtio-scsi, not IDE: on q35, IDE is AHCI, and Debian's cloud
	# kernel has no AHCI driver (CONFIG_SATA_AHCI is not set), so cloud-init never saw an IDE drive.
	qm set "$LAB_VMID" --scsi1 "${STORAGE}:cloudinit" --boot order=scsi0 \
		--ciuser gaia --sshkeys "$SSH_PUBKEY" "${lab_net[@]}"
	echo "gaia-lab created. Start it, then run lab/lab-vm/setup.sh inside:"
	echo "  qm start $LAB_VMID"
fi
