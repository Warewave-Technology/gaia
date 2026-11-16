#!/usr/bin/env bash
# FIXTURE, LAB ONLY (CLAUDE.md rule 9, ADR-003 §6): builds the T11 Windows test VM and creates or deletes it
# on the free ESXi host through the host's own shell (SSH: vmkfstools + vim-cmd + python). Never a product
# path: Gaia itself only reads from a migration source (cmd/gaia migrate-check, internal/vsphereread).
#
# gaia-t11-win: Windows Server 2022 Standard Evaluation (Desktop Experience), unattended install, powered off.
#   VMware virtual hardware: SATA (AHCI) disk, e1000e NIC with a static VMware MAC, BIOS, guestOS
#   windows2019srvNext-64, 2 vCPU, 4 GiB, 32 GiB thin disk. Remote Desktop on (its firewall rules enabled); WinRM as
#   Windows Server ships it.
# The install runs once on gaia-lab under KVM on the same device models VMware presents (AHCI controller, Intel
# 82574L = e1000e), so the installed drivers are the ones the guest uses on ESXi; nested ESXi does not run it.
# The result is cached in $IMAGES/gaia-t11-win.qcow2 (REBUILD=1 rebuilds). VMware Tools is not installed: its
# installer only runs inside a VM on VMware.
#
# Usage (on gaia-lab, as root):  lab/esxi/fixture-t11.sh info|build|inspect|create|delete ESXI_IP
#   info    ESXi version, datastore space, python on the host (read-only)
#   build   download the ISO and install Windows (no ESXi access)
#   inspect read the built disk's registry (read-only): setup finished, Remote Desktop and WinRM firewall rules
#   create  build if needed, then upload the disk and register the VM
# Env: PASSWORD_FILE (/root/.gaia-esxi-password; the ESXi root password, never printed), DATASTORE (datastore1),
#      IMAGES (/var/lib/gaia/images), WORK (/var/lib/gaia/t11-fixture), INSTALL_TIMEOUT_S (7200).
# The Administrator password of the guest is random, lives only in the install media while Windows installs,
# and is never printed or kept: nothing ever logs on to the fixture (Windows masks it in its own unattend copy).
# The Remote Desktop firewall rules are opened by SetupComplete.cmd: the unattend FirewallGroups setting left
# them inactive on Server 2022 (first T11 build, read from the image's registry).
set -euo pipefail

ACTION="${1:?info, build, create or delete}"
ESXI_IP="${2:-}"
PASSWORD_FILE="${PASSWORD_FILE:-/root/.gaia-esxi-password}"
DATASTORE="${DATASTORE:-datastore1}"
IMAGES="${IMAGES:-/var/lib/gaia/images}"
WORK="${WORK:-/var/lib/gaia/t11-fixture}"
INSTALL_TIMEOUT_S="${INSTALL_TIMEOUT_S:-7200}"
# Microsoft Evaluation Center, Windows Server 2022, ISO, English (United States); licence accepted by Yiğit (2026-11-16).
WIN_ISO_URL='https://go.microsoft.com/fwlink/p/?LinkID=2195280&clcid=0x409&culture=en-us&country=US'
WIN_ISO="$IMAGES/SERVER_EVAL_x64FRE_en-us.iso"
WIN_INDEX=2 # Windows Server 2022 Standard Evaluation (Desktop Experience); checked against the WIM's XML below
VM=gaia-t11-win
MAC=00:50:56:3f:b1:01
DISK_GIB=32
BUILT="$IMAGES/$VM.qcow2"

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }

ssh_dir=
esxi_setup() {
	[[ -n "$ESXI_IP" ]] || { echo "ESXI_IP is required for $ACTION"; exit 3; }
	[[ -s "$PASSWORD_FILE" ]] || { echo "no ESXi password in $PASSWORD_FILE"; exit 3; }
	ssh_dir=$(mktemp -d)
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
}
# shellcheck disable=SC2329 # invoked by the trap
cleanup() {
	if [[ -n "$ssh_dir" ]]; then
		ssh -o ControlPath="$ssh_dir/ssh.sock" -O exit "root@$ESXI_IP" 2>/dev/null || true
		rm -rf "$ssh_dir"
	fi
	rm -rf "$WORK/unattend" "$WORK/unattend.iso" # the only copies of the guest's install-time password
}
trap cleanup EXIT
esxi() { # esxi ARGS... < script: runs the script with the host's /bin/sh
	# shellcheck disable=SC2029 # the quoted args become the remote command on purpose
	SSH_ASKPASS="$ssh_dir/askpass" SSH_ASKPASS_REQUIRE=force ssh "${ssh_opts[@]}" "root@$ESXI_IP" "sh -s -- $(printf '%q ' "$@")"
}
esxi_cmd() { # esxi_cmd COMMAND < data: runs one command with stdin passed through
	# shellcheck disable=SC2029 # the command is expanded here on purpose
	SSH_ASKPASS="$ssh_dir/askpass" SSH_ASKPASS_REQUIRE=force ssh "${ssh_opts[@]}" "root@$ESXI_IP" "$1"
}

delete_vm() {
	esxi "$VM" "$DATASTORE" <<'EOF'
id=$(vim-cmd vmsvc/getallvms 2>/dev/null | awk -v n="$1" '$2 == n { print $1 }')
if [ -n "$id" ]; then
	vim-cmd vmsvc/power.off "$id" >/dev/null 2>&1 || true
	vim-cmd vmsvc/unregister "$id"
fi
rm -rf "/vmfs/volumes/$2/$1"
echo "deleted: $1 (vmid ${id:-none})"
EOF
}

iso() { # the Windows ISO, downloaded once; its final URL, size and sha256 go to $WIN_ISO.json
	mkdir -p "$IMAGES"
	if [[ ! -s "$WIN_ISO" ]]; then
		echo "downloading the Windows Server 2022 evaluation ISO (about 5 GB)"
		url=$(curl -fsSL -o "$WIN_ISO.part" -w '%{url_effective}' "$WIN_ISO_URL")
		mv "$WIN_ISO.part" "$WIN_ISO"
		jq -n --arg link "$WIN_ISO_URL" --arg url "$url" --argjson bytes "$(stat -c %s "$WIN_ISO")" \
			--arg sha256 "$(sha256sum "$WIN_ISO" | cut -d' ' -f1)" --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
			'{link: $link, url: $url, bytes: $bytes, sha256: $sha256, downloaded: $date}' >"$WIN_ISO.json"
	fi
	jq -c . "$WIN_ISO.json"
	isoinfo -d -i "$WIN_ISO" | grep -E '^Volume id'
	# The image names are in the WIM's XML resource (UTF-16), which the WIM header locates (rhXmlData at 72).
	mnt=$(mktemp -d)
	mount -o loop,ro "$WIN_ISO" "$mnt"
	names=$(python3 -I - "$mnt/sources/install.wim" <<'EOF'
import struct, sys, xml.etree.ElementTree as ET
with open(sys.argv[1], "rb") as f:
    hdr = f.read(96)
    if hdr[:8] != b"MSWIM\0\0\0":
        sys.exit("not a WIM")
    size_flags, off, _ = struct.unpack_from("<QqQ", hdr, 72)
    f.seek(off)
    data = f.read(size_flags & ((1 << 56) - 1))
for img in ET.fromstring(data.decode("utf-16")).iter("IMAGE"):
    print(img.get("INDEX"), img.findtext("DISPLAYNAME") or img.findtext("NAME"))
EOF
)
	umount "$mnt"
	rmdir "$mnt"
	echo "install.wim images:"
	while read -r line; do echo "  $line"; done <<<"$names"
	grep -q "^$WIN_INDEX .*Standard.*Desktop Experience" <<<"$names" ||
		{ echo "image $WIN_INDEX is not Standard (Desktop Experience)"; exit 3; }
}

unattend() { # autounattend.xml on a small second CD; Windows Setup finds it at the root of removable media
	local pw
	pw="Gaia-$(openssl rand -hex 12)-T11!"
	mkdir -p "$WORK/unattend"
	cat >"$WORK/unattend/autounattend.xml" <<EOF
<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">
  <settings pass="windowsPE">
    <component name="Microsoft-Windows-International-Core-WinPE" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <SetupUILanguage><UILanguage>en-US</UILanguage></SetupUILanguage>
      <InputLocale>en-US</InputLocale><SystemLocale>en-US</SystemLocale><UILanguage>en-US</UILanguage><UserLocale>en-US</UserLocale>
    </component>
    <component name="Microsoft-Windows-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <DiskConfiguration>
        <Disk wcm:action="add">
          <DiskID>0</DiskID>
          <WillWipeDisk>true</WillWipeDisk>
          <CreatePartitions>
            <CreatePartition wcm:action="add"><Order>1</Order><Type>Primary</Type><Size>500</Size></CreatePartition>
            <CreatePartition wcm:action="add"><Order>2</Order><Type>Primary</Type><Extend>true</Extend></CreatePartition>
          </CreatePartitions>
          <ModifyPartitions>
            <ModifyPartition wcm:action="add"><Order>1</Order><PartitionID>1</PartitionID><Format>NTFS</Format><Label>System Reserved</Label><Active>true</Active></ModifyPartition>
            <ModifyPartition wcm:action="add"><Order>2</Order><PartitionID>2</PartitionID><Format>NTFS</Format><Label>Windows</Label><Letter>C</Letter></ModifyPartition>
          </ModifyPartitions>
        </Disk>
      </DiskConfiguration>
      <ImageInstall>
        <OSImage>
          <InstallFrom><MetaData wcm:action="add"><Key>/IMAGE/INDEX</Key><Value>$WIN_INDEX</Value></MetaData></InstallFrom>
          <InstallTo><DiskID>0</DiskID><PartitionID>2</PartitionID></InstallTo>
        </OSImage>
      </ImageInstall>
      <UserData>
        <AcceptEula>true</AcceptEula>
        <FullName>Gaia lab</FullName>
        <Organization>Gaia lab</Organization>
      </UserData>
    </component>
  </settings>
  <settings pass="specialize">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <ComputerName>GAIA-T11-WIN</ComputerName>
      <TimeZone>UTC</TimeZone>
    </component>
    <component name="Microsoft-Windows-TerminalServices-LocalSessionManager" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <fDenyTSConnections>false</fDenyTSConnections>
    </component>
    <component name="Microsoft-Windows-Deployment" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <RunSynchronous>
        <RunSynchronousCommand wcm:action="add"><Order>1</Order><Path>cmd /c mkdir %WINDIR%\Setup\Scripts</Path></RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add"><Order>2</Order><Path>cmd /c echo netsh advfirewall firewall set rule group="Remote Desktop" new enable=Yes&gt;%WINDIR%\Setup\Scripts\SetupComplete.cmd</Path></RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add"><Order>3</Order><Path>cmd /c echo shutdown /s /t 10 /f&gt;&gt;%WINDIR%\Setup\Scripts\SetupComplete.cmd</Path></RunSynchronousCommand>
      </RunSynchronous>
    </component>
  </settings>
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-International-Core" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <InputLocale>en-US</InputLocale><SystemLocale>en-US</SystemLocale><UILanguage>en-US</UILanguage><UserLocale>en-US</UserLocale>
    </component>
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <OOBE><HideEULAPage>true</HideEULAPage><ProtectYourPC>3</ProtectYourPC></OOBE>
      <UserAccounts><AdministratorPassword><Value>$pw</Value><PlainText>true</PlainText></AdministratorPassword></UserAccounts>
      <TimeZone>UTC</TimeZone>
    </component>
  </settings>
</unattend>
EOF
	chmod 600 "$WORK/unattend/autounattend.xml"
	genisoimage -quiet -J -r -V UNATTEND -o "$WORK/unattend.iso" "$WORK/unattend"
	rm -rf "$WORK/unattend"
}

inspect() { # the built disk's own record that setup finished and which firewall rules are active (JSON)
	local reg
	reg=$(mktemp -d)
	LIBGUESTFS_BACKEND=direct virt-win-reg "$BUILT" 'HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Setup\State' >"$reg/state.reg"
	LIBGUESTFS_BACKEND=direct virt-win-reg "$BUILT" 'HKLM\SYSTEM\ControlSet001\Services\SharedAccess\Parameters\FirewallPolicy\FirewallRules' >"$reg/fw.reg"
	python3 -I - "$reg/state.reg" "$reg/fw.reg" <<'EOF'
import json, re, sys

def values(path):  # .reg hex(1) values (REG_SZ, UTF-16) by name
    t = open(path, "rb").read().decode("latin-1")
    for m in re.finditer(r'"([^"]+)"=hex\(1\):([0-9a-f,\\\s]+)', t):
        b = bytes(int(x, 16) for x in re.findall(r"[0-9a-f]{2}", m.group(2)))
        yield m.group(1), b.decode("utf-16le").rstrip("\0")

state = dict(values(sys.argv[1])).get("ImageState")
rules = {}
for name, v in values(sys.argv[2]):
    if name.startswith(("RemoteDesktop-UserMode-In-TCP", "WINRM-HTTP-In-TCP")):
        f = v.split("|")
        rules[name] = {"active": "Active=TRUE" in f, "profiles": [x[8:] for x in f if x.startswith("Profile=")] or ["all"],
                       "remote": [x[4:] for x in f if x.startswith("RA4=")] or ["any"]}
print(json.dumps({"image_state": state, "firewall_rules": rules}))
EOF
	rm -rf "$reg"
}

monitor() { # monitor COMMAND: one line to the build VM's QEMU monitor
	printf '%s\n' "$1" | socat -t2 - "UNIX-CONNECT:$WORK/monitor.sock" >/dev/null 2>&1 || true
}

build() {
	if [[ -s "$BUILT" && "${REBUILD:-}" != 1 ]]; then
		echo "fixture disk cached: $BUILT ($(du -h "$BUILT" | cut -f1)); REBUILD=1 rebuilds"
		jq -c . "$BUILT.json"
		return
	fi
	for bin in qemu-system-x86_64 qemu-img genisoimage isoinfo socat openssl python3 jq curl; do
		command -v "$bin" >/dev/null || { echo "MISSING: $bin (run setup.sh)"; exit 3; }
	done
	[[ -e /dev/kvm ]] || { echo "UNKNOWN: /dev/kvm is missing"; exit 3; }
	free_kib=$(df -Pk "$IMAGES" | awk 'NR == 2 { print $4 }')
	((free_kib >= 50 * 1048576)) || { echo "UNKNOWN: $((free_kib / 1048576)) GiB free in $IMAGES, needs 50"; exit 3; }
	iso
	rm -rf "$WORK"
	mkdir -p "$WORK"
	unattend
	qemu-img create -q -f qcow2 "$WORK/build.qcow2" "${DISK_GIB}G"
	echo "installing Windows (unattended, up to $INSTALL_TIMEOUT_S s); progress screenshots in $WORK"
	start=$(date +%s)
	# No uplink: restrict=on gives the NIC a link and DHCP but no route out, so the install sees no Windows Update.
	qemu-system-x86_64 -name "$VM-build" -enable-kvm -machine pc -cpu host -smp 2 -m 4096 -rtc base=utc \
		-device ahci,id=ahci \
		-drive "if=none,id=disk0,file=$WORK/build.qcow2,format=qcow2,cache=unsafe,discard=unmap" -device ide-hd,drive=disk0,bus=ahci.0 \
		-drive "file=$WIN_ISO,media=cdrom,readonly=on,index=2" -drive "file=$WORK/unattend.iso,media=cdrom,readonly=on,index=3" \
		-boot order=cd -netdev user,id=net0,restrict=on -device "e1000e,netdev=net0,mac=$MAC" \
		-vga std -display none -monitor "unix:$WORK/monitor.sock,server=on,wait=off" -serial none \
		-pidfile "$WORK/qemu.pid" >"$WORK/qemu.log" 2>&1 &
	qpid=$!
	n=0
	while kill -0 "$qpid" 2>/dev/null; do
		if (($(date +%s) - start > INSTALL_TIMEOUT_S)); then
			monitor "screendump $WORK/timeout.ppm"
			kill "$qpid"
			echo "UNKNOWN: the install did not finish within $INSTALL_TIMEOUT_S s (last screen: $WORK/timeout.ppm)"
			exit 3
		fi
		sleep 30
		n=$((n + 1))
		((n % 10)) || monitor "screendump $WORK/progress.ppm"
	done
	rc=0
	wait "$qpid" || rc=$?
	cat "$WORK/qemu.log"
	((rc == 0)) || { echo "UNKNOWN: qemu exited $rc"; exit 3; }
	secs=$(($(date +%s) - start))
	# SetupComplete.cmd shuts Windows down at the end of setup: QEMU exiting on its own is the end of the install.
	((secs > 300)) || { echo "UNKNOWN: qemu exited after $secs s, too early for an install"; exit 3; }
	mv "$WORK/build.qcow2" "$BUILT"
	jq -n --slurpfile iso "$WIN_ISO.json" --argjson secs "$secs" --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		--argjson index "$WIN_INDEX" --arg mac "$MAC" --argjson gib "$DISK_GIB" --arg qemu "$(qemu-system-x86_64 --version | head -1)" \
		'{iso: $iso[0], image_index: $index, install_seconds: $secs, built: $date, disk_gib: $gib, mac: $mac, qemu: $qemu,
		  hardware: "pc (i440FX) + AHCI disk + e1000e (82574L), as VMware sata0 + e1000e"}' >"$BUILT.json"
	echo "installed in $secs s: $BUILT ($(du -h "$BUILT" | cut -f1))"
	jq -c . "$BUILT.json"
	inspect | tee "$BUILT.inspect.json"
	jq -e '.image_state == "IMAGE_STATE_COMPLETE" and .firewall_rules["RemoteDesktop-UserMode-In-TCP"].active' "$BUILT.inspect.json" >/dev/null ||
		{ echo "UNKNOWN: setup did not finish, or Remote Desktop is closed in the firewall"; exit 3; }
}

case "$ACTION" in
info)
	esxi_setup
	esxi "$DATASTORE" <<'EOF'
vmware -vl
df -h | awk -v ds="$1" 'NR == 1 || $NF ~ ds'
ls -l /bin/python* 2>&1 | head -3
vim-cmd vmsvc/getallvms | awk 'NR > 1 { print "vm:", $2 }'
EOF
	exit 0
	;;
build)
	build
	exit 0
	;;
inspect)
	inspect
	exit 0
	;;
delete)
	esxi_setup
	delete_vm
	exit 0
	;;
create) ;;
*)
	echo "usage: $0 info|build|create|delete ESXI_IP"
	exit 3
	;;
esac

esxi_setup
build
mkdir -p "$WORK"
raw="$WORK/$VM.raw"
qemu-img convert -q -f qcow2 -O raw "$BUILT" "$raw"
sha=$(sha256sum "$raw" | cut -d' ' -f1)
echo "$sha" >"$WORK/$VM.raw.sha256"
echo "disk: $(stat -c %s "$raw") bytes, $(du -h "$raw" | cut -f1) allocated, sha256 $sha"
esxi "$DATASTORE" <<'EOF'
[ -d "/vmfs/volumes/$1" ] || { echo "no datastore $1"; ls /vmfs/volumes; exit 1; }
[ -x /bin/python ] || { echo "no python on the host"; exit 1; } # BusyBox sh: no `command` builtin
EOF
delete_vm >/dev/null
dir="/vmfs/volumes/$DATASTORE/$VM"
esxi "$dir" "$VM" "$DISK_GIB" <<'EOF'
set -e
mkdir -p "$1"
vmkfstools -c "$3G" -d thin -a ide "$1/$2.vmdk" >/dev/null
EOF
# ESXi 8 cannot import hosted VMDKs, and a raw upload would write every zero into VMFS: only the image's
# allocated, non-zero extents travel, as (offset, length, bytes) records, into the thin disk's flat file.
esxi_cmd 'cat > /tmp/gaia-t11-extents.py' <<'EOF'
import struct, sys
src, n = sys.stdin.buffer, 0
with open(sys.argv[1], "r+b") as f:
    while True:
        hdr = src.read(16)
        if len(hdr) != 16:
            sys.exit("short header")
        off, ln = struct.unpack(">QQ", hdr)
        if ln == 0:
            break
        f.seek(off)
        while ln:
            b = src.read(min(ln, 1 << 20))
            if not b:
                sys.exit("short data")
            f.write(b)
            ln -= len(b)
            n += len(b)
print("wrote %d bytes" % n)
EOF
python3 -I - "$raw" <<'EOF' | esxi_cmd "python /tmp/gaia-t11-extents.py '$dir/$VM-flat.vmdk'; rm -f /tmp/gaia-t11-extents.py"
import os, struct, sys
fd, out, pos = os.open(sys.argv[1], os.O_RDONLY), sys.stdout.buffer, 0
chunk = 4 << 20
while True:
    try:
        data = os.lseek(fd, pos, os.SEEK_DATA)
    except OSError:
        break
    hole = os.lseek(fd, data, os.SEEK_HOLE)
    while data < hole:
        b = os.pread(fd, min(chunk, hole - data), data)
        if b.count(0) != len(b):
            out.write(struct.pack(">QQ", data, len(b)))
            out.write(b)
        data += len(b)
    pos = hole
out.write(struct.pack(">QQ", 0, 0))
EOF
rm -f "$raw"
esxi "$dir" "$VM" "$MAC" <<'EOF'
set -e
cd "$1"
cat >"$2.vmx" <<VMX
.encoding = "UTF-8"
config.version = "8"
virtualHW.version = "19"
displayName = "$2"
guestOS = "windows2019srvNext-64"
firmware = "bios"
memSize = "4096"
numvcpus = "2"
sata0.present = "TRUE"
sata0:0.present = "TRUE"
sata0:0.fileName = "$2.vmdk"
ethernet0.present = "TRUE"
ethernet0.virtualDev = "e1000e"
ethernet0.networkName = "VM Network"
ethernet0.addressType = "static"
ethernet0.address = "$3"
VMX
echo "$2: vmid $(vim-cmd solo/registervm "$1/$2.vmx"), $(vim-cmd vmsvc/getallvms | awk -v n="$2" '$2 == n { print $1 }' | xargs vim-cmd vmsvc/power.getstate | tail -1)"
ls -l "$1"
du -h "$1"
EOF
