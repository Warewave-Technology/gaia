#!/usr/bin/env bash
# T29 acceptance, "proof capacity with 1 slot and with 4 slots matches what happens over 6 hours" (CLAUDE.md T29,
# docs/t29-acceptance.md). Not part of the product: it only reads, with the workstation's gaia command and its login.
#
#   lab/lab-vm/t29-capacity.sh start DIR    the capacity view now (slots, demand, coverage, the proofs it says will
#                                           expire), into DIR/start.json
#   lab/lab-vm/t29-capacity.sh check DIR    6 hours later: what happened in the 6 hours after start, next to what the view
#                                           said, into DIR/check.json
#
# What is compared:
#   tests     the restore tests that finished in the 6 hours, against the view's simulation: tests_in_horizon is for
#             its 24-hour horizon, so its share of 6 hours is a quarter of it. They match within 20 % (or 2 tests, for
#             a handful of VMs).
#   expiries  the VMs whose proof the view said would expire within the 6 hours before their next test finishes,
#             against the VMs whose proof did expire then (no passing test finished before its proof_expires_at). The
#             two sets are the same.
# Exit: 0 OK / 2 CRITICAL (no match) / 3 UNKNOWN (not 6 hours yet, or nothing to read).
set -euo pipefail

WINDOW_S=$((6 * 3600))
GAIA=${GAIA:-gaia}
cmd=${1:-}
DIR=${2:-}
[[ -n "$cmd" && -n "$DIR" ]] || { sed -n '2,17p' "$0"; exit 3; }
mkdir -p "$DIR"

case "$cmd" in
start)
	"$GAIA" api GET '/api/v1/views/capacity?limit=500' >"$DIR/capacity.json"
	"$GAIA" api GET '/api/v1/views/vm-list?code=proof_too_old&limit=200' | jq '[.rows[].vm] | sort' >"$DIR/too-old-start.json"
	jq --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --argjson w "$WINDOW_S" '
		{started_at: $now, slots, slots_source, vms, coverage, slots_needed, tests_in_horizon, horizon_s,
		 tests_expected_in_window: (.tests_in_horizon * $w / .horizon_s),
		 expiring_in_window: [.expiring[] | select((.proof_expires_at | sub("\\.[0-9]+"; "") | fromdateiso8601) <= (($now | fromdateiso8601) + $w))
		                      | {vm_id, vm, proof_expires_at}],
		 expiring_total, by_os_class: [.by_os_class[]? | {os_class: (.os_class // .name), slots_needed}]}' "$DIR/capacity.json" |
		tee "$DIR/start.json"
	echo "check at $(date -u -r $(($(date +%s) + WINDOW_S)) +%H:%MZ 2>/dev/null || date -u -d "+6 hours" +%H:%MZ): $0 check $DIR" >&2
	;;
check)
	[[ -f "$DIR/start.json" ]] || { echo "UNKNOWN: no $DIR/start.json (run start first)"; exit 3; }
	start=$(jq -r '.started_at | fromdateiso8601' "$DIR/start.json")
	now=$(date +%s)
	((now >= start + WINDOW_S)) || { echo "UNKNOWN: only $(((now - start) / 60)) of 360 minutes have passed"; exit 3; }
	"$GAIA" api GET '/api/v1/restore-tests?limit=1000' >"$DIR/tests.json"
	"$GAIA" api GET '/api/v1/views/capacity?limit=500' >"$DIR/capacity-end.json"
	"$GAIA" api GET '/api/v1/views/vm-list?code=proof_too_old&limit=200' | jq '[.rows[].vm] | sort' >"$DIR/too-old-end.json"
	jq -n --slurpfile s "$DIR/start.json" --slurpfile o0 "$DIR/too-old-start.json" --slurpfile o1 "$DIR/too-old-end.json" --slurpfile t "$DIR/tests.json" --slurpfile e "$DIR/capacity-end.json" --argjson w "$WINDOW_S" '
		def ts: if . == null or . == "" then null else sub("\\.[0-9]+"; "") | sub("(?<z>[+-][0-9]{2}):(?<m>[0-9]{2})$"; "Z") | fromdateiso8601 end;
		$s[0] as $s | ($s.started_at | ts) as $t0 | ($t0 + $w) as $t1
		| [$t[0][] | select(.finished_at != "" and (.finished_at | ts) > $t0 and (.finished_at | ts) <= $t1)] as $done
		| {started_at: $s.started_at, slots: $s.slots, slots_at_end: $e[0].slots, coverage: $s.coverage,
		   tests: {expected: ($s.tests_expected_in_window * 10 | round / 10), finished: ($done | length),
		           by_verdict: ($done | group_by(.verdict) | map({(.[0].verdict): length}) | add // {})},
		   expiries: {
		     said: [$s.expiring_in_window[].vm] | sort,
		     happened: ([$s.expiring_in_window[] as $x | $x
		                | select([$done[] | select(.vm_id == $x.vm_id and .verdict != "CRITICAL" and .verdict != "UNKNOWN"
		                                           and (.finished_at | ts) <= ($x.proof_expires_at | ts))] | length == 0) | .vm]
		                + ($o1[0] - $o0[0]) | unique),
		     proof_too_old_at_start: $o0[0], proof_too_old_at_end: $o1[0]}}
		| .tests.match = ((.tests.finished - .tests.expected) | fabs) <= ([.tests.expected * 0.2, 2] | max)
		| .expiries.match = (.expiries.said == .expiries.happened)
		| . + {verdict: (if .tests.match and .expiries.match and .slots_at_end == .slots then "OK" else "CRITICAL" end)}' | tee "$DIR/check.json"
	[[ $(jq -r .verdict "$DIR/check.json") == OK ]] || exit 2
	;;
*)
	sed -n '2,17p' "$0"
	exit 3
	;;
esac
