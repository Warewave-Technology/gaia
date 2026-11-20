#!/usr/bin/env bash
# fixture: the VMs of the T25 gate (parallel restore tests, ADR-007 C4), on gaia-lab. Not part of the product: Gaia
# only reads these VMs (backups) and boots copies of them in its sandbox.
#
#   create     the network gaia-t25 (NAT, 192.168.225.0/24) and the VMs that do not exist yet:
#                gaia-t25-iso-1..4   Debian 12, 384 MiB: they carry the isolation probe (below)
#                gaia-t25-lin-1..6   Debian 12, 384 MiB: the set whose tests are timed with 1, 2 and 4 slots
#                gaia-t25-win        T11's Windows Server 2022 converted for KVM as in t23-guests.sh (virt-v2v in
#                                    the pinned converter image, smbus removed), 2 GiB
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
WIN_SRC=/var/lib/gaia/images/gaia-t11-win.qcow2
VIRTIO_ISO=/var/lib/gaia/images/virtio-win-0.1.302.iso
VIRTIO_SHA256=303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d
CONV_USER=gaia-convert
CONV_HOME=/var/lib/gaia-convert
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

# windows_guest: T11's Windows Server 2022 converted for KVM by virt-v2v in the pinned converter image, as
# t23-guests.sh does it (with virtio-win 0.1.302, then the smbus driver removed); a copy of T23's running VM would not
# be consistent.
windows_guest() {
	local n=gaia-t25-win w=$CONV_HOME/t25-win image root
	echo "$VIRTIO_SHA256  $VIRTIO_ISO" | sha256sum -c --quiet
	image=$(jq -r .image /etc/gaia/convert.json)
	rm -rf "$w" && install -d -o "$CONV_USER" -g "$CONV_USER" "$w" "$w/out"
	cp --reflink=auto "$WIN_SRC" "$w/in.qcow2"
	cp "$VIRTIO_ISO" "$w/virtio-win.iso" && chown "$CONV_USER:" "$w/in.qcow2" "$w/virtio-win.iso"
	log "converting $WIN_SRC with virt-v2v in $image"
	systemd-run --wait --quiet --collect --uid="$CONV_USER" --gid="$CONV_USER" -p SupplementaryGroups=kvm -p RuntimeDirectory=gaia-convert \
		-E HOME="$CONV_HOME" -E XDG_RUNTIME_DIR=/run/gaia-convert -E TMPDIR="$CONV_HOME/tmp" \
		bash -c "podman --cgroup-manager=cgroupfs run --rm --init --network=none --cgroups=disabled --cap-drop=all \
			--security-opt=no-new-privileges --userns=keep-id --group-add=keep-groups --device=/dev/kvm \
			--volume=$w/in.qcow2:/in/disk.qcow2:ro --volume=$w/out:/out --volume=$w/virtio-win.iso:/virtio-win.iso:ro \
			--env=VIRTIO_WIN=/virtio-win.iso $image virt-v2v -i disk /in/disk.qcow2 -o local -os /out -of qcow2 -on $n \
			>$w/virt-v2v.log 2>&1"
	tail -3 "$w/virt-v2v.log"
	mv "$w/out/$n-sda" "$LV/$n-vda.qcow2"
	rm -f "$w/in.qcow2" "$w/virtio-win.iso"
	root=$(LIBGUESTFS_BACKEND=direct guestfish --ro -a "$LV/$n-vda.qcow2" -i inspect-get-roots | head -1)
	printf '%s\n' run "ntfsfix $root" "mount $root /" 'glob rm-f /Windows/Drivers/VirtIO/smbus*' >"$D/no-smbus.gf"
	LIBGUESTFS_BACKEND=direct guestfish --rw -a "$LV/$n-vda.qcow2" -f "$D/no-smbus.gf"
	chown libvirt-qemu:libvirt-qemu "$LV/$n-vda.qcow2"
	virsh -q define /dev/stdin <<EOF
<domain type='kvm'><name>$n</name><memory unit='MiB'>2048</memory><vcpu>2</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><features><acpi/><apic/><hyperv><relaxed state='on'/><vapic state='on'/><spinlocks state='on' retries='8191'/></hyperv></features>
  <cpu mode='host-passthrough'/><clock offset='localtime'/>
  <devices>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2' discard='unmap'/><source file='$LV/$n-vda.qcow2'/><target dev='vda' bus='virtio'/></disk>
    <interface type='network'><source network='$NET'/><model type='virtio'/></interface>
    <serial type='pty'/><console type='pty'/><input type='tablet' bus='usb'/><graphics type='vnc' listen='127.0.0.1'/><video><model type='vga'/></video>
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
