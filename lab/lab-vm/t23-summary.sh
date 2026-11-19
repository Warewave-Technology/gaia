#!/usr/bin/env bash
# T23 acceptance numbers, from the files lab/lab-vm/t23-collect.sh wrote: how long the schedules ran, every VM status
# change with its reason, the failed jobs, the restore tests per VM (count, verdicts, mean duration of a passing
# one), the notifications by event and state, the gaps in the schedule (the server down), and the newest daily report's rebuild.
#
#   t23-summary.sh DIR     prints one JSON document
set -euo pipefail
d=${1:?the directory t23-collect.sh wrote}
# shellcheck disable=SC2016 # jq programs in single quotes
jq -n --slurpfile jobs "$d/jobs.json" --slurpfile tests "$d/tests.json" --slurpfile vms "$d/vms.json" \
	--slurpfile notes "$d/notifications.json" --slurpfile reports "$d/daily-reports.json" --slurpfile sys "$d/system.json" \
	--slurpfile vs "$d/vms-status.json" --slurpfile sc <(jq -s . "$d/state-changes.jsonl") '
	def t: sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601;
	def name($id): ($vms[0][] | select(.id == $id) | .name) // ($id | tostring);
	($jobs[0] | map(select(.kind == "backup" and (.reason | startswith("schedule")))) | sort_by(.created_at)) as $sched
	| {
	  version: $sys[0].version,
	  first_job: ($jobs[0] | map(.created_at) | min),
	  newest_job: ($jobs[0] | map(.created_at) | max),
	  schedule_hours: ((($jobs[0] | map(.created_at | t) | max) - ($jobs[0] | map(.created_at | t) | min)) / 3600 * 100 | floor / 100),
	  # scheduled backups of any VM, one after another: with 3 VMs every 30 minutes, a gap over 20 minutes is the server down
	  schedule_gaps_over_20m: [range(1; $sched | length) as $i
	      | {from: $sched[$i-1].created_at[0:19], to: $sched[$i].created_at[0:19], min: ((($sched[$i].created_at | t) - ($sched[$i-1].created_at | t)) / 60 | floor)}
	      | select(.min > 20)],
	  status_changes: [$sc[0][] | select(.entity == "vm_status") | {at: .at[0:19], vm: name(.entity_id), from, to, reason}],
	  failed_jobs: [$jobs[0][] | select(.state == "failed") | {id, kind, vm: name(.vm_id), created_at: .created_at[0:19], error}],
	  tests: [$tests[0] | group_by(.vm_id)[] | {vm: name(.[0].vm_id), count: length,
	      verdicts: (group_by(.verdict) | map({key: .[0].verdict, value: length}) | from_entries),
	      mean_passing_s: (map(select(.verdict == "OK" and .finished_at != "") | (.finished_at | t) - (.started_at | t)) | (add / length) | floor)}],
	  notifications: ($notes[0] | group_by(.event) | map({key: .[0].event, value: (group_by(.state) | map({key: .[0].state, value: length}) | from_entries)}) | from_entries),
	  newest_daily_report: ($reports[0][0] | {id, generated_at, reason, head_seq, last_rebuild: .report.last_rebuild}),
	  vms_now: [$vs[0][] | {vm, status, reason: .reasons[0].text, notes}]
	}'
