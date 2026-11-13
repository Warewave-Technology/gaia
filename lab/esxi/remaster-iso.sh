#!/usr/bin/env bash
# Builds an unattended ESXi 8 installer ISO from the stock ISO plus lab/esxi/ks.cfg.
# The boot config is changed to load the kickstart from the CD, so the install
# needs no keyboard. Runs on the Proxmox host (needs xorriso + genisoimage).
#
# Usage: ESXI_ROOT_PASSWORD='...' lab/esxi/remaster-iso.sh /path/VMware-VMvisor-Installer-8.0U3e-*.iso /var/lib/vz/template/iso/gaia-esxi-ks.iso
set -euo pipefail

SRC_ISO="${1:?stock ESXi ISO}"
OUT_ISO="${2:?output ISO path}"
: "${ESXI_ROOT_PASSWORD:?set ESXI_ROOT_PASSWORD (ESXi requires 7+ chars, mixed classes)}"
KS_TEMPLATE="$(dirname "$0")/ks.cfg"

for bin in xorriso genisoimage; do command -v "$bin" >/dev/null || { echo "missing: $bin (apt install xorriso genisoimage)"; exit 1; }; done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
xorriso -osirrox on -indev "$SRC_ISO" -extract / "$work/iso" 2>/dev/null
chmod -R u+w "$work/iso"

# Kickstart with the password filled in. File names on the ESXi ISO are upper case.
pw_escaped=$(printf '%s' "$ESXI_ROOT_PASSWORD" | sed 's/[&|\\]/\\&/g') # keep &, | and \ literal for sed
sed "s|__ROOT_PASSWORD__|${pw_escaped}|" "$KS_TEMPLATE" >"$work/iso/KS.CFG"

# BIOS and UEFI boot configs: load the kickstart, keep the system partition small.
for cfg in "$work/iso/BOOT.CFG" "$work/iso/EFI/BOOT/BOOT.CFG"; do
	[[ -f "$cfg" ]] || continue
	sed -i 's|^kernelopt=.*|kernelopt=ks=cdrom:/KS.CFG systemMediaSize=min|' "$cfg"
done

genisoimage -relaxed-filenames -J -R -quiet -o "$OUT_ISO" \
	-b ISOLINUX.BIN -c BOOT.CAT -no-emul-boot -boot-load-size 4 -boot-info-table \
	-eltorito-alt-boot -e EFIBOOT.IMG -no-emul-boot \
	"$work/iso"
echo "written: $OUT_ISO"
