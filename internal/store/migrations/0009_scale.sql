-- Schema version 9: the control plane at scale (T24, ADR-007 C1, C3). vm_status becomes the status model's cache:
-- what the views filter, group and sort by, and the inputs the time-driven figures are read from. The status model
-- (internal/status) stays the only place the truth table lives; GET /api/v1/status/check compares the two.

ALTER TABLE vm_status ADD COLUMN rank INTEGER NOT NULL DEFAULT 1;      -- sort order: CRITICAL 0, UNKNOWN 1, WARNING 2, OK 3
ALTER TABLE vm_status ADD COLUMN since REAL;                           -- changed_at, in unix seconds
ALTER TABLE vm_status ADD COLUMN code TEXT NOT NULL DEFAULT '';        -- the deciding reason's code (docs/reason-codes.md)
ALTER TABLE vm_status ADD COLUMN scope_kind TEXT NOT NULL DEFAULT '';  -- its scope: host, repository, sandbox; '' = the VM's own
ALTER TABLE vm_status ADD COLUMN scope_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vm_status ADD COLUMN scope_name TEXT NOT NULL DEFAULT '';
ALTER TABLE vm_status ADD COLUMN newest_point_id INTEGER;              -- the newest checked point
ALTER TABLE vm_status ADD COLUMN newest_taken_at REAL;                 -- unix seconds
ALTER TABLE vm_status ADD COLUMN proven_point_id INTEGER;              -- the point of the newest passing test
ALTER TABLE vm_status ADD COLUMN proven_taken_at REAL;
ALTER TABLE vm_status ADD COLUMN proven_test_id INTEGER;
ALTER TABLE vm_status ADD COLUMN proven_finished_at REAL;
ALTER TABLE vm_status ADD COLUMN proven_rto_s REAL;
ALTER TABLE vm_status ADD COLUMN rpo_target_s INTEGER;                 -- the policy's targets when computed
ALTER TABLE vm_status ADD COLUMN rto_target_s INTEGER;
ALTER TABLE vm_status ADD COLUMN max_proof_age_s INTEGER;
ALTER TABLE vm_status ADD COLUMN next_change_at REAL;                  -- when time alone changes it (unix); NULL: never
CREATE INDEX vm_status_rank ON vm_status (rank, proven_finished_at);
CREATE INDEX vm_status_code ON vm_status (code, scope_kind, scope_id);
CREATE INDEX vm_status_next ON vm_status (next_change_at);

-- Free-form tags for grouping: "key" or "key:value" (T24).
CREATE TABLE vm_tags (
	vm_id INTEGER NOT NULL REFERENCES vms (id) ON DELETE CASCADE,
	key   TEXT    NOT NULL,
	value TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (vm_id, key, value)
);
CREATE INDEX vm_tags_key ON vm_tags (key, value);

-- What the status facts and the scheduler read per VM, at thousands of VMs.
CREATE INDEX jobs_vm_kind ON jobs (vm_id, kind, id);
CREATE INDEX points_vm_checked ON points (vm_id, state, check_state, taken_at);
CREATE INDEX restore_tests_vm_verdict ON restore_tests (vm_id, verdict, id);
CREATE INDEX points_vm_taken ON points (vm_id, taken_at);
CREATE INDEX restore_tests_vm_finished ON restore_tests (vm_id, finished_at);

-- A VM status change records the deciding reason's code with its sentence, so a notification or a reader later
-- knows the cause as it was (ADR-007 C3); '' for every other change.
ALTER TABLE state_changes ADD COLUMN code TEXT NOT NULL DEFAULT '';
