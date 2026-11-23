#!/usr/bin/env bash
# fixture: T28's measurement of virtio-win's smbus driver on T11's Windows Server 2022 (ADR-005 C step 1), on gaia-lab.
# Not part of the product: it does by hand, in the pinned converter image, what a migration check does, to see what the
# converted guest does on its first boots and to read what virt-v2v's first-boot service wrote.
#
#   newest            the newest virtio-win release published (the release index, read-only), as JSON
#   run VARIANT OUT   VARIANT is "as-is" (virtio-win as virt-v2v takes it) or "no-smbus" (the same ISO, its smbus
#                     driver left out of the tree virt-v2v reads, as exclude_drivers does): convert T11's disk with
#                     virt-v2v in the converter image, boot the result with no network for BOOT_S seconds, count the
#                     guest's own restarts (libvirt reboot events), stop it, and copy the first-boot service's log
#                     and script folders out of the disk into OUT
#
# The guest is a transient libvirt domain with no NIC; it and its disk are removed at the end. Nothing logs on to it.
# Env: BOOT_S (480), MEMORY_MIB (2048).
#
# Usage (on gaia-lab, as root): lab/lab-vm/t28-firstboot.sh newest | run as-is|no-smbus OUT
set -euo pipefail

WIN_SRC=/var/lib/gaia/images/gaia-t11-win.qcow2
VIRTIO_ISO=/var/lib/gaia/images/virtio-win-0.1.302.iso
VIRTIO_SHA256=303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d
INDEX=https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/
CONV_USER=gaia-convert
CONV_HOME=/var/lib/gaia-convert
LV=/var/lib/libvirt/images
BOOT_S=${BOOT_S:-480}
MEMORY_MIB=${MEMORY_MIB:-2048}
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

case "${1:-}" in
newest)
	# The archive lists every release as virtio-win-0.1.N-R/; the newest is the highest N.
	page=$(curl -fsSL --max-time 30 "$INDEX")
	newest=$(grep -o 'virtio-win-0\.1\.[0-9]*-[0-9]*/' <<<"$page" | tr -d / | sort -uV | tail -1)
	when=$(grep -o "$newest/</a> *[0-9-]* [0-9:]*" <<<"$page" | sed 's/.*a> *//' | head -1)
	latest=$(curl -fsSI --max-time 30 "${INDEX%archive-virtio/}latest-virtio/virtio-win.iso" | tr -d '\r' | sed -n 's/^[Ll]ocation: //p')
	jq -n --arg index "$INDEX" --arg newest "$newest" --arg listed "$when" --arg latest "$latest" --arg pinned 0.1.302-1 \
		--arg at "$(date -u +%FT%TZ)" '{read_at: $at, index: $index, newest_release: $newest, listed: $listed,
		latest_link_points_to: $latest, pinned: $pinned, newer_than_pinned: ($newest != $pinned)}'
	exit 0 ;;
run) ;;
*) echo "usage: $0 newest | run as-is|no-smbus OUT"; exit 3 ;;
esac

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
variant=${2:?as-is or no-smbus}
out=${3:?OUT}
case "$variant" in as-is) exclude=() ;; no-smbus) exclude=(smbus) ;; *) echo "UNKNOWN: variant $variant"; exit 3 ;; esac
name=gaia-t28-fb-$variant
w=$CONV_HOME/t28-$variant
disk=$LV/$name.qcow2
mkdir -p "$out"
free_gb=$(df --output=avail -BG "$CONV_HOME" | tail -1 | tr -dc 0-9)
((free_gb >= 30)) || { echo "UNKNOWN: ${free_gb} GB free under $CONV_HOME, the conversion wants 30"; exit 3; }
echo "$VIRTIO_SHA256  $VIRTIO_ISO" | sha256sum -c --quiet
image=$(jq -r .image /etc/gaia/convert.json)
cleanup() {
	virsh -q destroy "$name" >/dev/null 2>&1 || true
	rm -rf "$w" "$disk"
	fstrim -a 2>/dev/null || true
}
trap cleanup EXIT
rm -rf "$w" && install -d -o "$CONV_USER" -g "$CONV_USER" "$w" "$w/out"
cp --reflink=auto "$WIN_SRC" "$w/in.qcow2"
cp "$VIRTIO_ISO" "$w/virtio-win.iso" && chown "$CONV_USER:" "$w/in.qcow2" "$w/virtio-win.iso"

# virt-v2v takes VIRTIO_WIN as an ISO or a directory; with a driver left out it reads a copy of the ISO's tree without
# that driver's folders and files (what the product's exclude_drivers does in the same image).
{
	echo 'set -eu'
	echo 'vw=/virtio-win.iso'
	if ((${#exclude[@]})); then
		ex=""
		for d in "${exclude[@]}"; do ex="$ex --exclude=$d --exclude=$d.*"; done
		echo 'mkdir /tmp/vw'
		echo "guestfish --ro --format=raw -a /virtio-win.iso -m /dev/sda tar-out / - | tar -x -C /tmp/vw --ignore-case$ex"
		for d in "${exclude[@]}"; do echo "echo \"left-out driver $d: \$(find /tmp/vw -iname '$d*' | wc -l) files still in the tree\""; done
		echo 'vw=/tmp/vw'
	fi
	echo "VIRTIO_WIN=\$vw virt-v2v -i disk /in/disk.qcow2 -o local -os /out -of qcow2 -on $name"
} >"$w/convert.sh"
log "$variant: converting $WIN_SRC with virt-v2v in $image (exclude: ${exclude[*]:-none})"
t0=$(date +%s)
systemd-run --wait --quiet --collect --uid="$CONV_USER" --gid="$CONV_USER" -p SupplementaryGroups=kvm -p RuntimeDirectory=gaia-convert \
	-E HOME="$CONV_HOME" -E XDG_RUNTIME_DIR=/run/gaia-convert -E TMPDIR="$CONV_HOME/tmp" \
	bash -c "podman --cgroup-manager=cgroupfs run --rm --init --network=none --cgroups=disabled --cap-drop=all \
		--security-opt=no-new-privileges --userns=keep-id --group-add=keep-groups --device=/dev/kvm \
		--volume=$w/in.qcow2:/in/disk.qcow2:ro --volume=$w/out:/out --volume=$w/virtio-win.iso:/virtio-win.iso:ro \
		--volume=$w/convert.sh:/convert.sh:ro $image sh /convert.sh >$w/virt-v2v.log 2>&1"
conv_s=$(($(date +%s) - t0))
cp "$w/virt-v2v.log" "$w/convert.sh" "$out/"
grep -i 'smbus\|left-out' "$w/virt-v2v.log" | head -20 >"$out/virt-v2v-smbus.txt" || true
mv "$w/out/$name-sda" "$disk"
chown libvirt-qemu:libvirt-qemu "$disk"

log "$variant: converted in ${conv_s} s; booting with no network for $BOOT_S s"
cat >"$w/domain.xml" <<EOF
<domain type='kvm'><name>$name</name><memory unit='MiB'>$MEMORY_MIB</memory><vcpu>2</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><features><acpi/><apic/><hyperv><relaxed state='on'/><vapic state='on'/><spinlocks state='on' retries='8191'/></hyperv></features>
  <cpu mode='host-passthrough'/><clock offset='localtime'/><on_reboot>restart</on_reboot>
  <devices>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='$disk'/><target dev='vda' bus='virtio'/></disk>
    <serial type='pty'/><console type='pty'/><input type='tablet' bus='usb'/><graphics type='vnc' listen='127.0.0.1'/><video><model type='vga'/></video>
  </devices></domain>
EOF
virsh -q create "$w/domain.xml"
# libvirt reports every restart the guest asks for as a reboot event (on_reboot=restart keeps the same QEMU)
timeout $((BOOT_S + 5)) virsh event --domain "$name" --event reboot --loop --timeout "$BOOT_S" --timestamp >"$out/reboot-events.txt" 2>&1 || true
reboots=$(grep -c "event 'reboot'" "$out/reboot-events.txt" || true)
virsh -q destroy "$name"
log "$variant: $reboots restart(s) in $BOOT_S s; reading the first-boot service's files"

# What virt-v2v's first-boot service (rhsrvany, C:\Program Files\Guestfs\Firstboot) left: its log and which scripts
# are done or still pending. ntfsfix: the guest was stopped, not shut down, and ntfs-3g mounts it read-only otherwise.
qemu-img create -q -f qcow2 -F qcow2 -b "$disk" "$w/read.qcow2"
root=$(LIBGUESTFS_BACKEND=direct guestfish --ro -a "$w/read.qcow2" -i inspect-get-roots | head -1)
fb='/Program Files/Guestfs/Firstboot'
printf '%s\n' run "ntfsfix $root" "mount-ro $root /" "-download \"$fb/log.txt\" $out/firstboot-log.txt" \
	"-find \"$fb\"" "-ls /Windows/Drivers/VirtIO" >"$w/read.gf"
LIBGUESTFS_BACKEND=direct guestfish --rw -a "$w/read.qcow2" -f "$w/read.gf" >"$out/firstboot-files.txt" 2>&1 || true
[[ -s $out/firstboot-log.txt ]] || echo "WARNING: no first-boot log on the disk" >&2
jq -n --arg v "$variant" --argjson conv "$conv_s" --argjson boot "$BOOT_S" --argjson reboots "$reboots" \
	--arg image "$image" --arg vw "$VIRTIO_SHA256" --argjson exclude "$(printf '%s\n' "${exclude[@]}" | jq -R . | jq -sc 'map(select(. != ""))')" \
	--argjson loglines "$(wc -l <"$out/firstboot-log.txt" 2>/dev/null || echo 0)" \
	'{variant: $v, converter_image: $image, virtio_win_sha256: $vw, excluded: $exclude, conversion_s: $conv, boot_window_s: $boot,
	  guest_restarts: $reboots, firstboot_log_lines: $loglines}' | tee "$out/result.json"
