#!/usr/bin/env bash
# Runs T8 on gaia-lab: the migration check, stage 1 (cold), against the free ESXi lab host.
#   1. fixture (lab only, ESXi host shell): two powered-off Debian 12 VMs, one healthy, one with a damaged
#      bootloader (lab/esxi/fixture-t8.sh);
#   2. gaia migrate-check, read-only vSphere calls only: inventory, datastore HTTP reads of the VM folder,
#      virt-v2v -i vmx to qcow2/virtio, gaia verify in the isolated sandbox, one signed readiness bundle per VM;
#   3. the read-only proof: the allowlist tests, Gaia's own call log, and, observed on the host itself, no task
#      created in hostd.log while the check ran and the VM folders unchanged byte for byte;
#   4. the ledger and bundles verified; the fixtures deleted.
# Gates (CLAUDE.md T8): (a) Linux fixture -> readiness OK with signed evidence; (b) broken fixture -> not OK;
#   (c) zero write calls against ESXi (allowlist test + run log).
#
# Usage (on gaia-lab, as root, after setup.sh):  lab/lab-vm/t8-migration.sh ESXI_IP
#   systemd-run --unit=gaia-t8 --collect -E HOME=/root lab/lab-vm/t8-migration.sh 192.168.1.29
# The ESXi root password must be in /root/.gaia-esxi-password (mode 0600, written by Yiğit; never printed).
# Env: PASSWORD_FILE, DATASTORE (datastore1), OUT_DIR (/root/t8-<UTC timestamp>), KEEP=1 keeps the fixtures and work dirs.
# Exit: 0 OK / 2 CRITICAL (a gate failed) / 3 UNKNOWN (not checked, also when the harness failed).
set -euo pipefail

ESXI_IP="${1:?ESXi host IP}"
PASSWORD_FILE="${PASSWORD_FILE:-/root/.gaia-esxi-password}"
DATASTORE="${DATASTORE:-datastore1}"
OUT_DIR="${OUT_DIR:-/root/t8-$(date -u +%Y%m%dT%H%M%SZ)}"
VMS=(gaia-t8-linux gaia-t8-broken)
WORK=/var/lib/gaia/migrate
IMAGE_DIR=/var/lib/libvirt/images/gaia-migrate
KEY=/var/lib/gaia/t8-evidence-key.pem
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
FIXTURE="$repo_root/lab/esxi/fixture-t8.sh"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

((EUID == 0)) || { echo "run as root (sudo)"; exit 3; }
for bin in go jq make qemu-img virsh virt-v2v virt-customize dnsmasq ip ssh curl fstrim; do
	command -v "$bin" >/dev/null || { echo "MISSING: $bin (run lab/lab-vm/setup.sh)"; exit 3; }
done
if [[ ! -s "$PASSWORD_FILE" ]] || [[ "$(stat -c %a "$PASSWORD_FILE")" != 600 ]]; then
	echo "UNKNOWN: the ESXi root password must be in $PASSWORD_FILE with mode 0600 (see the T8 report)"
	exit 3
fi
[[ -e /dev/kvm ]] || { echo "UNKNOWN: /dev/kvm is missing (see T6)"; exit 3; }
mkdir -p "$OUT_DIR/evidence"
exec > >(tee "$OUT_DIR/t8.log") 2>&1
ln -sfn "$OUT_DIR" /root/t8-latest

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
host_state() { # host_state FILE: power state and folder listing (name, size, mtime) of each fixture VM
	esxi "$DATASTORE" "${VMS[@]}" >"$1" <<'EOF'
ds=$1; shift
for vm in "$@"; do
	id=$(vim-cmd vmsvc/getallvms | awk -v n="$vm" '$2 == n { print $1 }')
	echo "$vm vmid=$id $(vim-cmd vmsvc/power.getstate "$id" | tail -1)"
	ls -le "/vmfs/volumes/$ds/$vm" | awk 'NR > 1 { print "  " $0 }'
done
EOF
}
# shellcheck disable=SC2329 # invoked by the trap
cleanup() {
	rm -f "$KEY"
	if [[ "${KEEP:-}" != 1 ]]; then
		"$FIXTURE" delete "$ESXI_IP" || echo "WARNING: fixture delete failed"
		rm -rf "$WORK" "$IMAGE_DIR" /var/lib/gaia/t8-fixture
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
((free_kib >= 30 * 1048576)) || { echo "UNKNOWN: $((free_kib / 1048576)) GiB free, needs 30 GiB"; exit 3; }
df -h /var/lib/libvirt/images

section "read-only guard: allowlist tests (go test, against vcsim)"
(cd "$repo_root" && runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" \
	go test -count=1 -v -run 'TestSOAPOperation|TestInventoryThroughGuard|TestWritesAreBlocked|TestDownload' ./internal/vsphereread/) |
	tee "$OUT_DIR/guard-tests.txt" | grep -E '^(=== RUN|--- |PASS|FAIL|ok )'
grep -q '^ok ' "$OUT_DIR/guard-tests.txt" && guard_tests=true || guard_tests=false

section "fixture (lab only: ESXi host shell)"
hostd_fixture=$(esxi <<<'wc -l < /var/log/hostd.log')
"$FIXTURE" create "$ESXI_IP"
esxi "$hostd_fixture" >"$OUT_DIR/hostd-fixture.log" <<'EOF'
tail -n +"$(($1 + 1))" /var/log/hostd.log
EOF
# hostd logs a task for every operation it runs, reads included: the datastore file service resolves each
# GET /folder's dcPath with vim.SearchIndex.findByInventoryPath (first T8 run). Those are reads; any other
# task is a write. The fixture's vim-cmd calls are the positive control: they must show up as writes.
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
jq -n '{"*": {template: "linux-basic", boot_timeout_s: 300, probe_timeout_s: 120,
	checks: [{name: "http-health", type: "http", port: 8080, path: "/health", expect_status: 200, expect_body: "ok"}]},
	"gaia-t8-broken": {template: "linux-basic", boot_timeout_s: 180, probe_timeout_s: 120,
	checks: [{name: "http-health", type: "http", port: 8080, path: "/health", expect_status: 200, expect_body: "ok"}]}}' >"$OUT_DIR/checks.json"
hostd_before=$(esxi <<<'wc -l < /var/log/hostd.log')
rc=0
"$BIN/gaia" migrate-check -url "https://$ESXI_IP/sdk" -insecure -password-file "$PASSWORD_FILE" -vm "$(IFS=,; echo "${VMS[*]}")" \
	-checks "$OUT_DIR/checks.json" -work-dir "$WORK" -image-dir "$IMAGE_DIR" -calls "$OUT_DIR/calls.jsonl" \
	-key "$KEY" -evidence-dir "$OUT_DIR/evidence" -ledger "$OUT_DIR/ledger.jsonl" -json >"$OUT_DIR/migrate.json" || rc=$?
echo "exit $rc"
esxi "$hostd_before" >"$OUT_DIR/hostd-window.log" <<'EOF'
tail -n +"$(($1 + 1))" /var/log/hostd.log
EOF
jq -c '{verdict, calls, blocked_calls, vms: [.vms[] | {vm, readiness, bundle_id, checks: [.checks[] | {name, verdict, seconds, detail}]}]}' "$OUT_DIR/migrate.json"
host_state "$OUT_DIR/host-after.txt"

section "read-only proof"
echo "Gaia's call log (method path op allowed status):"
jq -r '[.method, .path, (.op // "-"), .allowed, (.status // "-")] | @tsv' "$OUT_DIR/calls.jsonl" | sort | uniq -c
tasks=$(write_tasks "$OUT_DIR/hostd-window.log")
reads=$(read_tasks "$OUT_DIR/hostd-window.log")
gets=$(jq -s 'map(select(.method == "GET")) | length' "$OUT_DIR/calls.jsonl")
echo "hostd.log while the check ran: $(wc -l <"$OUT_DIR/hostd-window.log") lines; write tasks: $tasks; read tasks: $reads (GET /folder calls: $gets)"
grep 'Task Created' "$OUT_DIR/hostd-window.log" || true
if diff -u "$OUT_DIR/host-before.txt" "$OUT_DIR/host-after.txt"; then host_unchanged=true; else host_unchanged=false; fi
echo "fixture VMs (power state, folder listing) unchanged: $host_unchanged"
# Copy the downloads' sha256 next to the host listing: the bytes read are the bytes on the datastore.
for vm in "${VMS[@]}"; do
	jq -r --arg vm "$vm" '.[] | "\($vm) \(.name) \(.bytes) \(.sha256)"' "$WORK/$vm/downloads.json" 2>/dev/null || true
done

section "gaia evidence verify"
erc=0
"$BIN/gaia" evidence verify -pubkey "$OUT_DIR/evidence-pub.pem" -ledger "$OUT_DIR/ledger.jsonl" -evidence-dir "$OUT_DIR/evidence" -json >"$OUT_DIR/evidence-verify.json" || erc=$?
echo "exit $erc"
jq -c '{verdict, entries: .report.entries, bundles: [.report.bundles[] | {bundle_id, files, problems}], problems: .report.problems}' "$OUT_DIR/evidence-verify.json"

section "gate (CLAUDE.md T8)"
jq -n --slurpfile m "$OUT_DIR/migrate.json" --slurpfile ev "$OUT_DIR/evidence-verify.json" --slurpfile calls "$OUT_DIR/calls.jsonl" \
	--argjson guard_tests "$guard_tests" --argjson tasks "$tasks" --argjson reads "$reads" --argjson gets "$gets" \
	--argjson fixture_tasks "${fixture_tasks:-0}" --argjson host_unchanged "$host_unchanged" '
	($m[0].vms | map({key: .vm, value: .}) | from_entries) as $v
	| $calls as $c
	| {
	  a_linux_ready: {readiness: $v["gaia-t8-linux"].readiness, bundle: $v["gaia-t8-linux"].bundle_id, evidence_verify: $ev[0].verdict,
	                  pass: ($v["gaia-t8-linux"].readiness == "OK" and $ev[0].verdict == "OK")},
	  b_broken_not_ok: {readiness: $v["gaia-t8-broken"].readiness, bundle: $v["gaia-t8-broken"].bundle_id,
	                    pass: ($v["gaia-t8-broken"].readiness != null and $v["gaia-t8-broken"].readiness != "OK")},
	  c_zero_writes: {allowlist_tests_pass: $guard_tests, calls: ($c | length), blocked: ($c | map(select(.allowed | not)) | length),
	                  ops: ($c | map(.op // (.method + " /folder")) | unique),
	                  non_read: ($c | map(select((.method == "POST" and .path == "/sdk") or ((.method == "GET" or .method == "HEAD") and (.path | startswith("/folder/"))) | not)) | length),
	                  hostd_write_tasks_during_check: $tasks, hostd_read_tasks_during_check: $reads, get_folder_calls: $gets,
	                  hostd_write_tasks_by_fixture_positive_control: $fixture_tasks, fixture_vms_unchanged: $host_unchanged,
	                  pass: ($guard_tests and ($c | length) > 0 and ($c | all(.allowed)) and $tasks == 0 and $reads == $gets
	                         and $fixture_tasks > 0 and $host_unchanged)}
	} | . + {verdict: (if .a_linux_ready.pass and .b_broken_not_ok.pass and .c_zero_writes.pass then "OK"
	                   elif .a_linux_ready.readiness == null or .a_linux_ready.readiness == "UNKNOWN" then "UNKNOWN"
	                   else "CRITICAL" end)}' | tee "$OUT_DIR/gate.json"

section "result"
verdict=$(jq -r .verdict "$OUT_DIR/gate.json")
echo "$verdict"
echo "(gate.json, migrate.json, calls.jsonl, evidence/, ledger.jsonl in $OUT_DIR)"
case $verdict in
OK) exit 0 ;;
CRITICAL) exit 2 ;;
*) exit 3 ;;
esac
