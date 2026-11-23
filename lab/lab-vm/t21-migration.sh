#!/usr/bin/env bash
# Runs T21 on gaia-lab: migration checks through gaia server (ADR-003, ADR-005). T8 and T11 again, now as server jobs
# from the API and the command line, plus a UEFI guest and the negative controls:
#   gaia-t8-linux        Debian, BIOS                     -> OK (boot, SSH, HTTP /health)
#   gaia-t8-broken       the same, MBR boot code zeroed   -> CRITICAL (no boot)
#   gaia-t21-uefi        MBR boot code zeroed, firmware efi: boots only through UEFI -> OK, firmware UEFI
#   gaia-t11-win         Windows Server 2022, BIOS        -> UNKNOWN before virtio-win is in place (not converted),
#                                                            then (since T28, ADR-005 B and C) CRITICAL driver_install_loop:
#                                                            WinRM and RDP answer once, but virtio-win 0.1.302's smbus driver
#                                                            fails to install at first boot and Windows restarts on every
#                                                            boot, so it does not stay up for settle_s (T21's OK was a false
#                                                            positive); checked again with exclude_drivers ["smbus"]: WARNING
#                                                            (ADR-005 F: its first-boot restarts listed, before it stays up),
#                                                            WinRM (an Identify, or as Windows Server's default an NTLM
#                                                            CHALLENGE with the computer's name and Windows version) and RDP
#                                                            answer and keep answering, the left-out driver a warning step
#   gaia-t21-win-broken  the same disk, MBR boot code zeroed -> CRITICAL, the WinRM check saying why it cannot pass
# The fixtures (lab/esxi/fixture-t8.sh, fixture-t11.sh) are made through the ESXi host's own shell (lab only, rule 9).
#
# gaia server, its sandbox helper and the converter service run from the packaged units: gaia server as its own user
# (gaia-t21, in the group gaia-convert), the sandbox helper as root, the converter as gaia-convert with rootless podman
# and the converter image built from packaging/converter/Containerfile and pinned by its ID (/etc/gaia/convert.json).
# Every check is queued with `gaia sources check`, waited for with `gaia migration-checks get`, and its readiness
# report taken with `gaia migration-checks report` and verified with gaia evidence verify and the independent Python
# verifier (bundle mode).
#
# Zero writes to ESXi, as in T8/T11: every vSphere call Gaia made (the bundles' call logs and the inventory's) is
# read-only and none was blocked; hostd.log shows no write task while the server ran the checks, and as many read
# tasks as GET /folder calls; the fixture's own writes show up (positive control); every fixture VM's folder and power
# state are the same before and after.
#
# Usage (on gaia-lab, as root, after setup.sh):  systemd-run --unit=gaia-t21 --collect -E HOME=/root lab/lab-vm/t21-migration.sh ESXI_IP
#   lab/lab-vm/t21-migration.sh --gate OUT_DIR   verifies a run's readiness reports again and computes its gate from what
#                                               the run recorded (no fixtures, no server), into OUT_DIR/gate.json
# Env: PASSWORD_FILE (/root/.gaia-esxi-password, never printed), DATASTORE (datastore1), OUT_DIR (/root/t21-<UTC>),
#      KEEP=1 keeps the fixtures and the server.
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

if [[ "${1:-}" == --gate ]]; then
	GATE_ONLY=1 OUT_DIR="${2:?OUT_DIR}" ESXI_IP=-
else
	ESXI_IP="${1:?ESXi host IP}"
fi
PASSWORD_FILE="${PASSWORD_FILE:-/root/.gaia-esxi-password}"
DATASTORE="${DATASTORE:-datastore1}"
OUT_DIR="${OUT_DIR:-/root/t21-$(date -u +%Y%m%dT%H%M%SZ)}"
IMAGES=/var/lib/gaia/images
LV_IMAGES=/var/lib/libvirt/images
VIRTIO_ISO="$IMAGES/virtio-win-0.1.302.iso"
SRV_USER=gaia-t21
SRV_HOME=/var/lib/gaia-t21
SRV_BIN=/usr/local/sbin/gaia-t21
UNIT=gaia-t21-server
SB_UNIT=gaia-t21-sandbox
SB_HOME=/var/lib/gaia-t21-sandbox
SB_SOCK=/run/gaia-t21-sandbox/sandbox.sock
SB_WORK=$LV_IMAGES/gaia-t21-sandbox
CONV_USER=gaia-convert
CONV_HOME=/var/lib/gaia-convert
# Since T26 (ADR-008) the server needs a DNS name for WebAuthn and the admin signs in with a second factor: the name
# gaia-lab.test (in /etc/hosts, as t27-lab.sh), and the admin's security key in software (lab/tools/softkey).
API=https://gaia-lab.test:8443
WORK=/root/t21-work
LINUX_VMS=(gaia-t8-linux gaia-t8-broken gaia-t21-uefi)
WIN_VMS=(gaia-t11-win gaia-t21-win-broken)
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
PYV="$repo_root/docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in go jq make qemu-img virsh virt-customize podman newuidmap catatonit curl ssh python3 fstrim; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done
[[ -e /usr/share/qemu/firmware/60-edk2-x86_64.json ]] || { echo "MISSING: OVMF (run lab/lab-vm/setup.sh)"; exit 3; }
if [[ ! -s "$PASSWORD_FILE" ]] || [[ "$(stat -c %a "$PASSWORD_FILE")" != 600 ]]; then
	echo "UNKNOWN: the ESXi root password must be in $PASSWORD_FILE with mode 0600"
	exit 3
fi
[[ -e /dev/kvm ]] || { echo "UNKNOWN: /dev/kvm is missing"; exit 3; }
[[ -s "$VIRTIO_ISO" ]] || { echo "UNKNOWN: $VIRTIO_ISO is missing (T11's download)"; exit 3; }
section() { printf '\n== %s\n' "$*"; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
gaia() { "$BIN/gaia" "$@"; }
api() { gaia "$@" -json; }
# sk METHOD PATH [JSON] [-o FILE]: a call that may need a step-up, with the admin's key in software
sk() { "$BIN/softkey" call -cli-config /root/.config/gaia/cli.json -key "$WORK/admin-key.json" "$@"; }

# Observations on the ESXi host, lab only, through the fixture's SSH (read-only commands).
ssh_dir=$(mktemp -d)
cat >"$ssh_dir/askpass" <<EOF
#!/bin/sh
cat "$PASSWORD_FILE"
EOF
chmod 700 "$ssh_dir/askpass"
ssh_opts=(-o ControlMaster=auto -o "ControlPath=$ssh_dir/ssh.sock" -o ControlPersist=900
	-o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_dir/known_hosts"
	-o NumberOfPasswordPrompts=1 -o PubkeyAuthentication=no -o ConnectTimeout=10)
esxi() {
	# shellcheck disable=SC2029 # the quoted args become the remote command on purpose
	SSH_ASKPASS="$ssh_dir/askpass" SSH_ASKPASS_REQUIRE=force ssh "${ssh_opts[@]}" "root@$ESXI_IP" "sh -s -- $(printf '%q ' "$@")"
}
hostd_lines() { esxi <<<'wc -l < /var/log/hostd.log'; }
hostd_since() { # hostd_since LINE FILE
	esxi "$1" >"$2" <<'EOF'
tail -n +"$(($1 + 1))" /var/log/hostd.log
EOF
}
# Reads also create hostd tasks (vim.SearchIndex.find*, T8); any other task is a write.
read_tasks() { grep 'Task Created' "$1" | grep -c -E 'vim\.SearchIndex\.find' || true; }
write_tasks() { grep 'Task Created' "$1" | grep -c -v -E 'vim\.SearchIndex\.find' || true; }
host_state() { # host_state FILE: power state and folder listing (name, size, mtime) of every fixture VM
	esxi "$DATASTORE" "${LINUX_VMS[@]}" "${WIN_VMS[@]}" >"$1" <<'EOF'
set -e
ds=$1
shift
for vm in "$@"; do
	id=$(vim-cmd vmsvc/getallvms | awk -v n="$vm" '$2 == n { print $1 }')
	echo "$vm vmid=$id $(vim-cmd vmsvc/power.getstate "$id" | tail -1)"
	ls -l --full-time "/vmfs/volumes/$ds/$vm" >/tmp/gaia-t21-ls
	awk 'NR > 1 { print "  " $0 }' /tmp/gaia-t21-ls
done
rm -f /tmp/gaia-t21-ls
EOF
	(($(grep -c '^  ' "$1") >= 3 * 5)) || { echo "UNKNOWN: no complete folder listing in $1"; cat "$1"; exit 3; }
}

# finish verifies the readiness reports, proves zero writes and computes the gate, from OUT_DIR only.
finish() {
section "readiness reports: gaia evidence verify and the independent verifier (bundle mode)"
reports='[]'
for f in "$OUT_DIR"/check-*.json; do
	id=$(jq -r .id "$f")
	d="$OUT_DIR/reports/$id"
	if [[ -z "${GATE_ONLY:-}" ]]; then
		mkdir -p "$d"
		sk -o "$d/report.tar.gz" GET "/api/v1/migration-checks/$id/report" >/dev/null # a step-up download
		tar -C "$d" -xzf "$d/report.tar.gz"
	fi
	root=$(find "$d" -mindepth 1 -maxdepth 1 -type d -name 'gaia-readiness-*')
	bundle=$(jq -r .bundle "$root/readiness.json")
	rc=0
	gaia evidence verify -pubkey "$root/keys/evidence-key-current.pub" -bundle "$root/bundles/$bundle" -json >"$d/gaia-verify.json" || rc=$?
	prc=0
	python3 -I "$PYV" bundle "$root/bundles/$bundle" --key "$root/keys/evidence-key-current.pub" --json >"$d/python-verify.json" || prc=$?
	sums=true
	(cd "$root" && sha256sum --quiet -c SHA256SUMS) || sums=false
	reports=$(jq -c --arg vm "$(jq -r .vm "$root/readiness.json")" --argjson id "$id" --argjson rc "$rc" --argjson prc "$prc" --argjson sums "$sums" \
		--slurpfile g "$d/gaia-verify.json" --slurpfile p "$d/python-verify.json" --slurpfile r "$root/readiness.json" \
		'. + [{check: $id, vm: $vm, readiness: $r[0].readiness, sha256sums: $sums, gaia: {exit: $rc, verdict: $g[0].verdict},
		       python: {exit: $prc, verdict: $p[0].verdict}}]' <<<"$reports")
	cp "$root/readiness.json" "$OUT_DIR/reports/$id-readiness.json"
	cp "$root/README.txt" "$OUT_DIR/reports/$id-README.txt"
done
echo "$reports" | jq -c '.[]'

section "read-only proof"
if [[ -z "${GATE_ONLY:-}" ]]; then # every call: the bundles' call logs and the inventory's
	: >"$OUT_DIR/calls.jsonl"
	for f in "$OUT_DIR"/reports/*/gaia-readiness-*/bundles/*/migration/calls.jsonl; do cat "$f" >>"$OUT_DIR/calls.jsonl"; done
	cat "$SRV_HOME/data/migration/source-$src-calls.jsonl" >>"$OUT_DIR/calls.jsonl"
fi
jq -r '[.method, .path, (.op // "-"), .allowed, (.status // "-")] | @tsv' "$OUT_DIR/calls.jsonl" | sort | uniq -c
tasks=$(write_tasks "$OUT_DIR/hostd-window.log")
reads=$(read_tasks "$OUT_DIR/hostd-window.log")
gets=$(jq -s 'map(select(.method == "GET")) | length' "$OUT_DIR/calls.jsonl")
echo "hostd.log while the server ran the checks: write tasks $tasks, read tasks $reads (GET /folder calls: $gets)"
grep 'Task Created' "$OUT_DIR/hostd-window.log" | sed 's/.*Task Created : /  /' | sort | uniq -c || true
if diff -u "$OUT_DIR/host-before.txt" "$OUT_DIR/host-after.txt"; then host_unchanged=true; else host_unchanged=false; fi
echo "fixture VMs (power state, folder listing) unchanged: $host_unchanged"

section "gate (CLAUDE.md T21)"
image=$(jq -r .image "$OUT_DIR/convert.json")
fixture_tasks=$(write_tasks "$OUT_DIR/hostd-fixture.log")
step='def step(n): (.detail.checks // []) | map(select(.name == n)) | first // {};'
jq -n --argjson calls "$(jq -s . "$OUT_DIR/calls.jsonl")" --argjson reports "$reports" --arg image "$image" \
	--argjson tasks "$tasks" --argjson reads "$reads" --argjson gets "$gets" --argjson fixture_tasks "${fixture_tasks:-0}" \
	--argjson host_unchanged "$host_unchanged" --slurpfile conv "$OUT_DIR/converter.json" \
	--slurpfile lin "$OUT_DIR/check-gaia-t8-linux.json" --slurpfile brk "$OUT_DIR/check-gaia-t8-broken.json" \
	--slurpfile uefi "$OUT_DIR/check-gaia-t21-uefi.json" --slurpfile win "$OUT_DIR/check-gaia-t11-win.json" \
	--slurpfile wbrk "$OUT_DIR/check-gaia-t21-win-broken.json" --slurpfile nov "$OUT_DIR/check-gaia-t11-win-no-virtio.json" \
	--slurpfile wex "$OUT_DIR/check-gaia-t11-win@no-smbus.json" "$step"'
	def s(x): {readiness: x.readiness, reason: x.reason, firmware: x.firmware, bundle: x.bundle_id,
	           steps: [(x.detail.checks // [])[] | {name, verdict, detail: (.detail | .[0:240])}]};
	{a_t8_linux: {ready: s($lin[0]), broken: s($brk[0]),
	              pass: ($lin[0].readiness == "OK" and $brk[0].readiness == "CRITICAL" and ($brk[0] | step("boot").verdict) == "CRITICAL")},
	 b_t11_windows: {as_is: (s($win[0]) + {code: $win[0].detail.code, drivers: $win[0].detail.drivers, restarts: $win[0].detail.restarts}),
	                 without_smbus: (s($wex[0]) + {first_boot_restarts_s: $wex[0].detail.restarts}),
	                 broken: s($wbrk[0]), without_virtio_win: s($nov[0]),
	                 t21_ok_was_a_false_positive: (($win[0] | step("boot").verdict) == "OK" and ($win[0] | step("stable").verdict) == "CRITICAL"),
	                 pass: ($win[0].readiness == "CRITICAL" and $win[0].detail.code == "driver_install_loop" and ($win[0].detail.drivers | index("smbus")) != null
	                        and ($win[0] | step("drivers").verdict) == "CRITICAL" and ($win[0] | step("stable").verdict) == "CRITICAL"
	                        and $wex[0].readiness == "WARNING"
	                        and ($wex[0] | step("boot") | .type == "winrm" and .verdict == "OK" and (.detail | contains("IdentifyResponse") or (contains("NTLM NEGOTIATE with a CHALLENGE from computer") and test("Windows [0-9]+[.][0-9]+ build [0-9]+"))))
	                        and ($wex[0] | step("rdp").verdict) == "OK" and ($wex[0] | step("stable").verdict) == "OK"
	                        and ($wex[0] | step("drivers_excluded") | .verdict == "WARNING" and (.detail | contains("smbus")))
	                        and ([$wex[0].detail.checks[] | select(.verdict != "OK") | .name] == ["drivers_excluded"])
	                        and (($wex[0].detail.restarts // []) | length) > 0 and ($wex[0].reason | contains("on its first boot"))
	                        and $wbrk[0].readiness == "CRITICAL" and ($wbrk[0] | step("boot").detail | contains("WinRM answers only from a running Windows"))
	                        and $nov[0].readiness == "UNKNOWN" and ($nov[0] | step("conversion").detail | contains("virtio-win")))},
	 c_uefi: {check: s($uefi[0]),
	          pass: ($uefi[0].readiness == "OK" and $uefi[0].firmware == "efi" and ($uefi[0] | step("firmware").detail | startswith("UEFI")))},
	 d_zero_writes: {calls: ($calls | length), blocked: ($calls | map(select(.allowed | not)) | length),
	                 ops: ($calls | map(.op // .method) | unique), hostd_write_tasks_during_checks: $tasks,
	                 hostd_read_tasks_during_checks: $reads, get_folder_calls: $gets, hostd_write_tasks_by_fixtures_positive_control: $fixture_tasks,
	                 fixture_vms_unchanged: $host_unchanged,
	                 pass: (($calls | length) > 0 and ($calls | all(.allowed)) and $tasks == 0 and $reads == $gets and $fixture_tasks > 0 and $host_unchanged)},
	 e_reports: {reports: $reports, pass: ($reports | length == 7 and all(.sha256sums and .gaia.exit == 0 and .gaia.verdict == "OK" and .python.exit == 0 and .python.verdict == "OK"))},
	 f_converter: {image: $image, versions: $conv[0].info.versions, virtio_win: ($conv[0].info.virtio_win | {version, state, sha256}),
	               bundles_name_the_image: ([$lin[0], $uefi[0], $win[0], $wex[0], $wbrk[0], $brk[0]] | all(.detail.converter.image == $image)),
	               pass: ($conv[0].info.image == $image and $conv[0].info.image_present and $conv[0].info.virtio_win.state == "ok"
	                      and ([$lin[0], $uefi[0], $win[0], $wex[0], $wbrk[0], $brk[0]] | all(.detail.converter.image == $image)))}}
	| . + {verdict: (if [.a_t8_linux, .b_t11_windows, .c_uefi, .d_zero_writes, .e_reports, .f_converter] | all(.pass) then "OK" else "CRITICAL" end)}' |
	tee "$OUT_DIR/gate.json" | jq -c '{verdict, a: .a_t8_linux.pass, b: .b_t11_windows.pass, c: .c_uefi.pass, d: .d_zero_writes.pass, e: .e_reports.pass, f: .f_converter.pass}'
echo "out: $OUT_DIR"
[[ $(jq -r .verdict "$OUT_DIR/gate.json") == OK ]]
}

if [[ -n "${GATE_ONLY:-}" ]]; then
	rc=0
	finish 2>&1 | tee "$OUT_DIR/gate.log" || rc=$?
	exit $((rc ? 2 : 0))
fi
mkdir -p "$OUT_DIR/reports"
exec > >(tee "$OUT_DIR/t21.log") 2>&1
ln -sfn "$OUT_DIR" /root/t21-latest

cleanup() {
	[[ "${KEEP:-}" == 1 ]] && return 0
	for u in "$UNIT" "$SB_UNIT" gaia-convert; do
		systemctl stop "$u" >/dev/null 2>&1 || true
		systemctl reset-failed "$u" >/dev/null 2>&1 || true
	done
	rm -f "/run/systemd/system/$UNIT.service" "/run/systemd/system/$SB_UNIT.service" /run/systemd/system/gaia-convert.service
	systemctl daemon-reload
	rm -rf "$SRV_HOME" "$SB_HOME" "$SB_WORK" "$CONV_HOME/jobs" "$CONV_HOME/virtio-win-0.1.302.iso" /root/.config/gaia "$WORK"
	userdel "$SRV_USER" >/dev/null 2>&1 || true
	"$repo_root/lab/esxi/fixture-t8.sh" delete "$ESXI_IP" || echo "WARNING: fixture-t8 delete failed"
	"$repo_root/lab/esxi/fixture-t11.sh" delete "$ESXI_IP" || echo "WARNING: fixture-t11 delete failed"
	VARIANT=broken "$repo_root/lab/esxi/fixture-t11.sh" delete "$ESXI_IP" || echo "WARNING: fixture-t11 (broken) delete failed"
	rm -rf /var/lib/gaia/t8-fixture /var/lib/gaia/t11-fixture
}
# shellcheck disable=SC2329 # invoked by the trap
on_exit() {
	if [[ -n "${SRV_STARTED:-}" ]]; then
		for u in "$UNIT" "$SB_UNIT" gaia-convert; do
			journalctl -u "$u" --no-pager -o short-iso-precise >"$OUT_DIR/${u#gaia-}.journal" 2>&1 || true
		done
	fi
	cleanup
	ssh -o ControlPath="$ssh_dir/ssh.sock" -O exit "root@$ESXI_IP" 2>/dev/null || true
	rm -rf "$ssh_dir"
	sync
	fstrim -a >/dev/null 2>&1 || true
}
trap on_exit EXIT

section "date / commit"
date -u +%Y-%m-%dT%H:%M:%SZ
git -C "$repo_root" rev-parse HEAD 2>/dev/null || echo "not a git checkout"

section "build"
owner=$(stat -c %U "$repo_root")
asowner() {
	if [[ "$owner" == root ]]; then "$@"; else runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" "$@"; fi
}
asowner make -C "$repo_root" -s build
asowner go build -C "$repo_root" -o "$BIN/softkey" ./lab/tools/softkey
go version -m "$BIN/gaia" | grep -E 'vcs\.(revision|modified)' || echo "no VCS stamp"
virsh -q version
podman --version

section "free space"
cleanup
install -d -m 0700 "$WORK"
grep -q " gaia-lab.test\$" /etc/hosts || echo "$(hostname -I | awk '{ print $1 }') gaia-lab.test" >>/etc/hosts
free_kib=$(df -Pk /var/lib | awk 'NR == 2 { print $4 }')
((free_kib >= 60 * 1048576)) || { echo "UNKNOWN: $((free_kib / 1048576)) GiB free on /var/lib, needs 60 GiB"; exit 3; }
df -h /var/lib
esxi "$DATASTORE" <<'EOF'
df -h | awk -v d="$1" 'NR == 1 || $NF ~ d'
EOF

section "fixtures (lab only: ESXi host shell)"
hostd_fixture=$(hostd_lines)
"$repo_root/lab/esxi/fixture-t8.sh" create "$ESXI_IP"
"$repo_root/lab/esxi/fixture-t11.sh" create "$ESXI_IP"
VARIANT=broken "$repo_root/lab/esxi/fixture-t11.sh" create "$ESXI_IP"
hostd_since "$hostd_fixture" "$OUT_DIR/hostd-fixture.log"
fixture_tasks=$(write_tasks "$OUT_DIR/hostd-fixture.log")
echo "hostd.log while the fixtures were made (positive control): $fixture_tasks write task(s)"
host_state "$OUT_DIR/host-before.txt"
cat "$OUT_DIR/host-before.txt"

section "converter service (user $CONV_USER, rootless podman; install guide step 10d)"
id "$CONV_USER" >/dev/null 2>&1 || useradd --system --home-dir "$CONV_HOME" --create-home --shell /usr/sbin/nologin "$CONV_USER"
usermod -aG kvm "$CONV_USER"
grep -q "^$CONV_USER:" /etc/subuid || usermod --add-subuids 1000000-1065535 --add-subgids 1000000-1065535 "$CONV_USER"
install -d -o "$CONV_USER" -g "$CONV_USER" -m 0750 "$CONV_HOME" "$CONV_HOME/build"
install -d -o "$CONV_USER" -g "$CONV_USER" -m 2770 "$CONV_HOME/jobs"
install -m 0644 "$repo_root/packaging/converter/Containerfile" "$CONV_HOME/build/Containerfile"
build_start=$(date +%s)
systemd-run --wait --quiet --unit=gaia-t21-convert-build --collect --uid="$CONV_USER" --gid="$CONV_USER" -p RuntimeDirectory=gaia-convert \
	-p WorkingDirectory="$CONV_HOME/build" -E HOME="$CONV_HOME" -E XDG_RUNTIME_DIR=/run/gaia-convert -E TMPDIR="$CONV_HOME/tmp" \
	bash -c "mkdir -p $CONV_HOME/tmp && podman --cgroup-manager=cgroupfs build -t gaia-converter . >$CONV_HOME/build/build.log 2>&1 && podman --cgroup-manager=cgroupfs image inspect --format '{{.Id}}' gaia-converter >$CONV_HOME/build/image-id"
tail -3 "$CONV_HOME/build/build.log"
image="sha256:$(cat "$CONV_HOME/build/image-id")"
echo "converter image $image (build $(($(date +%s) - build_start)) s)"
mkdir -p /etc/gaia
jq -n --arg i "$image" '{image: $i}' >/etc/gaia/convert.json
chmod 0644 /etc/gaia/convert.json
cp /etc/gaia/convert.json "$OUT_DIR/convert.json"
install -m 0755 "$BIN/gaia" "$SRV_BIN"
sed -e "s|^ExecStart=/usr/bin/gaia |ExecStart=$SRV_BIN |" "$repo_root/packaging/systemd/gaia-convert.service" >/run/systemd/system/gaia-convert.service

section "gaia server and sandbox helper (packaged units)"
useradd --system --home-dir "$SRV_HOME" --shell /usr/sbin/nologin -G "$CONV_USER" "$SRV_USER"
install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME" "$SRV_HOME/data"
sed -e "s|^ExecStart=.*|ExecStart=$SRV_BIN sandbox serve -group $SRV_USER -socket $SB_SOCK -images $SB_HOME/images -work-dir $SB_WORK -bundle-dir $SB_HOME/bundles|" \
	"$repo_root/packaging/systemd/gaia-sandbox.service" >"/run/systemd/system/$SB_UNIT.service"
pwfile="$SRV_HOME/admin.pw"
kitfile="$SRV_HOME/kit.pass"
head -c 24 /dev/urandom | base64 | install -m 0600 -o "$SRV_USER" -g "$SRV_USER" /dev/stdin "$pwfile"
head -c 24 /dev/urandom | base64 | install -m 0600 -o "$SRV_USER" -g "$SRV_USER" /dev/stdin "$kitfile"
runuser -u "$SRV_USER" -- "$SRV_BIN" server init -config "$SRV_HOME/server.json" -data-dir "$SRV_HOME/data" \
	-listen :8443 -hostname gaia-lab.test -public-url "$API" -admin admin -password-file "$pwfile" -kit-passphrase-file "$kitfile" \
	-json >"$WORK/server-init.json" # never in OUT_DIR
cert=$(jq -r .result.tls_certificate_sha256 "$WORK/server-init.json")
jq --arg s "$SB_SOCK" --arg i "$SB_HOME/images" '. + {sandbox_socket: $s, sandbox_images_dir: $i}' "$SRV_HOME/server.json" >"$OUT_DIR/server.json"
install -m 0640 -o "$SRV_USER" -g "$SRV_USER" "$OUT_DIR/server.json" "$SRV_HOME/server.json"
sed -e "s|^User=.*|User=$SRV_USER|" -e "s|^Group=.*|Group=$SRV_USER|" \
	-e "s|^ExecStart=.*|ExecStart=$SRV_BIN server run -config $SRV_HOME/server.json|" \
	-e "s|^ReadWritePaths=.*|ReadWritePaths=$SRV_HOME -$SB_HOME/images -$CONV_HOME/jobs|" \
	-e "s|gaia-sandbox.service|$SB_UNIT.service gaia-convert.service|" \
	"$repo_root/packaging/systemd/gaia.service" >"/run/systemd/system/$UNIT.service"
systemctl daemon-reload
systemctl start gaia-convert "$SB_UNIT"
for ((i = 0; i < 30; i++)); do [[ -S $SB_SOCK && -S /run/gaia-convert/convert.sock ]] && break; sleep 1; done
systemctl start "$UNIT"
SRV_STARTED=1
for ((i = 0; i < 30; i++)); do curl -fsk "$API/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
"$BIN/softkey" login -cli-config /root/.config/gaia/cli.json -server "$API" -fingerprint "$cert" -user admin \
	-password-file "$pwfile" -key "$WORK/admin-key.json" >/dev/null
api system | jq -c '{version, schema_version}' | tee "$OUT_DIR/system.json"
systemctl show gaia-convert -p User -p NoNewPrivileges -p CapabilityBoundingSet | tee "$OUT_DIR/convert-unit.txt"
api api GET /api/v1/converter | tee "$OUT_DIR/converter-before.json" | jq -c '{reachable, image: .info.image, present: .info.image_present, virtio_win: .info.virtio_win.state}'

section "source (install guide step 10d) and inventory: read-only"
hostd_checks=$(hostd_lines)
GAIA_VSPHERE_PASSWORD="$(cat "$PASSWORD_FILE")" api sources add name=esxi url="https://$ESXI_IP/sdk" username=root insecure=true >"$OUT_DIR/source.json"
src=$(jq -r .id "$OUT_DIR/source.json")
api sources inventory "$src" >"$OUT_DIR/inventory.json"
jq -r '.vms[] | [.name, .power_state, .guest_id, .firmware, .disks, (.checkable // "")] | @tsv' "$OUT_DIR/inventory.json"

declare -A CHECK_ID
check() { # check VM[@LABEL] TEMPLATE [CHECKS_JSON [KEY=VALUE...]]: queue a migration check, record its id under VM[@LABEL]
	local args=("vm=${1%%@*}" "template=$2")
	[[ -n "${3:-}" ]] && args+=("checks=$3")
	args+=("${@:4}")
	CHECK_ID[$1]=$(api sources check "$src" "${args[@]}" | jq -r .id)
	log "queued: $1 as check ${CHECK_ID[$1]}"
}
wait_check() { # wait_check VM[@LABEL]: until it has a readiness; its JSON goes to OUT_DIR/check-VM[@LABEL].json
	local id=${CHECK_ID[$1]} r
	for ((i = 0; i < 360; i++)); do
		r=$(api migration-checks get "$id" | tee "$OUT_DIR/check-$1.json" | jq -r .readiness)
		[[ -n "$r" ]] && { log "$1: $r: $(jq -r .reason "$OUT_DIR/check-$1.json")"; return 0; }
		sleep 10
	done
	echo "UNKNOWN: check of $1 did not end within an hour"
	exit 3
}

section "checks: Windows before virtio-win is in place (ADR-005 §2)"
check gaia-t11-win windows-rdp
wait_check gaia-t11-win
cp "$OUT_DIR/check-gaia-t11-win.json" "$OUT_DIR/check-gaia-t11-win-no-virtio.json"

section "virtio-win 0.1.302 (the operator's download, checked against the pinned sha256)"
install -m 0644 -o "$CONV_USER" -g "$CONV_USER" "$VIRTIO_ISO" "$CONV_HOME/virtio-win-0.1.302.iso"
api api GET /api/v1/converter | tee "$OUT_DIR/converter.json" | jq -c '{reachable, image: .info.image, virtio_win: .info.virtio_win.state, versions: .info.versions}'

section "checks: every fixture VM"
health='[{"name":"health","type":"http","port":8080,"path":"/health","expect_body":"ok"}]'
for vm in "${LINUX_VMS[@]}"; do check "$vm" linux-basic "$health"; done
for vm in "${WIN_VMS[@]}"; do check "$vm" windows-rdp; done
for vm in "${LINUX_VMS[@]}" "${WIN_VMS[@]}"; do wait_check "$vm"; done
# ADR-005 C step 2 (T28): the same Windows without virtio-win's smbus driver (one check of a VM at a time)
check gaia-t11-win@no-smbus windows-rdp "" 'exclude_drivers=["smbus"]'
wait_check gaia-t11-win@no-smbus
hostd_since "$hostd_checks" "$OUT_DIR/hostd-window.log"
host_state "$OUT_DIR/host-after.txt"

finish || exit 2
