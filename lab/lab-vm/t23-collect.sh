#!/usr/bin/env bash
# T23 acceptance evidence: what the acceptance server recorded, read through the API with the workstation's gaia
# command (logged in, any role that reads; channels need an admin). Read-only. Not part of the product.
#
#   t23-collect.sh OUT [GAIA]   writes OUT/*.json and OUT/state-changes.jsonl (every page); GAIA is the gaia command
#                               (default: gaia on PATH)
#
# Then lab/lab-vm/t23-summary.sh OUT computes the report's numbers from those files.
set -euo pipefail
out=${1:?OUT directory}
g=${2:-gaia}
mkdir -p "$out"
api() { "$g" api GET "$1"; }

# every page of a list the API caps at 1000 (oldest first, paged with after=<last seq or id>)
pages() { # PATH KEY: prints one JSON value per line
	local path=$1 key=$2 after=0 page n
	while :; do
		page=$(api "$path?limit=1000&after=$after")
		n=$(jq length <<<"$page")
		((n > 0)) || break
		jq -c '.[]' <<<"$page"
		after=$(jq ".[-1].$key" <<<"$page")
		((n == 1000)) || break
	done
}

"$g" system -json >"$out/system.json"
"$g" vms list -json >"$out/vms.json"
jq -r '.[].id' "$out/vms.json" | while read -r id; do "$g" vms status "$id" -json; done | jq -s . >"$out/vms-status.json"
jq -r '.[].id' "$out/vms.json" | while read -r id; do "$g" points list -json vm_id="$id" limit=1000; done | jq -s 'add' >"$out/points.json"
"$g" jobs list -json limit=1000 >"$out/jobs.json"
"$g" tests list -json limit=1000 >"$out/tests.json"
"$g" verifications list -json >"$out/verifications.json"
"$g" quarantine list -json >"$out/quarantine.json"
api '/api/v1/notifications?limit=1000' >"$out/notifications.json"
api '/api/v1/notification-channels' | jq '[.[] | {id, name, kind, enabled, events, created_at}]' >"$out/channels.json"
api '/api/v1/daily-reports?limit=10' >"$out/daily-reports.json"
pages /api/v1/state-changes seq >"$out/state-changes.jsonl"
for f in "$out"/*.json; do jq -e . "$f" >/dev/null || { echo "UNKNOWN: $f is not JSON" >&2; exit 3; }; done
echo "OK: $(wc -l <"$out/state-changes.jsonl" | tr -d ' ') state changes, $(jq length "$out/jobs.json") jobs, $(jq length "$out/tests.json") tests in $out"
