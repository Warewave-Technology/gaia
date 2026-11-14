#!/usr/bin/env bash
# T5 only: formats and mounts gaia-lab's second disk (Proxmox scsi2) at /mnt/gaia-t5, so the restore
# investigation can write its target to a different disk than the one holding the repository.
# Add the disk on the Proxmox host first (see the T5 report), then run this on gaia-lab as root.
# It formats only a disk that carries no signature at all, or one it formatted itself (label gaia-t5).
# Not persistent: after a reboot, run it again (it mounts without formatting).
set -euo pipefail

DEV="${DEV:-/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_drive-scsi2}"
MNT=/mnt/gaia-t5

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
[[ -b "$DEV" ]] || { echo "no disk at $DEV: add it on the Proxmox host first (qm set ... --scsi2 ...)"; exit 3; }
mountpoint -q "$MNT" && { echo "$MNT already mounted"; df -h "$MNT"; exit 0; }
label=$(blkid -o value -s LABEL "$DEV" 2>/dev/null || true)
if [[ "$label" != gaia-t5 ]]; then
	if [[ -n "$(blkid -p -o value -s TYPE "$DEV" 2>/dev/null || true)" ]] || [[ -n "$(lsblk -no NAME "$DEV" | tail -n +2)" ]]; then
		echo "refusing: $DEV has a filesystem or partitions that this script did not create"
		lsblk -f "$DEV"
		exit 3
	fi
	mkfs.ext4 -q -L gaia-t5 "$DEV"
fi
mkdir -p "$MNT"
mount -o noatime "$DEV" "$MNT"
lsblk -o NAME,SIZE,FSTYPE,LABEL,MOUNTPOINT "$(readlink -f "$DEV")"
df -h "$MNT"
