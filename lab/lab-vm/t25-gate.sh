#!/usr/bin/env bash
# fixture: the T25 gate on gaia-lab (CLAUDE.md T25 a-c; ADR-007 C4). Not part of the product. A server of its own
# (gaia-t25, packaged units, 127.0.0.1:8445) protects the fixture's VMs (lab/lab-vm/t25-guests.sh create) on gaia-lab
# over SSH, with a sandbox unit of its own whose slots the gate sets.
#
#   a  isolation: four tests at once in four slots; each guest's probe (serial.log in its bundle) must reach its own
#      slot's probe address and nothing else: not the other three slots' probe and guest addresses, not the host,
#      the LAN or the outside. Every pair.
#   b  throughput: the six plain Linux VMs' tests, all queued at once, with 1, 2 and 4 slots, three runs each: tests
#      per hour (first start to last finish), the median per slot count, and every test's verdict equal to the
#      serial run's.
#   c  a Windows guest among them: 4 slots and a memory limit that holds Windows (2048 MiB + overhead) and two Linux
#      guests, not three: Windows's proven RTO is reported, and a Linux test only waits while no slot or memory is
#      free (each admission after a wait comes when another test has just ended).
#
# Usage (on gaia-lab, as root): systemd-run --unit=gaia-t25 --collect -E HOME=/root lab/lab-vm/t25-gate.sh [OUT_DIR]
#                               lab/lab-vm/t25-gate.sh recompute OUT_DIR   a, c and gate.json again from a run's files and
#                                                                         the helper's journal
#                               lab/lab-vm/t25-gate.sh cleanup
# Exit 0 OK, 2 CRITICAL, 3 UNKNOWN.
# shellcheck disable=SC2016 # jq programs in single quotes
set -euo pipefail

OUT_DIR=${1:-/root/t25-$(date -u +%Y%m%dT%H%M%SZ)}
SRV_USER=gaia-t25
SRV_HOME=/var/lib/gaia-t25
SRV_BIN=/usr/local/sbin/gaia-t25
UNIT=gaia-t25-server
SB_UNIT=gaia-t25-sandbox
SB_HOME=/var/lib/gaia-t25-sandbox
SB_SOCK=/run/gaia-t25-sandbox/sandbox.sock
SB_WORK=/var/lib/libvirt/images/gaia-t25-sandbox
HOST_USER=gaia-t25-backup
API=https://127.0.0.1:8445
WORK=/root/t25w
ISO=(gaia-t25-iso-1 gaia-t25-iso-2 gaia-t25-iso-3 gaia-t25-iso-4)
LIN=(gaia-t25-lin-1 gaia-t25-lin-2 gaia-t25-lin-3 gaia-t25-lin-4 gaia-t25-lin-5 gaia-t25-lin-6)
WIN=gaia-t25-win
RUNS=${RUNS:-3}
TIMEOUT_S=${TIMEOUT_S:-3600}
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$repo_root/bin"
set -E
trap 'echo "UNKNOWN: harness failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR
((EUID == 0)) || { echo "run as root"; exit 3; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
section() { printf '\n== %s\n' "$*" >&2; }
api() { HOME=$WORK "$BIN/gaia" "$@" -json; }

cleanup() {
	for u in "$UNIT" "$SB_UNIT"; do systemctl stop "$u" 2>/dev/null || true; done
	rm -f "/run/systemd/system/$UNIT.service" "/run/systemd/system/$SB_UNIT.service"
	systemctl daemon-reload
	pkill -u "$HOST_USER" 2>/dev/null || true
	userdel -r "$HOST_USER" 2>/dev/null || true
	userdel "$SRV_USER" 2>/dev/null || true
	rm -rf "$SRV_HOME" "$SB_HOME" "$SB_WORK" "$SRV_BIN" "$WORK"
	for vm in "${ISO[@]}" "${LIN[@]}" "$WIN"; do
		for c in $(virsh checkpoint-list "$vm" --name 2>/dev/null); do virsh -q checkpoint-delete "$vm" "$c" 2>/dev/null || true; done
	done
	fstrim -a 2>/dev/null || true
}
if [[ "${1:-}" == cleanup ]]; then
	cleanup
	exit 0
fi


# --- analyses (also: t25-gate.sh recompute OUT_DIR) ---------------------------------------------------------------
analyze_a() {
python3 - "$OUT_DIR" <<'EOF' >"$OUT_DIR/a.json"
import json, re, sys
out = sys.argv[1]
bundles = json.load(open(f"{out}/a-bundles.json"))
tests = json.load(open(f"{out}/a-tests.json"))
probes = {}
for line in open(f"{out}/a-probes.txt"):
    b, rest = line.split(" ", 1)
    m = re.search(r"slot=(\d+) own=(\S+) target=(\S+) reached=(\w+) via=(\S+)", rest)
    if m:
        probes.setdefault(b, []).append({"target": m[3], "reached": m[4] == "yes", "via": m[5]})
own_ip = {}
for line in open(f"{out}/a-probes.txt"):
    b, rest = line.split(" ", 1)
    m = re.search(r"own=(\S+)", rest)
    if m:
        own_ip[b] = m[1]
for x in bundles:  # a guest booted by its serial console leases an address verify does not record: its probe says it
    x["guest_ip"] = x["guest_ip"] or own_ip.get(x["bundle"], "")
slots = {x["bundle"]: x for x in bundles}
rows, ok = [], True
for a in bundles:
    pa = {p["target"]: p for p in probes.get(a["bundle"], [])}
    own = f"10.254.{a['slot']}.1"
    row = {"slot": a["slot"], "guest_ip": a["guest_ip"], "isolated": a["isolated"], "netns_ip_forward": a["forwarding"],
           "own_probe_reached": pa.get(own, {}).get("reached", False), "pairs": [], "others_reached": []}
    for b in bundles:
        if b is a:
            continue
        for t in (f"10.254.{b['slot']}.1", b["guest_ip"]):
            p = pa.get(t)
            row["pairs"].append({"to_slot": b["slot"], "target": t, "probed": p is not None, "reached": bool(p and p["reached"])})
    for t, p in pa.items():
        if t != own and p["reached"]:
            row["others_reached"].append(t)
    row["pass"] = (row["isolated"] and row["netns_ip_forward"] == "0" and row["own_probe_reached"] and not row["others_reached"]
                   and all(x["probed"] and not x["reached"] for x in row["pairs"]))
    ok = ok and row["pass"]
    rows.append(row)
starts = sorted(t["test"]["started_at"] for t in tests)
print(json.dumps({"slots": sorted(r["slot"] for r in rows), "rows": rows, "test_starts": starts,
                  "pass": ok and len(rows) == 4 and len({r["slot"] for r in rows}) == 4}, indent=2))
EOF
}
analyze_c() { # LIMIT
	local limit=$1
python3 - "$OUT_DIR" "$limit" <<'EOF' >"$OUT_DIR/c.json"
import json, re, sys
from datetime import datetime
out, limit = sys.argv[1], int(sys.argv[2])
t = lambda s: datetime.fromisoformat(s.replace("Z", "+00:00"))
events = []
for line in open(f"{out}/c-helper.log"):
    kv = dict(re.findall(r'(\w+)=("[^"]*"|\S+)', line))
    kv = {k: v.strip('"') for k, v in kv.items()}
    events.append(kv)
tests = json.load(open(f"{out}/c-tests.json"))
win = next(x for x in tests if x["vm"].endswith("-win"))
ends = [t(e["time"]) for e in events if e.get("msg") == "verified"]
waits = []
for e in events:
    if e.get("msg") != "admitted":
        continue
    w = float(e.get("waited_s", 0))
    at = t(e["time"])
    # A wait is for admission only if a test ended just before this one was admitted.
    freed = w < 1 or any(0 <= (at - x).total_seconds() <= 3 for x in ends)
    waits.append({"vm": e["vm"], "slot": int(e["slot"]), "memory_mib": int(e["memory_mib"]), "waited_s": w, "admitted_when_freed": freed})
lin = [x for x in waits if not x["vm"].endswith("-win")]
logged = len(lin) == len([x for x in tests if not x["vm"].endswith("-win")]) and any(x["vm"].endswith("-win") for x in waits)
linux_ok = logged and all(x["admitted_when_freed"] for x in lin)
print(json.dumps({"memory_limit_mib": limit, "admission_log_complete": logged, "windows": {"verdict": win["test"]["verdict"], "proven_rto_s": win["test"]["proven_rto_s"],
                  "reason": win["test"]["reason"]}, "admissions": waits,
                  "verdicts": {x["vm"]: x["test"]["verdict"] for x in tests},
                  "pass": win["test"]["proven_rto_s"] is not None and linux_ok and all(x["test"]["verdict"] == "OK" for x in tests)}, indent=2))
EOF
}
gate_json() {
	jq -n --slurpfile a "$OUT_DIR/a.json" --slurpfile b "$OUT_DIR/b.json" --slurpfile c "$OUT_DIR/c.json" --arg commit "$(cat "$OUT_DIR/commit.txt")" \
	'{commit: $commit, a: $a[0].pass, b: $b[0].pass, c: $c[0].pass,
	  tests_per_hour: ($b[0].by_slots | map_values(.median_tests_per_hour)), windows_rto_s: $c[0].windows.proven_rto_s}
	 | . + {verdict: (if .a and .b and .c then "OK" else "CRITICAL" end)}' | tee "$OUT_DIR/gate.json"
}
# helper_log LIMIT: the sandbox helper's admissions and ends since it last started with that memory limit (phase c's
# start), whatever came before
helper_log() {
	journalctl -u "$SB_UNIT" --no-pager -o cat | awk -v m="memory_limit_mib=$1 " '/sandbox helper listening/ && index($0, m) {buf = ""; next} {buf = buf $0 "\n"} END {printf "%s", buf}' |
		grep -E 'msg=(admitted|verified)' >"$OUT_DIR/c-helper.log" || true
}
if [[ "${1:-}" == recompute ]]; then
	OUT_DIR=${2:?OUT_DIR}
	limit=$(jq -r .memory_limit_mib "$OUT_DIR/c.json")
	helper_log "$limit"
	analyze_a
	analyze_c "$limit"
	gate_json
	exit 0
fi
mkdir -p "$OUT_DIR"
exec > >(tee -a "$OUT_DIR/t25.log") 2>&1
for vm in "${ISO[@]}" "${LIN[@]}" "$WIN"; do
	[[ $(virsh domstate "$vm" 2>/dev/null) == running ]] || { echo "UNKNOWN: $vm is not running (lab/lab-vm/t25-guests.sh create)"; exit 3; }
done
avail=$(df --output=avail -B1G /var/lib/libvirt/images | tail -1 | tr -d ' ')
((avail >= 60)) || { echo "UNKNOWN: $avail GiB free, the gate needs 60"; exit 3; }
mem=$(awk '/MemAvailable/ {print int($2 / 1024)}' /proc/meminfo)
log "gaia-lab: $(nproc) CPUs, $(awk '/MemTotal/ {print int($2 / 1024)}' /proc/meminfo) MiB, $mem MiB available"
# The sandbox's guests at most: four Linux guests (4 x (384 + 256)) or phase c's limit (Windows and two Linux and a
# half: 3904), and 512 MiB to spare.
need=$(( (3904 > 4 * 640 ? 3904 : 4 * 640) + 512 ))
((mem >= need)) || { echo "UNKNOWN: $mem MiB available, the gate needs $need (the sandbox's guests)"; exit 3; }

declare -A VM_ID
# sandbox SLOTS MEMORY_MIB: (re)start the sandbox unit with that many slots and that memory limit
sandbox() {
	sed -e "s|^ExecStart=.*|ExecStart=$SRV_BIN sandbox serve -group $SRV_USER -socket $SB_SOCK -images $SB_HOME/images -work-dir $SB_WORK -bundle-dir $SB_HOME/bundles -slots $1 -memory-mib $2|" \
		"$repo_root/packaging/systemd/gaia-sandbox.service" >"/run/systemd/system/$SB_UNIT.service"
	systemctl daemon-reload
	systemctl restart "$SB_UNIT"
	for ((i = 0; i < 30; i++)); do [[ -S $SB_SOCK ]] && break; sleep 1; done
	sleep 12 # the server reads the helper's slots every 10 s
	curl -s --unix-socket "$SB_SOCK" http://sandbox/v1/capacity | jq -c '{slots, memory_limit_mib, basis}' >&2
}
# test_all VM...: queues a restore test of each at once and waits until all ended; prints [{vm, job, test}]
test_all() {
	local vm out ids=()
	for vm in "$@"; do
		out=$(api vms test "${VM_ID[$vm]}")
		ids+=("$(jq -r .id <<<"$out")")
	done
	local t0
	t0=$(date +%s)
	while (($(date +%s) - t0 < TIMEOUT_S)); do
		local open=0
		for j in "${ids[@]}"; do
			s=$(api jobs get "$j" | jq -r .state)
			[[ $s == queued || $s == running ]] && open=$((open + 1))
		done
		((open == 0)) && break
		sleep 10
	done
	local i=0
	for vm in "$@"; do
		jq -n --arg vm "$vm" --argjson job "${ids[$i]}" --argjson t "$(api tests list vm_id="${VM_ID[$vm]}" limit=1 | jq '.[0]')" \
			'{vm: $vm, job: $job, test: ($t | {id, verdict, started_at, finished_at, proven_rto_s, reason, bundle_id})}'
		i=$((i + 1))
	done | jq -s .
}

section "server (packaged units), host, policies, VMs"
cleanup
mkdir -p "$WORK"
owner=$(stat -c %U "$repo_root")
asowner() { if [[ "$owner" == root ]]; then "$@"; else runuser -u "$owner" -- env PATH="$PATH" HOME="$(getent passwd "$owner" | cut -d: -f6)" "$@"; fi; }
asowner make -C "$repo_root" -s build
git -C "$repo_root" rev-parse --short=12 HEAD >"$OUT_DIR/commit.txt"
useradd --system --home-dir "$SRV_HOME" --shell /usr/sbin/nologin "$SRV_USER"
install -d -m 0750 -o "$SRV_USER" -g "$SRV_USER" "$SRV_HOME" "$SRV_HOME/data" "$SRV_HOME/repo"
install -m 0755 "$BIN/gaia" "$SRV_BIN"
head -c 24 /dev/urandom | base64 | install -m 0600 /dev/stdin "$WORK/admin.pw"
head -c 24 /dev/urandom | base64 | install -m 0600 /dev/stdin "$WORK/kit.pass"
install -m 0600 -o "$SRV_USER" "$WORK/admin.pw" "$SRV_HOME/admin.pw"
install -m 0600 -o "$SRV_USER" "$WORK/kit.pass" "$SRV_HOME/kit.pass"
runuser -u "$SRV_USER" -- "$SRV_BIN" server init -config "$SRV_HOME/server.json" -data-dir "$SRV_HOME/data" \
	-listen 127.0.0.1:8445 -hostname 127.0.0.1 -admin admin -password-file "$SRV_HOME/admin.pw" -kit-passphrase-file "$SRV_HOME/kit.pass" -json >"$OUT_DIR/server-init.json"
jq --arg s "$SB_SOCK" --arg i "$SB_HOME/images" '. + {sandbox_socket: $s, sandbox_images_dir: $i}' "$SRV_HOME/server.json" >"$WORK/server.json"
install -m 0640 -o "$SRV_USER" -g "$SRV_USER" "$WORK/server.json" "$SRV_HOME/server.json"
sed -e "s|^User=.*|User=$SRV_USER|" -e "s|^Group=.*|Group=$SRV_USER|" \
	-e "s|^ExecStart=.*|ExecStart=$SRV_BIN server run -config $SRV_HOME/server.json|" \
	-e "s|^ReadWritePaths=.*|ReadWritePaths=$SRV_HOME -$SB_HOME/images|" \
	-e "s|gaia-sandbox.service gaia-convert.service|$SB_UNIT.service|" \
	"$repo_root/packaging/systemd/gaia.service" >"/run/systemd/system/$UNIT.service"
sandbox 4 6144
systemctl start "$UNIT"
for ((i = 0; i < 30; i++)); do curl -fsk "$API/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
HOME=$WORK "$BIN/gaia" login -server "$API" -user admin -fingerprint "$(jq -r .result.tls_certificate_sha256 "$OUT_DIR/server-init.json")" \
	-password-file "$WORK/admin.pw" >/dev/null
api system | jq -c '{version, schema_version}' | tee "$OUT_DIR/system.json" >&2
GAIA_KIT_PASSPHRASE=$(cat "$WORK/kit.pass") api repositories add name=main location="$SRV_HOME/repo/main" | jq -c '{id, name}' >&2
useradd -m -s /usr/sbin/nologin -G libvirt,libvirt-qemu "$HOST_USER"
install -d -m 700 -o "$HOST_USER" -g "$HOST_USER" "/home/$HOST_USER/.ssh"
api system | jq -r .ssh_authorized_keys_line | install -m 0600 -o "$HOST_USER" -g "$HOST_USER" /dev/stdin "/home/$HOST_USER/.ssh/authorized_keys"
host_fp=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $2 }')
host_id=$(api hosts add name=gaia-lab address=127.0.0.1 ssh_user="$HOST_USER" | jq .id)
[[ $(api hosts scan "$host_id" | jq -r .fingerprint) == "$host_fp" ]] || { echo "UNKNOWN: scanned fingerprint differs"; exit 3; }
api hosts trust "$host_id" fingerprint="$host_fp" >/dev/null
# A day between scheduled jobs: the first backup and test run at once, the gate's tests are queued by the gate.
pol() { api policies add name="$1" repository_id=1 backup_interval_s=86400 test_interval_s=86400 keep_last=4 keep_daily=0 keep_weekly=0 \
	rpo_target_s=172800 rto_target_s=1800 max_proof_age_s=172800 check_template="$2" | jq .id; }
lin_pol=$(pol t25-linux linux-boot)
win_pol=$(pol t25-windows windows-basic)
for vm in "${ISO[@]}" "${LIN[@]}" "$WIN"; do
	p=$lin_pol
	[[ $vm == "$WIN" ]] && p=$win_pol
	VM_ID[$vm]=$(api vms add host_id="$host_id" name="$vm" policy_id="$p" | jq .id)
done
log "waiting for every VM's first backup and test"
t0=$(date +%s)
while (($(date +%s) - t0 < TIMEOUT_S)); do
	open=$(api jobs list limit=200 | jq '[.[] | select(.state == "queued" or .state == "running")] | length')
	tested=$(for vm in "${!VM_ID[@]}"; do api tests list vm_id="${VM_ID[$vm]}" limit=1 | jq 'length'; done | awk '{n += $1} END {print n + 0}')
	((open == 0 && tested == ${#VM_ID[@]})) && break
	sleep 20
done
api jobs list limit=200 | jq -c '[.[] | select(.state == "failed") | {id, kind, vm_id, error}]' | tee "$OUT_DIR/setup-failed-jobs.json" >&2

section "a: isolation, four tests at once in four slots"
sandbox 4 6144
test_all "${ISO[@]}" >"$OUT_DIR/a-tests.json"
for b in $(jq -r '.[].test.bundle_id' "$OUT_DIR/a-tests.json"); do
	d="$SRV_HOME/data/evidence/bundles/$b"
	jq -c '{bundle: .bundle_id, slot: .sandbox.slot, guest_ip: .sandbox.guest_ip, isolated: .sandbox.isolated, forwarding: .sandbox.netns_ip_forward, verdict}' "$d/result.json"
	grep -a 'GAIA-ISOLATION' "$d/serial.log" | tr -d '\r' | sed "s/^/$b /" >>"$OUT_DIR/a-probes.txt" || true
done | jq -s . >"$OUT_DIR/a-bundles.json"
analyze_a
jq -c '{pass, slots, rows: [.rows[] | {slot, own_probe_reached, others_reached, pass}]}' "$OUT_DIR/a.json" >&2

section "b: throughput with 1, 2 and 4 slots, $RUNS runs each"
for n in 1 2 4; do
	sandbox "$n" 6144
	for r in $(seq 1 "$RUNS"); do
		test_all "${LIN[@]}" >"$OUT_DIR/b-$n-$r.json"
		jq -c --argjson n "$n" --argjson r "$r" '{slots: $n, run: $r,
			first_start: ([.[].test.started_at] | min), last_finish: ([.[].test.finished_at] | max),
			verdicts: (map({key: .vm, value: .test.verdict}) | from_entries)}' "$OUT_DIR/b-$n-$r.json" >&2
	done
done
python3 - "$OUT_DIR" "$RUNS" <<'EOF' >"$OUT_DIR/b.json"
import json, sys, statistics
from datetime import datetime
out, runs = sys.argv[1], int(sys.argv[2])
t = lambda s: datetime.fromisoformat(s.replace("Z", "+00:00"))
res, base, ok = {}, None, True
for n in (1, 2, 4):
    rs = []
    for r in range(1, runs + 1):
        x = json.load(open(f"{out}/b-{n}-{r}.json"))
        span = (max(t(i["test"]["finished_at"]) for i in x) - min(t(i["test"]["started_at"]) for i in x)).total_seconds()
        verdicts = {i["vm"]: i["test"]["verdict"] for i in x}
        rs.append({"run": r, "tests": len(x), "span_s": span, "tests_per_hour": len(x) / span * 3600, "verdicts": verdicts})
        if base is None:
            base = verdicts
        ok = ok and verdicts == base and all(v == "OK" for v in verdicts.values())
    res[str(n)] = {"runs": rs, "median_tests_per_hour": statistics.median(r["tests_per_hour"] for r in rs)}
print(json.dumps({"by_slots": res, "serial_verdicts": base, "verdicts_identical_to_serial": ok,
                  "speedup_2": res["2"]["median_tests_per_hour"] / res["1"]["median_tests_per_hour"],
                  "speedup_4": res["4"]["median_tests_per_hour"] / res["1"]["median_tests_per_hour"], "pass": ok}, indent=2))
EOF
jq -c '{pass, speedup_2, speedup_4, median: (.by_slots | map_values(.median_tests_per_hour))}' "$OUT_DIR/b.json" >&2

section "c: Windows among them, 4 slots, memory for Windows and two Linux guests"
win_mem=$(( 2048 + 256 ))
lin_mem=$(( 384 + 256 ))
limit=$(( win_mem + 2 * lin_mem + lin_mem / 2 )) # Windows and two Linux fit, Windows and three do not
sandbox 4 "$limit"
test_all "$WIN" "${LIN[@]}" >"$OUT_DIR/c-tests.json"
helper_log "$limit"
analyze_c "$limit"
jq -c '{pass, windows, linux_waits: [.admissions[] | select(.vm | endswith("-win") | not) | {vm, waited_s, admitted_when_freed}]}' "$OUT_DIR/c.json" >&2

gate_json
log "out: $OUT_DIR (the server stays until: lab/lab-vm/t25-gate.sh cleanup)"
[[ $(jq -r .verdict "$OUT_DIR/gate.json") == OK ]] || exit 2
