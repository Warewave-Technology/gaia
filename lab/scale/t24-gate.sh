#!/usr/bin/env bash
# fixture: the T24 gate (CLAUDE.md T24, ADR-007 C1) on a clean lab VM of the acceptance server's size. Not part of
# the product. It needs no Go: the binaries are built on the workstation (make-binaries below) and copied with it.
#
#   (workstation)  lab/scale/t24-gate.sh make-binaries DIR    builds gaia, gen, bench and the simulation test for
#                                                             linux/amd64 into DIR, with this script and a manifest
#   (lab VM, root) DIR/t24-gate.sh run [OUT]                  the gate; OUT default /root/t24-<UTC>
#
# run: a server of its own (data /var/lib/gaia-t24, config /etc/gaia-t24, 127.0.0.1:18443, never the packaged gaia),
# the synthetic catalog (5,000 VMs, 100 hosts, 3,000,000 points), the server started on it (its first tick is the full
# recompute, timed), lab/scale/bench (200 requests per view endpoint, three more full recomputes, the status check,
# the server's memory), then the server stopped and TestScaleSimulation at full size (24 simulated hours, gate b).
# gate.json: a = every p95 within its limit, b = the simulation's checks all equal, c = every full recompute <= 30 s.
# Exit 0 OK, 2 CRITICAL, 3 UNKNOWN (could not measure).
# Env: VMS HOSTS POINTS HOURS N (defaults 5000 100 3000000 24 200).
set -euo pipefail

VMS=${VMS:-5000}
HOSTS=${HOSTS:-100}
POINTS=${POINTS:-3000000}
HOURS=${HOURS:-24}
N=${N:-200}

case "${1:-}" in
make-binaries)
	dir=${2:?DIR}
	root="$(cd "$(dirname "$0")/../.." && pwd)"
	mkdir -p "$dir"
	rev=$(git -C "$root" rev-parse --short=12 HEAD)
	dirty=$(git -C "$root" status --porcelain --untracked-files=no | grep -q . && echo "+dirty" || true)
	export GOOS=linux GOARCH=amd64 CGO_ENABLED=0
	go build -C "$root" -trimpath -ldflags "-X github.com/Warewave-Technology/gaia/internal/buildinfo.Version=t24-$rev$dirty" -o "$dir/gaia" ./cmd/gaia
	go build -C "$root" -trimpath -o "$dir/gen" ./lab/scale/gen
	go build -C "$root" -trimpath -o "$dir/bench" ./lab/scale/bench
	go test -C "$root" -c -trimpath -o "$dir/scale.test" ./internal/server
	install -m 0755 "$0" "$dir/t24-gate.sh"
	sum=sha256sum
	command -v sha256sum >/dev/null || sum="shasum -a 256"
	(cd "$dir" && $sum gaia gen bench scale.test t24-gate.sh) >"$dir/MANIFEST"
	printf '%s%s\n' "$rev" "$dirty" >"$dir/COMMIT"
	cat "$dir/MANIFEST" "$dir/COMMIT"
	exit 0
	;;
run) ;;
*)
	sed -n '2,17p' "$0"
	exit 3
	;;
esac

((EUID == 0)) || { echo "UNKNOWN: run as root"; exit 3; }
bin="$(cd "$(dirname "$0")" && pwd)"
OUT=${2:-/root/t24-$(date -u +%Y%m%dT%H%M%SZ)}
DATA=/var/lib/gaia-t24
CONF=/etc/gaia-t24
ADDR=127.0.0.1:18443
mkdir -p "$OUT"
exec > >(tee -a "$OUT/t24.log") 2>&1
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
command -v jq >/dev/null || apt-get -o DPkg::Lock::Timeout=300 install -y -q jq >/dev/null
for f in gaia gen bench scale.test; do [[ -x "$bin/$f" ]] || { echo "UNKNOWN: $bin/$f is missing (make-binaries)"; exit 3; }; done
(cd "$bin" && sha256sum -c --ignore-missing MANIFEST) >"$OUT/manifest-check.txt" || { echo "UNKNOWN: the binaries are not the manifest's"; exit 3; }
cp "$bin/COMMIT" "$OUT/commit.txt"

# The machine, stated with the result.
{
	echo "{"
	echo "  \"cpus\": $(nproc),"
	echo "  \"cpu_model\": \"$(sed -n 's/^model name[[:space:]]*: //p' /proc/cpuinfo | head -1)\","
	echo "  \"mem_total_mib\": $(($(sed -n 's/^MemTotal: *\([0-9]*\).*/\1/p' /proc/meminfo) / 1024)),"
	echo "  \"kernel\": \"$(uname -r)\","
	# shellcheck disable=SC1091 # the lab VM's own file
	echo "  \"os\": \"$(. /etc/os-release && echo "$PRETTY_NAME")\","
	echo "  \"root_fs\": \"$(df -h --output=source,size,avail / | tail -1 | tr -s ' ')\""
	echo "}"
} >"$OUT/machine.json"
log "machine: $(tr -d '\n' <"$OUT/machine.json")"
avail=$(df --output=avail -B1G /var/lib | tail -1 | tr -d ' ')
((avail >= 12)) || { echo "UNKNOWN: $avail GiB free on /var/lib, the gate needs 12 (two catalogs of ~3 GiB, their WAL)"; exit 3; }

stop_server() { [[ -n "${SPID:-}" ]] && kill "$SPID" 2>/dev/null && wait "$SPID" 2>/dev/null || true; SPID=; }
cleanup() {
	stop_server
	rm -rf "$DATA" "$CONF" /tmp/t24-sim-*
	fstrim -a 2>/dev/null || true
}
trap cleanup EXIT
rm -rf "$DATA" "$CONF"
umask 077
head -c 24 /dev/urandom | base64 >"$OUT/.pw"
head -c 24 /dev/urandom | base64 >"$OUT/.kit"

log "== server init and the synthetic catalog ($VMS VMs, $HOSTS hosts, $POINTS points)"
"$bin/gaia" server init -config "$CONF/server.json" -data-dir "$DATA" -hostname 127.0.0.1 -listen "$ADDR" \
	-password-file "$OUT/.pw" -kit-passphrase-file "$OUT/.kit" -json | jq '{version, tls_sha256}' >"$OUT/init.json"
"$bin/gen" -db "$DATA/gaia.db" -vms "$VMS" -hosts "$HOSTS" -points "$POINTS" -seed 1 >"$OUT/catalog.json"
du -b "$DATA/gaia.db" | awk '{print $1}' >"$OUT/db-bytes.txt"
log "catalog: $(jq -c '{vms, hosts, points, present_points, jobs, tests, state_changes, seconds}' "$OUT/catalog.json"), database $(numfmt --to=iec <"$OUT/db-bytes.txt")"
sync && echo 3 >/proc/sys/vm/drop_caches # the server starts with a cold page cache

log "== the server on the catalog: its start is a full recompute"
"$bin/gaia" server run -config "$CONF/server.json" >"$OUT/server.log" 2>&1 &
SPID=$!
for _ in $(seq 1 600); do
	grep -q 'status table recomputed' "$OUT/server.log" && break
	kill -0 "$SPID" 2>/dev/null || { echo "UNKNOWN: the server stopped:"; tail -20 "$OUT/server.log"; exit 3; }
	sleep 1
done
grep -m1 'status table recomputed' "$OUT/server.log" | tee "$OUT/start-recompute.txt" >&2 || { echo "UNKNOWN: no recompute in 10 minutes"; exit 3; }

log "== bench: $N requests per view endpoint"
"$bin/bench" -url "https://$ADDR" -cert "$DATA/tls/cert.pem" -password-file "$OUT/.pw" -n "$N" -pid "$SPID" >"$OUT/bench.json" || true
jq -e .verdict "$OUT/bench.json" >/dev/null || { echo "UNKNOWN: bench gave no result"; exit 3; }
jq -r '.endpoints[] | "\(.name) p50 \(.p50_ms) p95 \(.p95_ms) max \(.max_ms) limit \(.limit_p95_ms) \(.pass)"' "$OUT/bench.json" >&2
stop_server

log "== TestScaleSimulation: $HOURS simulated hours"
sim=$(mktemp -d /tmp/t24-sim-XXXXXX)
(cd "$sim" && TMPDIR="$sim" "$bin/scale.test" -test.run '^TestScaleSimulation$' -test.v -test.timeout 4h \
	-scale-vms "$VMS" -scale-hosts "$HOSTS" -scale-points "$POINTS" -scale-hours "$HOURS" -scale-out "$OUT/sim.json") >"$OUT/sim.log" 2>&1 || true
tail -5 "$OUT/sim.log" >&2
jq -e .Verdict "$OUT/sim.json" >/dev/null 2>&1 || { echo "UNKNOWN: the simulation gave no result"; exit 3; }

start_s=$(sed -n 's/.*reason=start .*seconds=\([0-9.]*\).*/\1/p' "$OUT/start-recompute.txt")
jq -n --slurpfile b "$OUT/bench.json" --slurpfile s "$OUT/sim.json" --slurpfile m "$OUT/machine.json" --slurpfile c "$OUT/catalog.json" \
	--argjson start "${start_s:-null}" --arg commit "$(cat "$OUT/commit.txt")" '
	($b[0].recompute | map(.seconds)) as $rb | ($s[0].FullRecomputes | map(.seconds)) as $rs |
	{commit: $commit, machine: $m[0], catalog: ($c[0] | {vms, hosts, points, present_points, jobs, tests, state_changes}),
	 a: {pass: ($b[0].endpoints | all(.pass)), worst_view_p95_ms: ([$b[0].endpoints[] | select(.limit_p95_ms == 300) | .p95_ms] | max),
	     worst_search_p95_ms: ([$b[0].endpoints[] | select(.limit_p95_ms == 150) | .p95_ms] | max), server_rss: $b[0].server_rss},
	 b: {pass: ($s[0].Verdict == "OK" and ($s[0].Checks | all((.differ | length) == 0 and .due == 0))), checks: ($s[0].Checks | length),
	     vms: $s[0].VMs, hours: $s[0].Hours, events: $s[0].Events, status_changes: $s[0].StatusChanges},
	 c: {pass: (([$start] + $rb + $rs | map(select(. != null)) | max) <= 30), start_s: $start, bench_s: $rb, simulation_max_s: ($rs | max)}}
	| . + {verdict: (if .a.pass and .b.pass and .c.pass then "OK" else "CRITICAL" end)}' | tee "$OUT/gate.json"
rm -f "$OUT/.pw" "$OUT/.kit"
log "out: $OUT"
[[ $(jq -r .verdict "$OUT/gate.json") == OK ]] || exit 2
