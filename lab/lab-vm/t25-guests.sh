#!/usr/bin/env bash
# fixture: the VMs of the T25 gate (parallel restore tests, ADR-007 C4), on gaia-lab. Not part of the product: Gaia
# only reads these VMs (backups) and boots copies of them in its sandbox.
#
#   create     the network gaia-t25 (NAT, 192.168.225.0/24) and the VMs that do not exist yet:
#                gaia-t25-iso-1..4   Debian 12, 384 MiB: they carry the isolation probe (below)
#                gaia-t25-lin-1..6   Debian 12, 384 MiB: the set whose tests are timed with 1, 2 and 4 slots
#                gaia-t25-win        a copy of T23's converted Windows Server 2022 (virt-v2v output in
#                                    /var/lib/gaia-convert/t23-win, smbus removed as in t23-guests.sh), 2 GiB
#   status     each VM's state and address
#   destroy    the VMs, their disks and the network
#
# The isolation probe (gaia-isolation-probe.service) runs at boot only where the guest leased a sandbox address
# (10.254.<slot>.x): for 120 s it tries to reach, by ICMP and TCP 22, 53 and 443, its own slot's probe address (the
# positive control: it must answer), every other slot's probe address and guest address (.10: dnsmasq hands the
# range out in order and a slot has one guest), the lab host, the
# LAN's gateway and the outside, and prints one line per target to the serial console:
#   GAIA-ISOLATION slot=<s> own=<ip> target=<ip> reached=<yes|no> via=<icmp,tcp22,...|->
# then GAIA-ISOLATION-END. The serial login prompt comes after it, so the restore test's boot check waits for the
# probe and the bundle's serial.log holds its result.
#
# Usage (on gaia-lab, as root): lab/lab-vm/t25-guests.sh create | status | destroy
set -euo pipefail

D=/root/t25-guests
NET=gaia-t25
LV=/var/lib/libvirt/images
CLOUD_IMG=/var/lib/gaia/images/debian-12-genericcloud-amd64.qcow2
WIN_SRC_DIR=/var/lib/gaia-convert/t23-win
set -E
trap 'echo "UNKNOWN: fixture failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR
((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

names() {
	for i in 1 2 3 4; do echo "gaia-t25-iso-$i"; done
	for i in 1 2 3 4 5 6; do echo "gaia-t25-lin-$i"; done
	echo gaia-t25-win
}

network() {
	virsh net-info "$NET" >/dev/null 2>&1 && return
	cat >"$D/$NET.xml" <<EOF
<network><name>$NET</name><forward mode='nat'/><bridge name='virbr-t25' stp='on' delay='0'/>
  <ip address='192.168.225.1' netmask='255.255.255.0'><dhcp><range start='192.168.225.100' end='192.168.225.200'/></dhcp></ip></network>
EOF
	virsh -q net-define "$D/$NET.xml" && virsh -q net-autostart "$NET" && virsh -q net-start "$NET"
}

probe_script() {
	cat <<'EOF'
#!/bin/bash
# T25 isolation probe (lab/lab-vm/t25-guests.sh): only in a Gaia sandbox slot.
own=$(ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1 | head -1)
case "$own" in 10.254.*) ;; *) exit 0 ;; esac
slot=$(cut -d. -f3 <<<"$own")
targets="10.254.$slot.1"
for s in 0 1 2 3 4 5 6 7; do
  [ "$s" = "$slot" ] && continue
  targets="$targets 10.254.$s.1 10.254.$s.10"
done
targets="$targets 192.168.1.30 192.168.1.1 192.168.1.99 192.168.225.1 1.1.1.1 9.9.9.9"
declare -A via
end=$((SECONDS + 120))
while [ $SECONDS -lt $end ]; do
  for t in $targets; do
    ping -c1 -W1 "$t" >/dev/null 2>&1 && via[$t]="${via[$t]:+${via[$t]},}icmp"
    for p in 22 53 443; do
      timeout 1 bash -c "</dev/tcp/$t/$p" 2>/dev/null && via[$t]="${via[$t]:+${via[$t]},}tcp$p"
    done
  done
  sleep 5
done
for t in $targets; do
  v=$(tr ',' '\n' <<<"${via[$t]:-}" | sort -u | paste -sd, -)
  r=no; [ -n "$v" ] && r=yes
  echo "GAIA-ISOLATION slot=$slot own=$own target=$t reached=$r via=${v:--}" | tee /dev/ttyS0
done
echo "GAIA-ISOLATION-END slot=$slot own=$own" | tee /dev/ttyS0
EOF
}

linux_guest() { # NAME PROBE(0|1)
	local n=$1 probe=$2 w=$D/$1
	mkdir -p "$w"
	qemu-img convert -q -f qcow2 -O qcow2 "$CLOUD_IMG" "$LV/$n-vda.qcow2"
	qemu-img resize -q "$LV/$n-vda.qcow2" 4G
	{
		echo "#cloud-config"
		echo "hostname: $n"
		echo "packages: [iputils-ping]"
		if ((probe)); then
			echo "write_files:"
			echo "  - path: /usr/local/sbin/gaia-isolation-probe"
			echo "    permissions: '0755'"
			echo "    content: |"
			probe_script | sed 's/^/      /'
			cat <<'EOF'
  - path: /etc/systemd/system/gaia-isolation-probe.service
    content: |
      [Unit]
      Description=Gaia T25 isolation probe (sandbox only)
      Wants=network-online.target
      After=network-online.target
      Before=serial-getty@ttyS0.service getty.target
      [Service]
      Type=oneshot
      ExecStart=/usr/local/sbin/gaia-isolation-probe
      TimeoutStartSec=300
      [Install]
      WantedBy=multi-user.target
runcmd:
  - [systemctl, daemon-reload]
  - [systemctl, enable, gaia-isolation-probe.service]
EOF
		fi
	} >"$w/user-data"
	printf 'instance-id: %s\nlocal-hostname: %s\n' "$n" "$n" >"$w/meta-data"
	cloud-localds "$LV/$n-seed.img" "$w/user-data" "$w/meta-data"
	chown libvirt-qemu:libvirt-qemu "$LV/$n"-*
	virsh -q define /dev/stdin <<EOF
<domain type='kvm'><name>$n</name><memory unit='MiB'>384</memory><vcpu>1</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><features><acpi/><apic/></features>
  <cpu mode='host-passthrough'/><on_poweroff>destroy</on_poweroff>
  <devices>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2' discard='unmap'/><source file='$LV/$n-vda.qcow2'/><target dev='vda' bus='virtio'/></disk>
    <disk type='file' device='disk'><driver name='qemu' type='raw'/><source file='$LV/$n-seed.img'/><target dev='vdz' bus='virtio'/><readonly/></disk>
    <interface type='network'><source network='$NET'/><model type='virtio'/></interface>
    <serial type='pty'/><console type='pty'/>
  </devices></domain>
EOF
	virsh -q autostart "$n" && virsh -q start "$n"
}

windows_guest() {
	local n=gaia-t25-win src
	src=$(find "$WIN_SRC_DIR" -maxdepth 2 -type f -name '*-sda' 2>/dev/null | head -1)
	[[ -n "$src" ]] || { echo "UNKNOWN: no converted Windows disk in $WIN_SRC_DIR (t23-guests.sh create made it)"; exit 3; }
	qemu-img convert -q -O qcow2 "$src" "$LV/$n-vda.qcow2"
	# as t23-guests.sh no_smbus: virtio-win 0.1.302's smbus driver restarts Server 2022 for ever
	local root
	root=$(LIBGUESTFS_BACKEND=direct guestfish --ro -a "$LV/$n-vda.qcow2" -i inspect-get-roots | head -1)
	printf '%s\n' run "ntfsfix $root" "mount $root /" 'glob rm-f /Windows/Drivers/VirtIO/smbus*' >"$D/no-smbus.gf"
	LIBGUESTFS_BACKEND=direct guestfish --rw -a "$LV/$n-vda.qcow2" -f "$D/no-smbus.gf"
	chown libvirt-qemu:libvirt-qemu "$LV/$n-vda.qcow2"
	virsh -q define /dev/stdin <<EOF
<domain type='kvm'><name>$n</name><memory unit='MiB'>2048</memory><vcpu>2</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><features><acpi/><apic/><hyperv><relaxed state='on'/></hyperv></features>
  <cpu mode='host-passthrough'/><clock offset='localtime'/><on_poweroff>destroy</on_poweroff>
  <devices>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2' discard='unmap'/><source file='$LV/$n-vda.qcow2'/><target dev='vda' bus='virtio'/></disk>
    <interface type='network'><source network='$NET'/><model type='virtio'/></interface>
    <graphics type='vnc' listen='127.0.0.1'/><video><model type='vga'/></video>
  </devices></domain>
EOF
	virsh -q autostart "$n" && virsh -q start "$n"
}

case "${1:-}" in
create)
	mkdir -p "$D"
	network
	for n in $(names); do
		virsh dominfo "$n" >/dev/null 2>&1 && continue
		log "creating $n"
		case "$n" in
		gaia-t25-iso-*) linux_guest "$n" 1 ;;
		gaia-t25-lin-*) linux_guest "$n" 0 ;;
		gaia-t25-win) windows_guest ;;
		esac
	done
	"$0" status
	;;
status)
	for n in $(names); do
		ip=$(virsh -q domifaddr "$n" 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1)
		printf '%s: %s, %s\n' "$n" "$(virsh domstate "$n" 2>/dev/null || echo missing)" "${ip:--}"
	done
	;;
destroy)
	for n in $(names); do
		virsh -q destroy "$n" 2>/dev/null || true
		virsh -q undefine "$n" 2>/dev/null || true
		rm -f "$LV/$n"-*
	done
	virsh -q net-destroy "$NET" 2>/dev/null || true
	virsh -q net-undefine "$NET" 2>/dev/null || true
	rm -rf "$D"
	fstrim -a 2>/dev/null || true
	;;
*)
	sed -n '2,22p' "$0"
	exit 3
	;;
esac
