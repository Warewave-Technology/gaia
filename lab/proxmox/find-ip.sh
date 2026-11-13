#!/usr/bin/env bash
# Prints the IPv4 address a Proxmox VM got from DHCP, found by its MAC address.
#
# Neither lab VM runs a guest agent Proxmox could ask (ESXi has none; the Debian cloud
# image does not ship qemu-guest-agent), so we ping the bridge's /24 once to fill the
# neighbour table and look the VM's MAC up in it. ESXi's vmk0 uses vmnic0's MAC.
#
# Usage (as root on the Proxmox host):  lab/proxmox/find-ip.sh 9101
# Exit: 0 found, 3 not found (UNKNOWN: the VM may still be booting).
set -euo pipefail

VMID="${1:?VMID}"
BRIDGE="${BRIDGE:-vmbr0}"

mac=$(qm config "$VMID" | sed -nE 's/^net0: [a-z0-9]+=([0-9A-Fa-f:]{17}),.*/\1/p')
[[ -n "$mac" ]] || { echo "VM $VMID has no net0 MAC"; exit 3; }
cidr=$(ip -4 -o addr show "$BRIDGE" | awk '{ print $4 }' | head -1)
[[ -n "$cidr" ]] || { echo "$BRIDGE has no IPv4 address"; exit 3; }
prefix="${cidr%.*}"

for i in $(seq 1 254); do ping -c 1 -W 1 "$prefix.$i" >/dev/null 2>&1 & done
wait

ip=$(ip -4 neigh show dev "$BRIDGE" | awk -v m="${mac,,}" 'tolower($3) == m && $1 != "" { print $1 }' | head -1)
[[ -n "$ip" ]] || { echo "no IPv4 for $mac on $BRIDGE (VM $VMID still booting?)"; exit 3; }
echo "$ip"
