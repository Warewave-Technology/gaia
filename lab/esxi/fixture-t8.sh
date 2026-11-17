#!/usr/bin/env bash
# FIXTURE, LAB ONLY (CLAUDE.md rule 9, ADR-003 §6): creates and deletes the T8 test VMs on the
# free ESXi host through the host's own shell (SSH: vmkfstools + vim-cmd). This is never a product
# path: Gaia itself only reads from a migration source (cmd/gaia migrate-check, internal/vsphereread).
#
# Three powered-off Debian 12 VMs (official generic image: standard kernel, VMware drivers; its disk has both a BIOS
# boot path and an EFI system partition), never started:
#   gaia-t8-linux   SSH on 22, HTTP /health on 8080 (systemd), DHCP on any en*/eth* NIC (systemd-networkd), BIOS
#   gaia-t8-broken  the same, with the MBR's boot code (bytes 0-439) zeroed: it cannot boot
#   gaia-t21-uefi   the same as gaia-t8-broken, but firmware "efi" (T21): UEFI reads no MBR boot code, so it boots
#                   only through UEFI, and a check that boots it proves the UEFI path, not BIOS
# All: pvscsi disk (thin), vmxnet3 NIC with a static MAC, guestOS debian12-64.
#
# Usage (on gaia-lab, as root):  lab/esxi/fixture-t8.sh create|delete ESXI_IP
# Env: PASSWORD_FILE (/root/.gaia-esxi-password; the ESXi root password, never printed), DATASTORE (datastore1),
#      IMAGES (/var/lib/gaia/images, the downloaded Debian image is cached there), WORK (/var/lib/gaia/t8-fixture).
set -euo pipefail

ACTION="${1:?create or delete}"
ESXI_IP="${2:?ESXi host IP}"
PASSWORD_FILE="${PASSWORD_FILE:-/root/.gaia-esxi-password}"
DATASTORE="${DATASTORE:-datastore1}"
IMAGES="${IMAGES:-/var/lib/gaia/images}"
WORK="${WORK:-/var/lib/gaia/t8-fixture}"
CLOUD_URL=https://cloud.debian.org/images/cloud/bookworm/latest
IMG=debian-12-generic-amd64.qcow2
VMS=(gaia-t8-linux gaia-t8-broken gaia-t21-uefi)
declare -A MAC=([gaia-t8-linux]=00:50:56:3f:a8:01 [gaia-t8-broken]=00:50:56:3f:a8:02 [gaia-t21-uefi]=00:50:56:3f:a8:03)
declare -A FIRMWARE=([gaia-t8-linux]=bios [gaia-t8-broken]=bios [gaia-t21-uefi]=efi)

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
[[ -s "$PASSWORD_FILE" ]] || { echo "no ESXi password in $PASSWORD_FILE"; exit 3; }
ssh_dir=$(mktemp -d)
trap 'ssh -o ControlPath="$ssh_dir/ssh.sock" -O exit "root@$ESXI_IP" 2>/dev/null || true; rm -rf "$ssh_dir"' EXIT
# The password reaches ssh through SSH_ASKPASS, never a command line; one shared connection and one
# attempt only, because ESXi locks root after 5 failed logins.
cat >"$ssh_dir/askpass" <<EOF
#!/bin/sh
cat "$PASSWORD_FILE"
EOF
chmod 700 "$ssh_dir/askpass"
ssh_opts=(-o ControlMaster=auto -o "ControlPath=$ssh_dir/ssh.sock" -o ControlPersist=600
	-o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_dir/known_hosts"
	-o NumberOfPasswordPrompts=1 -o PubkeyAuthentication=no -o ConnectTimeout=10)
esxi() { # esxi ARGS... < script: runs the script with the host's /bin/sh
	# shellcheck disable=SC2029 # the quoted args become the remote command on purpose
	SSH_ASKPASS="$ssh_dir/askpass" SSH_ASKPASS_REQUIRE=force ssh "${ssh_opts[@]}" "root@$ESXI_IP" "sh -s -- $(printf '%q ' "$@")"
}
esxi_put() { # esxi_put LOCAL REMOTE
	# shellcheck disable=SC2029 # the remote path is expanded here on purpose
	SSH_ASKPASS="$ssh_dir/askpass" SSH_ASKPASS_REQUIRE=force ssh "${ssh_opts[@]}" "root@$ESXI_IP" "cat > '$2'" <"$1"
}

delete_vm() {
	esxi "$1" "$DATASTORE" <<'EOF'
id=$(vim-cmd vmsvc/getallvms 2>/dev/null | awk -v n="$1" '$2 == n { print $1 }')
if [ -n "$id" ]; then
	vim-cmd vmsvc/power.off "$id" >/dev/null 2>&1 || true
	vim-cmd vmsvc/unregister "$id"
fi
rm -rf "/vmfs/volumes/$2/$1"
echo "deleted: $1 (vmid ${id:-none})"
EOF
}

if [[ "$ACTION" == delete ]]; then
	for vm in "${VMS[@]}"; do delete_vm "$vm"; done
	exit 0
fi
[[ "$ACTION" == create ]] || { echo "usage: $0 create|delete ESXI_IP"; exit 3; }
for bin in qemu-img virt-customize curl sha512sum; do command -v "$bin" >/dev/null || { echo "MISSING: $bin (run setup.sh)"; exit 3; }; done

# The guest image, checked against Debian's checksums.
mkdir -p "$IMAGES" "$WORK"
if [[ ! -s "$IMAGES/$IMG" ]]; then
	curl -fsSL "$CLOUD_URL/$IMG" -o "$IMAGES/$IMG.part" && mv "$IMAGES/$IMG.part" "$IMAGES/$IMG"
fi
curl -fsSL "$CLOUD_URL/SHA512SUMS" -o "$WORK/SHA512SUMS"
grep " $IMG\$" "$WORK/SHA512SUMS"
(cd "$IMAGES" && grep " $IMG\$" "$WORK/SHA512SUMS" | sha512sum -c -)

esxi "$DATASTORE" <<'EOF'
[ -d "/vmfs/volumes/$1" ] || { echo "no datastore $1"; ls /vmfs/volumes; exit 1; }
EOF

cat >"$WORK/50-gaia.network" <<'EOF'
[Match]
Name=en* eth*
[Network]
DHCP=ipv4
EOF
cat >"$WORK/gaia-health.service" <<'EOF'
[Unit]
Description=Gaia T8 HTTP health endpoint
After=network.target
[Service]
ExecStart=/usr/bin/python3 -m http.server 8080 --directory /srv/gaia-health
Restart=always
[Install]
WantedBy=multi-user.target
EOF

for vm in "${VMS[@]}"; do
	delete_vm "$vm" >/dev/null
	qcow="$WORK/$vm.qcow2" raw="$WORK/$vm.raw"
	cp "$IMAGES/$IMG" "$qcow"
	# Offline changes only (the guest never runs here): hostname, SSH host keys, the health endpoint,
	# DHCP on any NIC by name (the NIC is vmxnet3 on ESXi, virtio on KVM), cloud-init off (no datasource).
	LIBGUESTFS_BACKEND=direct virt-customize -q -a "$qcow" --hostname "$vm" \
		--run-command 'ssh-keygen -A' \
		--mkdir /srv/gaia-health --write /srv/gaia-health/health:ok \
		--upload "$WORK/gaia-health.service:/etc/systemd/system/gaia-health.service" \
		--upload "$WORK/50-gaia.network:/etc/systemd/network/50-gaia.network" \
		--run-command 'systemctl enable systemd-networkd.service gaia-health.service' \
		--touch /etc/cloud/cloud-init.disabled
	qemu-img convert -q -f qcow2 -O raw "$qcow" "$raw"
	if [[ "$vm" == gaia-t8-broken || "$vm" == gaia-t21-uefi ]]; then
		dd if=/dev/zero of="$raw" bs=440 count=1 conv=notrunc status=none
		echo "$vm: zeroed the MBR boot code (bytes 0-439)"
	fi
	rm -f "$qcow"
	# ESXi 8 has no multiextent module, so it cannot import hosted (sparse) VMDKs: the raw image goes up as
	# a flat extent with a VMFS descriptor, and vmkfstools clones that into a thin disk (VMFS to VMFS).
	dir="/vmfs/volumes/$DATASTORE/$vm"
	esxi "$dir" <<'EOF'
mkdir -p "$1"
EOF
	esxi_put "$raw" "$dir/$vm-src-flat.vmdk"
	sectors=$(($(stat -c %s "$raw") / 512))
	rm -f "$raw"
	esxi "$dir" "$vm" "${MAC[$vm]}" "$sectors" "${FIRMWARE[$vm]}" <<'EOF'
set -e
cd "$1"
cat >"$2-src.vmdk" <<VMDK
# Disk DescriptorFile
version=1
CID=fffffffe
parentCID=ffffffff
createType="vmfs"

RW $4 VMFS "$2-src-flat.vmdk"

ddb.adapterType = "lsilogic"
ddb.geometry.cylinders = "$(($4 / 16065))"
ddb.geometry.heads = "255"
ddb.geometry.sectors = "63"
ddb.virtualHWVersion = "19"
VMDK
vmkfstools -i "$2-src.vmdk" -d thin "$2.vmdk" >/dev/null
vmkfstools -U "$2-src.vmdk"
cat >"$2.vmx" <<VMX
.encoding = "UTF-8"
config.version = "8"
virtualHW.version = "19"
displayName = "$2"
guestOS = "debian12-64"
firmware = "$5"
memSize = "1024"
numvcpus = "2"
scsi0.present = "TRUE"
scsi0.virtualDev = "pvscsi"
scsi0:0.present = "TRUE"
scsi0:0.deviceType = "scsi-hardDisk"
scsi0:0.fileName = "$2.vmdk"
ethernet0.present = "TRUE"
ethernet0.virtualDev = "vmxnet3"
ethernet0.networkName = "VM Network"
ethernet0.addressType = "static"
ethernet0.address = "$3"
VMX
echo "$2: vmid $(vim-cmd solo/registervm "$1/$2.vmx"), $(vim-cmd vmsvc/getallvms | awk -v n="$2" '$2 == n { print $1 }' | xargs vim-cmd vmsvc/power.getstate | tail -1)"
ls -l "$1"
EOF
done
