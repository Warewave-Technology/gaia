#!/usr/bin/env bash
# Builds an unattended ESXi 8 installer ISO from the stock ISO plus lab/esxi/ks.cfg.
#
# Only three files change: /KS.CFG is added and both boot configs (BIOS and UEFI) get
# "ks=cdrom:/KS.CFG" appended to their kernelopt line. Everything else, including the
# El Torito boot setup, is copied from the stock ISO by xorriso ("-boot_image any replay"),
# so we never have to guess the boot image names. Runs on the Proxmox host (needs xorriso).
#
# The kickstart carries only a SHA-512 crypt hash of the root password, never the password.
#
# Usage: ESXI_ROOT_PASSWORD='...' [ESXI_IP=192.168.1.29] lab/esxi/remaster-iso.sh /var/lib/vz/template/iso/VMware-VMvisor-Installer-8.0U3e-*.iso /var/lib/vz/template/iso/gaia-esxi-ks.iso
# Env: ESXI_IP (empty = DHCP), ESXI_NETMASK (255.255.255.0), ESXI_GATEWAY and ESXI_DNS (default: this
#      host's default gateway and first non-loopback nameserver), ESXI_HOSTNAME (gaia-esxi).
set -euo pipefail

SRC_ISO="${1:?stock ESXi ISO}"
OUT_ISO="${2:?output ISO path}"
: "${ESXI_ROOT_PASSWORD:?set ESXI_ROOT_PASSWORD (ESXi requires 7+ chars, mixed classes)}"
KS_TEMPLATE="$(dirname "$0")/ks.cfg"
ESXI_IP="${ESXI_IP:-}"
ESXI_NETMASK="${ESXI_NETMASK:-255.255.255.0}"
ESXI_GATEWAY="${ESXI_GATEWAY:-$(ip -4 route show default 2>/dev/null | awk '{ print $3; exit }')}"
ESXI_DNS="${ESXI_DNS:-$(awk '$1 == "nameserver" && $2 !~ /^127\./ { print $2; exit }' /etc/resolv.conf 2>/dev/null)}"
ESXI_HOSTNAME="${ESXI_HOSTNAME:-gaia-esxi}"

# A fixed address keeps the host reachable over the operator's VPN, which routes only known IPs.
ipv4='^([0-9]{1,3}\.){3}[0-9]{1,3}$'
if [[ -n "$ESXI_IP" ]]; then
	for v in ESXI_IP ESXI_NETMASK ESXI_GATEWAY ESXI_DNS; do
		[[ "${!v}" =~ $ipv4 ]] || { echo "$v='${!v}' is not an IPv4 address (set it explicitly)"; exit 1; }
	done
	[[ "$ESXI_HOSTNAME" =~ ^[A-Za-z0-9-]+$ ]] || { echo "ESXI_HOSTNAME='$ESXI_HOSTNAME' is not a host name"; exit 1; }
	network="network --bootproto=static --device=vmnic0 --ip=$ESXI_IP --netmask=$ESXI_NETMASK --gateway=$ESXI_GATEWAY --nameserver=$ESXI_DNS --hostname=$ESXI_HOSTNAME"
else
	network="network --bootproto=dhcp --device=vmnic0"
fi
echo "kickstart: $network"

for bin in xorriso openssl; do command -v "$bin" >/dev/null || { echo "missing: $bin (apt install xorriso openssl)"; exit 1; }; done
[[ -f "$SRC_ISO" ]] || { echo "not found: $SRC_ISO"; exit 1; }
[[ ! -e "$OUT_ISO" ]] || { echo "$OUT_ISO exists; remove it first"; exit 1; }
((${#ESXI_ROOT_PASSWORD} >= 7)) || { echo "ESXI_ROOT_PASSWORD must be at least 7 characters"; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Kickstart with the password hash filled in. A crypt hash uses only [A-Za-z0-9./$], all safe for sed with "|".
pw_hash=$(printf '%s' "$ESXI_ROOT_PASSWORD" | openssl passwd -6 -stdin)
sed -e "s|__ROOT_PASSWORD_HASH__|${pw_hash}|" -e "s|^__NETWORK__$|${network}|" "$KS_TEMPLATE" >"$work/KS.CFG"
! grep -q '__[A-Z_]*__' "$work/KS.CFG" || { echo "unfilled placeholder in KS.CFG"; exit 1; }

# Find the boot configs on the ISO, whatever their case. xorriso prints paths quoted.
mapfile -t cfgs < <(xorriso -indev "$SRC_ISO" -find / -type f 2>/dev/null | tr -d "'" | grep -iE '(^|/)boot\.cfg$')
((${#cfgs[@]} > 0)) || { echo "no boot.cfg found on $SRC_ISO"; exit 1; }

map_args=(-map "$work/KS.CFG" /KS.CFG)
i=0
for cfg in "${cfgs[@]}"; do
	local_cfg="$work/boot-$i.cfg"
	xorriso -osirrox on -indev "$SRC_ISO" -extract "$cfg" "$local_cfg" 2>/dev/null
	chmod u+w "$local_cfg"
	grep -q '^kernelopt=' "$local_cfg" || { echo "$cfg has no kernelopt line"; exit 1; }
	echo "$cfg before: $(grep '^kernelopt=' "$local_cfg")"
	# Append, never replace: the stock options (runweasel cdromBoot) start the installer.
	# allowLegacyCPU=true lets the installer continue on a CPU it does not list as supported (nested lab).
	sed -i -E '/^kernelopt=/{/ks=/!s|$| ks=cdrom:/KS.CFG allowLegacyCPU=true|}' "$local_cfg"
	echo "$cfg after:  $(grep '^kernelopt=' "$local_cfg")"
	map_args+=(-map "$local_cfg" "$cfg")
	i=$((i + 1))
done

xorriso -indev "$SRC_ISO" -outdev "$OUT_ISO" "${map_args[@]}" -boot_image any replay 2>"$work/xorriso.log" || {
	cat "$work/xorriso.log"
	exit 1
}
echo "boot images in $OUT_ISO:"
xorriso -indev "$OUT_ISO" -report_el_torito plain 2>/dev/null | grep -E '^El Torito (boot img|img path)'
echo "written: $OUT_ISO"
