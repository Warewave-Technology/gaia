#!/usr/bin/env bash
# fixture: the VMs the T23 MVP acceptance protects, on gaia-lab (the KVM host), and the helpers that break them for
# the acceptance scenarios. Not part of the product: Gaia only reads these VMs (backups) and boots copies of them.
#
#   create              (each VM that does not exist yet) the network gaia-t23 (NAT, 192.168.223.0/24, starts with the host) and three running VMs:
#                       gaia-t23-linux2   Debian 12, BIOS, two disks (vda system, vdb data at /data), writing all the time
#                       gaia-t23-uefi     Debian 12, UEFI only (OVMF), one disk
#                       gaia-t23-win      T11's Windows Server 2022 disk (/var/lib/gaia/images/gaia-t11-win.qcow2, built
#                                         with VMware device models) converted for KVM by virt-v2v in the pinned
#                                         converter image, with virtio-win 0.1.302: virtio disk and network
#   status              each VM's state, address and disks
#   break-boot NAME     a guest made unbootable: zeroes the boot code of its first disk's MBR from inside the running
#                       guest (Linux, BIOS), keeping a copy; the VM keeps running, the next backup carries it
#   fix-boot NAME       writes the kept boot code back
#   delete-checkpoint NAME   deletes the VM's newest checkpoint (Gaia's) on the host, as a careless admin would
#   destroy             the three VMs, their disks and the network
#
# Usage (on gaia-lab, as root): lab/lab-vm/t23-guests.sh create | status | ...
# Exit: 0 OK / 3 UNKNOWN (not done).
set -euo pipefail

D=/root/t23-guests
NET=gaia-t23
LV=/var/lib/libvirt/images
IMAGES=/var/lib/gaia/images
CLOUD_IMG=$IMAGES/debian-12-genericcloud-amd64.qcow2
WIN_SRC=$IMAGES/gaia-t11-win.qcow2
VIRTIO_ISO=$IMAGES/virtio-win-0.1.302.iso
VIRTIO_SHA256=303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d
CONV_USER=gaia-convert
CONV_HOME=/var/lib/gaia-convert
declare -A IP=([gaia-t23-linux2]=192.168.223.11 [gaia-t23-uefi]=192.168.223.12 [gaia-t23-win]=192.168.223.13)
declare -A MAC=([gaia-t23-linux2]=52:54:00:23:00:11 [gaia-t23-uefi]=52:54:00:23:00:12 [gaia-t23-win]=52:54:00:23:00:13)
set -E
trap 'echo "UNKNOWN: fixture failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
sshopts=(-i "$D/ssh/id_ed25519" -o UserKnownHostsFile="$D/ssh/known_hosts" -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=10)
in_guest() { ssh "${sshopts[@]}" "debian@${IP[$1]}" sudo bash -s; } # NAME: stdin as root in a Linux guest

network() {
	virsh net-info "$NET" >/dev/null 2>&1 && return
	local f=$D/$NET.xml hosts="" n
	for n in "${!IP[@]}"; do hosts+="<host mac='${MAC[$n]}' name='$n' ip='${IP[$n]}'/>"; done
	cat >"$f" <<EOF
<network><name>$NET</name><forward mode='nat'/><bridge name='virbr-t23' stp='on' delay='0'/>
  <ip address='192.168.223.1' netmask='255.255.255.0'><dhcp><range start='192.168.223.100' end='192.168.223.150'/>$hosts</dhcp></ip></network>
EOF
	virsh -q net-define "$f" && virsh -q net-autostart "$NET" && virsh -q net-start "$NET"
}

# linux_guest NAME FIRMWARE DATA_DISK: a Debian cloud VM; DATA_DISK=1 adds vdb with a writer, like T17's guest.
linux_guest() {
	local n=$1 fw=$2 data=$3 w=$D/$1 os
	mkdir -p "$w"
	qemu-img convert -q -f qcow2 -O qcow2 "$CLOUD_IMG" "$LV/$n-vda.qcow2"
	qemu-img resize -q "$LV/$n-vda.qcow2" 6G
	local disks="<disk type='file' device='disk'><driver name='qemu' type='qcow2' discard='unmap'/><source file='$LV/$n-vda.qcow2'/><target dev='vda' bus='virtio'/></disk>"
	if ((data)); then
		qemu-img create -q -f qcow2 "$LV/$n-vdb.qcow2" 2G
		disks+="<disk type='file' device='disk'><driver name='qemu' type='qcow2' discard='unmap'/><source file='$LV/$n-vdb.qcow2'/><target dev='vdb' bus='virtio'/><serial>gaia-data</serial></disk>"
	fi
	cat >"$w/user-data" <<EOF
#cloud-config
hostname: $n
ssh_authorized_keys:
  - $(cat "$D/ssh/id_ed25519.pub")
write_files:
  - path: /usr/local/sbin/gaia-churn
    permissions: '0755'
    content: |
      #!/bin/bash
      # T23 workload: a little data all the time (on /data when the VM has its data disk, serial gaia-data; the
      # cloud-init seed is a virtio disk too, so a disk name like vdb says nothing)
      set -eu
      dir=/var/lib/gaia-churn
      data=/dev/disk/by-id/virtio-gaia-data
      if [ -b \$data ]; then
        blkid \$data >/dev/null 2>&1 || mkfs.ext4 -q -L gaia-data \$data
        grep -q gaia-data /etc/fstab || echo 'LABEL=gaia-data /data ext4 defaults 0 2' >> /etc/fstab
        mkdir -p /data && mount -a && dir=/data
      fi
      mkdir -p \$dir
      while true; do
        head -c 65536 /dev/urandom >> \$dir/app.log && sync \$dir/app.log
        [ "\$(stat -c %s \$dir/app.log)" -lt 268435456 ] || mv \$dir/app.log \$dir/app.log.1
        sleep 5
      done
  - path: /etc/systemd/system/gaia-churn.service
    content: |
      [Unit]
      Description=Gaia T23 test workload
      [Service]
      ExecStart=/usr/local/sbin/gaia-churn
      [Install]
      WantedBy=multi-user.target
runcmd:
  - [systemctl, daemon-reload]
  - [systemctl, enable, --now, gaia-churn.service]
EOF
	printf 'instance-id: %s\nlocal-hostname: %s\n' "$n" "$n" >"$w/meta-data"
	cloud-localds "$LV/$n-seed.img" "$w/user-data" "$w/meta-data"
	os="<os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os>"
	[[ "$fw" == efi ]] && os="<os firmware='efi'><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os>"
	chown libvirt-qemu:libvirt-qemu "$LV/$n"-*
	# The seed is a read-only virtio disk (Debian's cloud kernel reads no SATA CD-ROM, T16); Gaia does not back up a
	# read-only disk.
	virsh -q define /dev/stdin <<EOF
<domain type='kvm'><name>$n</name><memory unit='MiB'>768</memory><vcpu>1</vcpu>$os<features><acpi/><apic/></features>
  <cpu mode='host-passthrough'/><on_poweroff>destroy</on_poweroff>
  <devices>$disks
    <disk type='file' device='disk'><driver name='qemu' type='raw'/><source file='$LV/$n-seed.img'/><target dev='vdz' bus='virtio'/><readonly/></disk>
    <interface type='network'><source network='$NET'/><mac address='${MAC[$n]}'/><model type='virtio'/></interface>
    <serial type='pty'/><console type='pty'/>
  </devices></domain>
EOF
	virsh -q autostart "$n" && virsh -q start "$n"
}

# no_smbus DISK: virtio-win 0.1.302's smbus driver does not install on Windows Server 2022 ("terminated in a root
# certificate which is not trusted"), and virt-v2v's first-boot script then exits 249, "retrying on next boot": Windows
# restarts on every boot, for ever (found in T23). The driver is not needed; without it the script ends.
no_smbus() {
	printf '%s\n' 'glob rm-f /Windows/Drivers/Virtio/smbus*' 'ls /Windows/Drivers/Virtio' >"$D/no-smbus.gf"
	LIBGUESTFS_BACKEND=direct guestfish --rw -a "$1" -i -f "$D/no-smbus.gf" | paste -sd ' ' -
}

windows_guest() {
	local n=gaia-t23-win w=$CONV_HOME/t23-win image # gaia-convert cannot enter /root
	echo "$VIRTIO_SHA256  $VIRTIO_ISO" | sha256sum -c --quiet
	image=$(jq -r .image /etc/gaia/convert.json)
	rm -rf "$w" && install -d -o "$CONV_USER" -g "$CONV_USER" "$w" "$w/out"
	cp --reflink=auto "$WIN_SRC" "$w/in.qcow2"
	cp "$VIRTIO_ISO" "$w/virtio-win.iso" && chown "$CONV_USER:" "$w/in.qcow2" "$w/virtio-win.iso"
	log "converting $WIN_SRC with virt-v2v in $image"
	# The converter service's own podman flags (internal/convert), with -i disk instead of -i vmx.
	systemd-run --wait --quiet --collect --uid="$CONV_USER" --gid="$CONV_USER" -p SupplementaryGroups=kvm -p RuntimeDirectory=gaia-convert \
		-E HOME="$CONV_HOME" -E XDG_RUNTIME_DIR=/run/gaia-convert -E TMPDIR="$CONV_HOME/tmp" \
		bash -c "podman --cgroup-manager=cgroupfs run --rm --init --network=none --cgroups=disabled --cap-drop=all \
			--security-opt=no-new-privileges --userns=keep-id --group-add=keep-groups --device=/dev/kvm \
			--volume=$w/in.qcow2:/in/disk.qcow2:ro --volume=$w/out:/out --volume=$w/virtio-win.iso:/virtio-win.iso:ro \
			--env=VIRTIO_WIN=/virtio-win.iso $image virt-v2v -i disk /in/disk.qcow2 -o local -os /out -of qcow2 -on $n \
			>$w/virt-v2v.log 2>&1"
	tail -3 "$w/virt-v2v.log"
	mv "$w/out/$n-sda" "$LV/$n-vda.qcow2"
	no_smbus "$LV/$n-vda.qcow2"
	rm -f "$w/in.qcow2" "$w/virtio-win.iso"
	chown libvirt-qemu:libvirt-qemu "$LV/$n-vda.qcow2"
	qemu-img info "$LV/$n-vda.qcow2" | sed -n '1,4p'
	virsh -q define /dev/stdin <<EOF
<domain type='kvm'><name>$n</name><memory unit='MiB'>2048</memory><vcpu>2</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><features><acpi/><apic/><hyperv><relaxed state='on'/><vapic state='on'/><spinlocks state='on' retries='8191'/></hyperv></features>
  <cpu mode='host-passthrough'/><clock offset='localtime'/>
  <devices>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2' discard='unmap'/><source file='$LV/$n-vda.qcow2'/><target dev='vda' bus='virtio'/></disk>
    <interface type='network'><source network='$NET'/><mac address='${MAC[$n]}'/><model type='virtio'/></interface>
    <serial type='pty'/><console type='pty'/><input type='tablet' bus='usb'/><graphics type='vnc' listen='127.0.0.1'/><video><model type='vga'/></video>
  </devices></domain>
EOF
	virsh -q autostart "$n" && virsh -q start "$n"
}

wait_up() { # NAME PORT: the guest answers on its address
	local i
	for ((i = 0; i < 120; i++)); do
		timeout 3 bash -c "</dev/tcp/${IP[$1]}/$2" 2>/dev/null && { log "$1 answers on ${IP[$1]}:$2"; return 0; }
		sleep 5
	done
	echo "UNKNOWN: $1 does not answer on ${IP[$1]}:$2 after 600 s"
	exit 3
}

case "${1:-}" in
create)
	for f in "$CLOUD_IMG" "$WIN_SRC" "$VIRTIO_ISO" /etc/gaia/convert.json; do [[ -s "$f" ]] || { echo "UNKNOWN: $f is missing"; exit 3; }; done
	avail=$(df --output=avail -B1G "$LV" | tail -1 | tr -d ' ')
	((avail >= 60)) || { echo "UNKNOWN: $avail GiB free under $LV, the VMs need 60"; exit 3; }
	mkdir -p "$D/ssh"
	[[ -f "$D/ssh/id_ed25519" ]] || ssh-keygen -q -t ed25519 -N '' -C gaia-t23-guests -f "$D/ssh/id_ed25519"
	network
	exists() { virsh dominfo "$1" >/dev/null 2>&1 && log "$1 exists already: kept"; }
	exists gaia-t23-linux2 || linux_guest gaia-t23-linux2 bios 1
	exists gaia-t23-uefi || linux_guest gaia-t23-uefi efi 0
	exists gaia-t23-win || windows_guest
	wait_up gaia-t23-linux2 22
	wait_up gaia-t23-uefi 22
	wait_up gaia-t23-win 5985
	"$0" status
	;;
status)
	for n in gaia-t23-linux2 gaia-t23-uefi gaia-t23-win; do
		printf '%s: %s, %s, firmware %s, disks %s\n' "$n" "$(virsh domstate "$n" 2>/dev/null || echo missing)" "${IP[$n]}" \
			"$(virsh dumpxml "$n" 2>/dev/null | grep -qE "firmware='efi'|type='pflash'" && echo efi || echo bios)" \
			"$(virsh domblklist "$n" --details 2>/dev/null | awk '$2 == "disk" {print $3}' | paste -sd, -)"
	done
	;;
break-boot)
	n=${2:?NAME}
	[[ "$n" == gaia-t23-linux2 || "$n" == gaia-t23-uefi ]] || { echo "UNKNOWN: break-boot is for the Linux guests"; exit 3; }
	in_guest "$n" <<'EOF'
set -euo pipefail
[ -s /root/mbr-boot-code ] || dd if=/dev/vda of=/root/mbr-boot-code bs=446 count=1 status=none
dd if=/dev/zero of=/dev/vda bs=446 count=1 conv=fsync status=none
# UEFI boots from the EFI system partition, not the MBR: there the loader goes too
if [ -d /sys/firmware/efi ]; then mv /boot/efi/EFI /boot/efi/EFI.gaia-t23-broken; fi
sync
echo "boot code of /dev/vda zeroed (copy in /root/mbr-boot-code)"
EOF
	;;
fix-boot)
	n=${2:?NAME}
	in_guest "$n" <<'EOF'
set -euo pipefail
dd if=/root/mbr-boot-code of=/dev/vda bs=446 count=1 conv=fsync status=none
if [ -d /boot/efi/EFI.gaia-t23-broken ]; then mv /boot/efi/EFI.gaia-t23-broken /boot/efi/EFI; fi
sync
echo "boot code of /dev/vda written back"
EOF
	;;
delete-checkpoint)
	n=${2:?NAME}
	c=$(virsh checkpoint-list "$n" --name --topological | tail -1)
	[[ -n "$c" ]] || { echo "UNKNOWN: $n has no checkpoint"; exit 3; }
	virsh checkpoint-delete "$n" "$c"
	echo "deleted checkpoint $c of $n; left: $(virsh checkpoint-list "$n" --name | paste -sd ' ' -)"
	;;
destroy)
	for n in gaia-t23-linux2 gaia-t23-uefi gaia-t23-win; do
		virsh -q destroy "$n" 2>/dev/null || true
		virsh -q undefine "$n" --checkpoints-metadata --nvram 2>/dev/null || true
		rm -f "$LV/$n"-*
	done
	virsh -q net-destroy "$NET" 2>/dev/null || true
	virsh -q net-undefine "$NET" 2>/dev/null || true
	rm -rf "$CONV_HOME/t23-win"
	fstrim -a 2>/dev/null || true
	;;
*)
	sed -n '2,20p' "$0"
	exit 3
	;;
esac
