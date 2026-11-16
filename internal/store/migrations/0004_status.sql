-- Schema version 4: scheduled restore tests and the status model (T16, ADR-007 "The status model").

ALTER TABLE points ADD COLUMN machine TEXT NOT NULL DEFAULT '{}';        -- JSON: the VM's virtual hardware at backup time
ALTER TABLE restore_tests ADD COLUMN reason TEXT NOT NULL DEFAULT '';    -- why the verdict: what failed, or what passed
ALTER TABLE restore_tests ADD COLUMN bundle_id TEXT NOT NULL DEFAULT ''; -- the sealed evidence bundle
ALTER TABLE restore_tests ADD COLUMN detail TEXT NOT NULL DEFAULT '{}';  -- JSON: restore, checks and timings
CREATE INDEX restore_tests_vm ON restore_tests (vm_id, id);

-- The status last computed for each VM. Every change is also a row in state_changes (entity 'vm_status').
CREATE TABLE vm_status (
	vm_id       INTEGER PRIMARY KEY REFERENCES vms (id) ON DELETE CASCADE,
	status      TEXT    NOT NULL CHECK (status IN ('OK', 'WARNING', 'CRITICAL', 'UNKNOWN')),
	reason      TEXT    NOT NULL,                  -- the reason that decided the status
	result      TEXT    NOT NULL DEFAULT '{}',     -- JSON: the full computation (figures, every reason)
	changed_at  TEXT    NOT NULL,                  -- when the status last changed
	computed_at TEXT    NOT NULL
);
