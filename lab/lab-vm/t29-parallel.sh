#!/usr/bin/env bash
# T29 acceptance, 4.2: how the restore tests of a t29-capacity.sh window ran side by side. Not part of the product: it
# only reads the files `t29-capacity.sh check DIR` saved (DIR/start.json, DIR/tests.json), so it runs again on the
# evidence copy.
#
#   lab/lab-vm/t29-parallel.sh DIR    per VM: tests, verdicts, proven RTO range; the peak number of tests running at
#                                     once; into DIR/parallel.json and the window's tests into DIR/tests-window.json
# Exit: 0 / 3 UNKNOWN (no check yet).
set -euo pipefail

DIR=${1:-}
[[ -n "$DIR" ]] || { sed -n '2,8p' "$0"; exit 3; }
[[ -f "$DIR/start.json" && -f "$DIR/tests.json" ]] || { echo "UNKNOWN: no $DIR/start.json or $DIR/tests.json (run t29-capacity.sh check first)"; exit 3; }

jq --slurpfile s "$DIR/start.json" '
	def ts: sub("\\.[0-9]+"; "") | sub("(?<z>[+-][0-9]{2}):(?<m>[0-9]{2})$"; "Z") | fromdateiso8601;
	($s[0].started_at | ts) as $t0 | ($t0 + 6 * 3600) as $t1
	| [.[] | select(.finished_at != "" and (.finished_at | ts) > $t0 and (.finished_at | ts) <= $t1)
	  | {id, job_id, vm_id, verdict, started_at, finished_at, proven_rto_s}] | sort_by(.started_at)' "$DIR/tests.json" >"$DIR/tests-window.json"

jq --slurpfile s "$DIR/start.json" '
	def ts: sub("\\.[0-9]+"; "") | sub("(?<z>[+-][0-9]{2}):(?<m>[0-9]{2})$"; "Z") | fromdateiso8601;
	{started_at: $s[0].started_at, slots: $s[0].slots, tests: length,
	 by_vm: (group_by(.vm_id) | map({vm_id: .[0].vm_id, tests: length, verdicts: (map(.verdict) | unique),
	                                rto_s: (map(.proven_rto_s // empty) | if length > 0 then {min: min, max: max} else null end)})),
	 peak_running: ([.[] | {t: (.started_at | ts), d: 1}, {t: (.finished_at | ts), d: -1}] | sort_by(.t, .d)
	                | reduce .[] as $e ({c: 0, m: 0}; .c += $e.d | .m = ([.m, .c] | max)) | .m)}' "$DIR/tests-window.json" |
	tee "$DIR/parallel.json"
