# Reason codes (version 4)

Generated from `internal/status/codes.go` by `go test ./internal/status -run TestReasonCodesDoc -update`; do not edit.

Every reason that makes a VM's status what it is, and every note, carries one of these codes (ADR-007 C3). The API,
the command line's JSON, the notifications and the daily report carry the code, its scope and its parameters next to
the sentence; the sentence is written from them when it is read, so ages are current. A reason's **rule** is the row
of the status table (ADR-007 "The status model") it belongs to; several codes can share a row when the cause differs.
The **scope** is the cause a group of VMs shares (the overview's "Needs attention" groups by code and scope); a code
without a scope is a cause of the VM itself. Codes are part of the API contract: a change is a new version of this list.

| Code | Status | Rule | Scope | Parameters | Cause | Meaning |
|---|---|---|---|---|---|---|
| `no_backup_in_rpo` | CRITICAL | `theoretical_rpo_over_target` | - | `point`, `point_at`, `target_s` | No backup within the RPO target | the newest checked backup is older than the RPO target, and the newest backup job did not fail |
| `host_unreachable` | CRITICAL | `theoretical_rpo_over_target` | host | `point`, `point_at`, `target_s`, `job`, `job_at`, `detail` | Host does not answer | no backup within the RPO target because the newest backup job could not reach the host over SSH |
| `host_key_untrusted` | CRITICAL, UNKNOWN | `theoretical_rpo_over_target`, `no_checked_point` | host | `point`, `point_at`, `target_s`, `job`, `job_at`, `detail` | Host key not trusted yet | the host's SSH key was never trusted, so Gaia does not connect to it: no backup within the RPO target (CRITICAL), or no checked backup at all (UNKNOWN, rule `no_checked_point`) |
| `host_key_changed` | CRITICAL, UNKNOWN | `theoretical_rpo_over_target`, `no_checked_point` | host | `point`, `point_at`, `target_s`, `job`, `job_at`, `detail`, `pinned_at`, `fingerprint` | Host presents a different SSH key | the host presents an SSH host key other than the pinned one, so Gaia reads nothing from it until an admin checks and accepts the new key (never automatically): no backup within the RPO target (CRITICAL), or no checked backup at all (UNKNOWN, rule `no_checked_point`) |
| `backup_failing` | CRITICAL | `theoretical_rpo_over_target` | - | `point`, `point_at`, `target_s`, `job`, `job_at`, `detail` | Backups fail | no backup within the RPO target because the newest backup job failed (for a reason other than the host not answering or its SSH key) |
| `test_failed_boot` | CRITICAL | `latest_test_critical` | - | `test`, `point`, `test_at`, `check`, `detail` | Restore test failed to boot | the latest restore test with a verdict is CRITICAL because the restored guest did not boot |
| `guest_rebooted` | CRITICAL | `latest_test_critical` | - | `test`, `point`, `test_at`, `check`, `detail` | Restored guest restarts by itself | the latest restore test with a verdict is CRITICAL because the guest stopped answering inside the stability window (ADR-005 B) |
| `test_failed_check` | CRITICAL | `latest_test_critical` | - | `test`, `point`, `test_at`, `check`, `detail` | Restore test check failed | the latest restore test with a verdict is CRITICAL because a check after boot failed (the check is named) |
| `repo_content_damaged` | CRITICAL | `integrity_failed` | repository | `points` | Repository content damaged | present points failed their check (after the backup, or a repository verification found damaged contents) |
| `no_policy` | UNKNOWN | `no_policy` | - | - | No policy | the VM has no policy, so it has no targets |
| `never_backed_up` | UNKNOWN | `no_checked_point` | - | - | Never backed up | no present point passed its post-backup check |
| `never_proven` | UNKNOWN | `no_passing_test` | - | - | Never proven | no restore test of a present, undamaged point has passed |
| `no_proven_rto` | UNKNOWN | `no_proven_rto` | - | `test` | Passed without a proven RTO | the newest passing test has no proven RTO |
| `proven_rpo_over_target` | WARNING | `proven_rpo_over_target` | - | `test`, `point`, `point_at`, `target_s` | Proven point older than the RPO target | backups are within the RPO target, but the newest proven point is not |
| `rto_over_target` | WARNING | `proven_rto_over_target` | - | `test`, `rto_s`, `target_s` | Proven RTO over target | the newest passing test restored and booted the VM slower than the RTO target |
| `proof_too_old` | WARNING | `proof_too_old` | - | `test`, `test_at`, `target_s` | Proof older than its maximum age | the newest passing test is older than the policy's maximum proof age |
| `proven` | OK | `proven` | - | `test`, `point`, `point_at`, `test_at`, `rto_s`, `rpo_target_s`, `rto_target_s`, `max_proof_age_s` | Proven | the newest passing test meets every target |
| `no_rule` | UNKNOWN | `no_rule` | - | - | No rule applied | no row of the status table applied (cannot happen; never shown as healthy) |
| `test_warning` | note | `-` | - | `test`, `point`, `detail` | Restore test warned | note: the latest restore test with a verdict is WARNING |
| `full_backup` | note | `-` | - | `point`, `detail` | Full backup for a reason | note: the newest backup is full although the VM had an earlier one (a checkpoint gone, a disk resized, a quarantine) |
| `driver_install_loop` | CRITICAL | `-` (migration check) | - | `drivers`, `detail` | Converted Windows restarts for a driver | a migration check: the converted Windows restarts on every boot because virt-v2v's first-boot service cannot install a virtio-win driver (ADR-005 C); checked again with exclude_drivers it can be WARNING at best |
| `test_not_run` | note | `-` | sandbox | `test`, `point`, `detail` | Restore test could not run | note: the newest restore test could not run (the sandbox scope when the sandbox helper did not answer) |
