#!/usr/bin/env bash
# Runs T11 on gaia-lab: the migration check, stage 1 (cold), for a Windows guest on the free ESXi lab host.
#   1. fixture (lab only): a powered-off Windows Server 2022 evaluation VM with VMware virtual hardware
#      (lab/esxi/fixture-t11.sh: unattended install on gaia-lab, uploaded and registered through the host shell);
#   2. gaia migrate-check, read-only vSphere calls only, unchanged from T8 except -virtio-win: inventory, datastore
#      HTTP reads of the VM folder, virt-v2v -i vmx (VIRTIO_WIN = the Fedora virtio-win ISO) to qcow2/virtio,
#      gaia verify with the windows-basic template (boot = the RDP handshake, then WinRM), one signed bundle;
#   3. the read-only proof, as in T8: allowlist tests, Gaia's call log, hostd.log tasks, folder listings;
#   4. what the boot check proves, on the same Windows disk outside the migration flow: booted on the hardware it
#      was installed on (AHCI + e1000e) it must pass; on virtio storage without virtio drivers (Windows stops with
#      INACCESSIBLE_BOOT_DEVICE) it must not;
#   5. the ledger and every bundle verified; the fixture deleted.
# Gates (CLAUDE.md T11): (a) Windows fixture -> readiness OK with signed evidence; (b) zero write calls against ESXi;
#   (c) the check that proved Windows was up, and that it cannot pass on a broken boot (the report argues; step 4
#   shows it on this disk).
#
# Usage (on gaia-lab, as root, after setup.sh):  lab/lab-vm/t11-migration.sh ESXI_IP
#   systemd-run --unit=gaia-t11 --collect -E HOME=/root lab/lab-vm/t11-migration.sh 192.168.1.29
# The ESXi root password must be in /root/.gaia-esxi-password (mode 0600, written by Yiğit; never printed).
# Env: PASSWORD_FILE, DATASTORE (datastore1), OUT_DIR (/root/t11-<UTC timestamp>), KEEP=1 keeps the fixture and work dirs.
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

ESXI_IP="${1:?ESXi host IP}"
PASSWORD_FILE="${PASSWORD_FILE:-/root/.gaia-esxi-password}"
DATASTORE="${DATASTORE:-datastore1}"
OUT_DIR="${OUT_DIR:-/root/t11-$(date -u +%Y%m%dT%H%M%SZ)}"
VM=gaia-t11-win
MAC=00:50:56:3f:b1:01
IMAGES=/var/lib/gaia/images
WORK=/var/lib/gaia/migrate
IMAGE_DIR=/var/lib/libvirt/images/gaia-migrate
KEY=/var/lib/gaia/t11-evidence-key.pem
# Fedora's virtio-win, pinned (the stable link moves); approved by Yiğit (2026-11-16).
VIRTIO_VERSION=0.1.302
VIRTIO_URL="https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-$VIRTIO_VERSION-1/virtio-win-$VIRTIO_VERSION.iso"
VIRTIO_ISO="$IMAGES/virtio-win-$VIRTIO_VERSION.iso"
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
FIXTURE="$repo_root/lab/esxi/fixture-t11.sh"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in go jq make qemu-img virsh virt-v2v dnsmasq ip ssh curl socat fstrim; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done
if [[ ! -s "$PASSWORD_FILE" ]] || [[ "$(stat -c %a "$PASSWORD_FILE")" != 600 ]]; then
	echo "UNKNOWN: the ESXi root password must be in $PASSWORD_FILE with mode 0600 (see the T8 report)"
	exit 3
fi
[[ -e /dev/kvm ]] || { echo "UNKNOWN: /dev/kvm is missing (see T6)"; exit 3; }
mkdir -p "$OUT_DIR/evidence"
exec > >(tee "$OUT_DIR/t11.log") 2>&1
ln -sfn "$OUT_DIR" /root/t11-latest

section() { printf '\n== %s\n' "$*"; }

# Observations on the host itself, lab only, through the same fixture SSH (read-only commands).
ssh_dir=$(mktemp -d)
cat >"$ssh_dir/askpass" <<EOF
#!/bin/sh
cat "$PASSWORD_FILE"
EOF
chmod 700 "$ssh_dir/askpass"
ssh_opts=(-o ControlMaster=auto -o "ControlPath=$ssh_dir/ssh.sock" -o ControlPersist=600
	-o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_dir/known_hosts"
	-o NumberOfPasswordPrompts=1 -o PubkeyAuthentication=no -o ConnectTimeout=10)
esxi() {
	# shellcheck disable=SC2029 # the quoted args become the remote command on purpose
	SSH_ASKPASS="$ssh_dir/askpass" SSH_ASKPASS_REQUIRE=force ssh "${ssh_opts[@]}" "root@$ESXI_IP" "sh -s -- $(printf '%q ' "$@")"
}
host_state() { # host_state FILE: power state and folder listing (name, size, mtime) of the fixture VM
	esxi "$DATASTORE" "$VM" >"$1" <<'EOF'
set -e
id=$(vim-cmd vmsvc/getallvms | awk -v n="$2" '$2 == n { print $1 }')
echo "$2 vmid=$id $(vim-cmd vmsvc/power.getstate "$id" | tail -1)"
ls -l --full-time "/vmfs/volumes/$1/$2" >/tmp/gaia-t11-ls
awk 'NR > 1 { print "  " $0 }' /tmp/gaia-t11-ls
rm -f /tmp/gaia-t11-ls
EOF
	# A listing that failed would compare as unchanged: require the vmx, the descriptor and the extent.
	(($(grep -c '^  ' "$1") >= 3)) || { echo "UNKNOWN: no folder listing in $1"; cat "$1"; exit 3; }
}
# shellcheck disable=SC2329 # invoked by the trap
cleanup() {
	rm -f "$KEY"
	if [[ "${KEEP:-}" != 1 ]]; then
		"$FIXTURE" delete "$ESXI_IP" || echo "WARNING: fixture delete failed"
		rm -rf "$WORK" "$IMAGE_DIR" /var/lib/gaia/t11-fixture
	fi
	ssh -o ControlPath="$ssh_dir/ssh.sock" -O exit "root@$ESXI_IP" 2>/dev/null || true
	rm -rf "$ssh_dir"
	sync
	fstrim -a >/dev/null 2>&1 || true
}
trap cleanup EXIT

section "date / commit"
date -u +%Y-%m-%dT%H:%M:%SZ
git -C "$repo_root" rev-parse HEAD 2>/dev/null || echo "not a git checkout"
echo "output: $OUT_DIR"

section "build"
owner=$(stat -c %U "$repo_root")
if [[ "$owner" == root ]]; then
	make -C "$repo_root" -s build
else
	runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" make -C "$repo_root" -s build
fi
go version -m "$BIN/gaia" | grep -E 'vcs\.(revision|modified)' || echo "no VCS stamp"

section "tools"
virt-v2v --version
virsh -q version | head -4
free_kib=$(df -Pk /var/lib/libvirt/images | awk 'NR == 2 { print $4 }')
((free_kib >= 80 * 1048576)) || { echo "UNKNOWN: $((free_kib / 1048576)) GiB free, needs 80 GiB"; exit 3; }
df -h /var/lib/libvirt/images

section "virtio-win (Fedora)"
if [[ ! -s "$VIRTIO_ISO" ]]; then
	curl -fsSL -o "$VIRTIO_ISO.part" "$VIRTIO_URL"
	mv "$VIRTIO_ISO.part" "$VIRTIO_ISO"
fi
jq -n --arg url "$VIRTIO_URL" --argjson bytes "$(stat -c %s "$VIRTIO_ISO")" --arg sha256 "$(sha256sum "$VIRTIO_ISO" | cut -d' ' -f1)" \
	--arg volume "$(isoinfo -d -i "$VIRTIO_ISO" | sed -n 's/^Volume id: //p')" '{url: $url, bytes: $bytes, sha256: $sha256, volume_id: $volume}' |
	tee "$OUT_DIR/virtio-win.json" | jq -c .

section "read-only guard: allowlist tests (go test, against vcsim)"
(cd "$repo_root" && runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" \
	go test -count=1 -v -run 'TestSOAPOperation|TestInventoryThroughGuard|TestWritesAreBlocked|TestDownload' ./internal/vsphereread/) |
	tee "$OUT_DIR/guard-tests.txt" | grep -E '^(=== RUN|--- |PASS|FAIL|ok )'
grep -q '^ok ' "$OUT_DIR/guard-tests.txt" && guard_tests=true || guard_tests=false

section "fixture (lab only: install on gaia-lab, ESXi host shell)"
hostd_fixture=$(esxi <<<'wc -l < /var/log/hostd.log')
"$FIXTURE" create "$ESXI_IP"
cp "$IMAGES/$VM.qcow2.json" "$OUT_DIR/fixture-build.json"
esxi "$hostd_fixture" >"$OUT_DIR/hostd-fixture.log" <<'EOF'
tail -n +"$(($1 + 1))" /var/log/hostd.log
EOF
# Reads also create hostd tasks (vim.SearchIndex.find*, T8); any other task is a write. The fixture's vim-cmd
# calls are the positive control: they must show up as writes.
read_tasks() { grep 'Task Created' "$1" | grep -c -E 'vim\.SearchIndex\.find' || true; }
write_tasks() { grep 'Task Created' "$1" | grep -c -v -E 'vim\.SearchIndex\.find' || true; }
fixture_tasks=$(write_tasks "$OUT_DIR/hostd-fixture.log")
echo "hostd.log while the fixture was made (positive control): $fixture_tasks write task(s)"
grep 'Task Created' "$OUT_DIR/hostd-fixture.log" | sed 's/.*Task Created : /  /' | sort | uniq -c
host_state "$OUT_DIR/host-before.txt"
cat "$OUT_DIR/host-before.txt"

section "gaia migrate-check (read-only vSphere API)"
rm -f "$KEY"
"$BIN/gaia" evidence keygen -key "$KEY" -pub "$OUT_DIR/evidence-pub.pem" -json | jq -c '{fingerprint}'
# No template given: the guestId (windows2019srvNext_64Guest) selects windows-basic. Windows' first boot after the
# conversion installs devices, so the boot timeout is longer than T8's.
jq -n --arg vm "$VM" '{($vm): {boot_timeout_s: 900, probe_timeout_s: 300}}' >"$OUT_DIR/checks.json"
hostd_before=$(esxi <<<'wc -l < /var/log/hostd.log')
rc=0
"$BIN/gaia" migrate-check -url "https://$ESXI_IP/sdk" -insecure -password-file "$PASSWORD_FILE" -vm "$VM" \
	-checks "$OUT_DIR/checks.json" -virtio-win "$VIRTIO_ISO" -work-dir "$WORK" -image-dir "$IMAGE_DIR" -calls "$OUT_DIR/calls.jsonl" \
	-key "$KEY" -evidence-dir "$OUT_DIR/evidence" -ledger "$OUT_DIR/ledger.jsonl" -json >"$OUT_DIR/migrate.json" || rc=$?
echo "exit $rc"
esxi "$hostd_before" >"$OUT_DIR/hostd-window.log" <<'EOF'
tail -n +"$(($1 + 1))" /var/log/hostd.log
EOF
jq -c '{verdict, calls, blocked_calls, virtio_win, vms: [.vms[] | {vm, readiness, bundle_id, checks: [.checks[] | {name, verdict, seconds, detail}], timings_s}]}' "$OUT_DIR/migrate.json"
host_state "$OUT_DIR/host-after.txt"
bundle=$(jq -r '.vms[0].bundle_id // empty' "$OUT_DIR/migrate.json")
[[ -n "$bundle" ]] && cp "$OUT_DIR/evidence/$bundle/migration/virt-v2v.log" "$OUT_DIR/virt-v2v.log" 2>/dev/null || true

section "read-only proof"
echo "Gaia's call log (method path op allowed status):"
jq -r '[.method, .path, (.op // "-"), .allowed, (.status // "-")] | @tsv' "$OUT_DIR/calls.jsonl" | sort | uniq -c
tasks=$(write_tasks "$OUT_DIR/hostd-window.log")
reads=$(read_tasks "$OUT_DIR/hostd-window.log")
gets=$(jq -s 'map(select(.method == "GET")) | length' "$OUT_DIR/calls.jsonl")
echo "hostd.log while the check ran: $(wc -l <"$OUT_DIR/hostd-window.log") lines; write tasks: $tasks; read tasks: $reads (GET /folder calls: $gets)"
grep 'Task Created' "$OUT_DIR/hostd-window.log" || true
if diff -u "$OUT_DIR/host-before.txt" "$OUT_DIR/host-after.txt"; then host_unchanged=true; else host_unchanged=false; fi
echo "fixture VM (power state, folder listing) unchanged: $host_unchanged"
# The disk Gaia downloaded is the disk the fixture uploaded: the flat extent's sha256 against the fixture's.
fixture_sha=$(cat /var/lib/gaia/t11-fixture/$VM.raw.sha256)
download_sha=$(jq -r '.[] | select(.name | endswith("-flat.vmdk")) | .sha256' "$WORK/$VM/downloads.json" 2>/dev/null || true)
jq -r '.[] | "\(.name) \(.bytes) \(.sha256)"' "$WORK/$VM/downloads.json" 2>/dev/null || true
echo "fixture disk sha256 $fixture_sha, downloaded extent sha256 ${download_sha:-none}"
[[ "$fixture_sha" == "$download_sha" ]] && same_disk=true || same_disk=false

section "what the boot check proves: the same Windows disk, unconverted, on two sandboxes"
# The disk as installed (AHCI and e1000e drivers only), outside the migration flow: on its own hardware the check
# must pass; on virtio storage there is no driver for the boot disk, so Windows cannot boot and the check must fail.
control() { # control NAME DISK_BUS NIC TIMEOUT_S
	jq -n --arg vm "$VM-$1" --arg bus "$2" --arg nic "$3" --arg mac "$MAC" --argjson t "$4" \
		'{vm: $vm, template: "windows-basic", boot_timeout_s: $t, probe_timeout_s: 300,
		  machine: {firmware: "bios", memory_mib: 4096, vcpus: 2, disk_bus: $bus, nic_model: $nic, mac: $mac}}' >"$OUT_DIR/control-$1.json"
	local crc=0
	"$BIN/gaia" verify -config "$OUT_DIR/control-$1.json" -image "$IMAGES/$VM.qcow2" -format qcow2 -key "$KEY" \
		-evidence-dir "$OUT_DIR/evidence" -ledger "$OUT_DIR/ledger.jsonl" -json >"$OUT_DIR/control-$1-result.json" || crc=$?
	echo "control $1 ($2 disk, $3 NIC): exit $crc"
	jq -c '{vm, verdict, bundle_id, checks: [.checks[] | {name, verdict, seconds, detail}], screenshots, timings_s}' "$OUT_DIR/control-$1-result.json"
}
control native sata e1000e 900
control no-virtio-driver virtio e1000e 300

section "gaia evidence verify"
erc=0
"$BIN/gaia" evidence verify -pubkey "$OUT_DIR/evidence-pub.pem" -ledger "$OUT_DIR/ledger.jsonl" -evidence-dir "$OUT_DIR/evidence" -json >"$OUT_DIR/evidence-verify.json" || erc=$?
echo "exit $erc"
jq -c '{verdict, entries: .report.entries, bundles: [.report.bundles[] | {bundle_id, files, problems}], problems: .report.problems}' "$OUT_DIR/evidence-verify.json"

section "gate (CLAUDE.md T11)"
jq -n --slurpfile m "$OUT_DIR/migrate.json" --slurpfile ev "$OUT_DIR/evidence-verify.json" --slurpfile calls "$OUT_DIR/calls.jsonl" \
	--slurpfile native "$OUT_DIR/control-native-result.json" --slurpfile broken "$OUT_DIR/control-no-virtio-driver-result.json" \
	--arg vm "$VM" --argjson guard_tests "$guard_tests" --argjson tasks "$tasks" --argjson reads "$reads" --argjson gets "$gets" \
	--argjson fixture_tasks "${fixture_tasks:-0}" --argjson host_unchanged "$host_unchanged" --argjson same_disk "$same_disk" '
	($m[0].vms | map(select(.vm == $vm)) | first) as $v
	| ($v.checks // [] | map(select(.name == "boot")) | first) as $boot
	| def boot($r): ($r.checks | map(select(.name == "boot")) | first);
	{
	  a_windows_ready: {readiness: $v.readiness, bundle: $v.bundle_id, evidence_verify: $ev[0].verdict,
	                    pass: ($v.readiness == "OK" and $ev[0].verdict == "OK")},
	  b_zero_writes: {allowlist_tests_pass: $guard_tests, calls: ($calls | length), blocked: ($calls | map(select(.allowed | not)) | length),
	                  ops: ($calls | map(.op // (.method + " /folder")) | unique),
	                  non_read: ($calls | map(select((.method == "POST" and .path == "/sdk") or ((.method == "GET" or .method == "HEAD") and (.path | startswith("/folder/"))) | not)) | length),
	                  hostd_write_tasks_during_check: $tasks, hostd_read_tasks_during_check: $reads, get_folder_calls: $gets,
	                  hostd_write_tasks_by_fixture_positive_control: $fixture_tasks, fixture_vm_unchanged: $host_unchanged,
	                  pass: ($guard_tests and ($calls | length) > 0 and ($calls | all(.allowed)) and $tasks == 0 and $reads == $gets
	                         and $fixture_tasks > 0 and $host_unchanged)},
	  c_boot_check: {check: $boot.name, type: $boot.type, verdict: $boot.verdict, seconds: $boot.seconds, detail: $boot.detail,
	                 control_native: {verdict: $native[0].verdict, boot: boot($native[0]).verdict},
	                 control_no_virtio_driver: {verdict: $broken[0].verdict, boot: boot($broken[0]).verdict, detail: boot($broken[0]).detail},
	                 pass: ($boot.type == "rdp" and $boot.verdict == "OK" and boot($native[0]).verdict == "OK"
	                        and boot($broken[0]).verdict == "CRITICAL")},
	  downloaded_disk_is_fixture_disk: $same_disk
	} | . + {verdict: (if .a_windows_ready.pass and .b_zero_writes.pass and .c_boot_check.pass and .downloaded_disk_is_fixture_disk then "OK"
	                   elif .a_windows_ready.readiness == null or .a_windows_ready.readiness == "UNKNOWN" then "UNKNOWN"
	                   else "CRITICAL" end)}' | tee "$OUT_DIR/gate.json"

section "result"
verdict=$(jq -r .verdict "$OUT_DIR/gate.json")
echo "$verdict"
echo "(gate.json, migrate.json, calls.jsonl, control-*.json, evidence/, ledger.jsonl in $OUT_DIR)"
case $verdict in
OK) exit 0 ;;
CRITICAL) exit 2 ;;
*) exit 3 ;;
esac
