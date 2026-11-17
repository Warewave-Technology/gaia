-- gaia:foreign-keys-off
-- Schema version 8: migration checks in the server (ADR-003, ADR-005, T21).

-- jobs gets the kind migration_check. SQLite cannot change a CHECK constraint in place, so the table is rebuilt with
-- the same columns and rows (SQLite's procedure for other table changes: the first line runs this migration with
-- foreign key enforcement off, and Migrate checks every foreign key before it commits; points and restore_tests
-- refer to jobs).
CREATE TABLE jobs_new (
	id          INTEGER PRIMARY KEY,
	kind        TEXT    NOT NULL CHECK (kind IN ('backup', 'restore_test', 'verify_repository', 'state_backup', 'migration_check')),
	vm_id       INTEGER REFERENCES vms (id),
	state       TEXT    NOT NULL CHECK (state IN ('queued', 'running', 'ok', 'failed', 'cancelled')),
	reason      TEXT    NOT NULL,                   -- why the job exists (schedule, user, missing checkpoint, ...)
	error       TEXT    NOT NULL DEFAULT '',
	created_at  TEXT    NOT NULL,
	started_at  TEXT    NOT NULL DEFAULT '',
	finished_at TEXT    NOT NULL DEFAULT '',
	checkpoint  TEXT    NOT NULL DEFAULT '',        -- set once the host has created it
	detail      TEXT    NOT NULL DEFAULT '{}'       -- JSON: what the job did
);
INSERT INTO jobs_new (id, kind, vm_id, state, reason, error, created_at, started_at, finished_at, checkpoint, detail)
	SELECT id, kind, vm_id, state, reason, error, created_at, started_at, finished_at, checkpoint, detail FROM jobs;
DROP TABLE jobs;
ALTER TABLE jobs_new RENAME TO jobs;

-- Where migration checks read VMs from: a vSphere endpoint (ESXi or vCenter), through read-only calls only
-- (ADR-003 §2). The password is a file under secrets/ (source-NAME.password), never in the database or an answer.
-- insecure: the endpoint's certificate is not verified (a lab ESXi with its own certificate). Each change is a row in
-- state_changes (entity migration_source).
CREATE TABLE migration_sources (
	id         INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL UNIQUE,
	kind       TEXT    NOT NULL CHECK (kind IN ('vsphere')),
	url        TEXT    NOT NULL,
	username   TEXT    NOT NULL,
	insecure   INTEGER NOT NULL DEFAULT 0,
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

-- One check of one VM of a source: it is queued with its job, and gets its readiness (OK, WARNING, CRITICAL,
-- UNKNOWN), the reason and the signed bundle when it ends. config is the request as made (template, checks, timeouts);
-- detail what the check did (steps, guest checks, timings, converter image, virtio-win, vSphere calls). Each change of
-- readiness is a row in state_changes (entity migration_check).
CREATE TABLE migration_checks (
	id          INTEGER PRIMARY KEY,
	source_id   INTEGER NOT NULL REFERENCES migration_sources (id),
	vm          TEXT    NOT NULL,                   -- the VM's name on the source
	job_id      INTEGER NOT NULL REFERENCES jobs (id),
	config      TEXT    NOT NULL DEFAULT '{}',
	readiness   TEXT    NOT NULL DEFAULT '',
	reason      TEXT    NOT NULL DEFAULT '',
	bundle_id   TEXT    NOT NULL DEFAULT '',
	firmware    TEXT    NOT NULL DEFAULT '',         -- the source VM's: bios or efi
	guest_id    TEXT    NOT NULL DEFAULT '',         -- the source's guest OS identifier
	detail      TEXT    NOT NULL DEFAULT '{}',
	created_at  TEXT    NOT NULL,
	finished_at TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX migration_checks_source ON migration_checks (source_id, id);

-- A migration check's bundle belongs to no protected VM: it names its check instead.
ALTER TABLE evidence_bundles ADD COLUMN migration_check_id INTEGER REFERENCES migration_checks (id);
