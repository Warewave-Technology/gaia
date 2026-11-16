-- Gaia's state, schema version 1 (ADR-007 §2). Times are RFC 3339 UTC text. Every state change of a job, point,
-- restore test or VM goes to state_changes; every user action goes to audit_log. Both are append-only.

CREATE TABLE users (
	id                  INTEGER PRIMARY KEY,
	username            TEXT    NOT NULL UNIQUE,
	role                TEXT    NOT NULL CHECK (role IN ('admin', 'operator', 'auditor')),
	password_hash       TEXT    NOT NULL,           -- argon2id, PHC string
	disabled            INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
	created_at          TEXT    NOT NULL,
	updated_at          TEXT    NOT NULL,
	password_changed_at TEXT    NOT NULL
);

CREATE TABLE sessions (
	id           INTEGER PRIMARY KEY,
	token_sha256 TEXT    NOT NULL UNIQUE,           -- the bearer token itself is never stored
	user_id      INTEGER NOT NULL REFERENCES users (id),
	created_at   TEXT    NOT NULL,
	expires_at   TEXT    NOT NULL,
	last_used_at TEXT    NOT NULL,
	remote_addr  TEXT    NOT NULL
);

-- One row per user action. sha256 = sha256(prev_sha256 || "\n" || the row's fields), so a row changed or removed
-- outside Gaia (the triggers below stop it inside) breaks the chain.
CREATE TABLE audit_log (
	seq         INTEGER PRIMARY KEY,
	at          TEXT    NOT NULL,
	user_id     INTEGER REFERENCES users (id),
	username    TEXT    NOT NULL,
	role        TEXT    NOT NULL,
	action      TEXT    NOT NULL,                   -- the API operation, e.g. "POST /api/v1/hosts"
	target      TEXT    NOT NULL,
	outcome     INTEGER NOT NULL,                   -- HTTP status
	remote_addr TEXT    NOT NULL,
	detail      TEXT    NOT NULL,
	prev_sha256 TEXT    NOT NULL,
	sha256      TEXT    NOT NULL
);
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;

CREATE TABLE state_changes (
	seq        INTEGER PRIMARY KEY,
	at         TEXT    NOT NULL,
	entity     TEXT    NOT NULL,                    -- user, job, point, restore_test, vm, ...
	entity_id  INTEGER NOT NULL,
	from_state TEXT    NOT NULL,                    -- "" for a new entity
	to_state   TEXT    NOT NULL,
	reason     TEXT    NOT NULL,
	actor      TEXT    NOT NULL                     -- a username, or "system"
);
CREATE INDEX state_changes_entity ON state_changes (entity, entity_id);
CREATE TRIGGER state_changes_no_update BEFORE UPDATE ON state_changes BEGIN SELECT RAISE(ABORT, 'state_changes is append-only'); END;
CREATE TRIGGER state_changes_no_delete BEFORE DELETE ON state_changes BEGIN SELECT RAISE(ABORT, 'state_changes is append-only'); END;

CREATE TABLE hosts (
	id           INTEGER PRIMARY KEY,
	name         TEXT    NOT NULL UNIQUE,
	address      TEXT    NOT NULL,                  -- DNS name or IP
	ssh_port     INTEGER NOT NULL DEFAULT 22 CHECK (ssh_port BETWEEN 1 AND 65535),
	ssh_user     TEXT    NOT NULL,
	ssh_host_key TEXT    NOT NULL DEFAULT '',       -- pinned on registration (T15)
	created_at   TEXT    NOT NULL,
	updated_at   TEXT    NOT NULL
);

CREATE TABLE repositories (
	id         INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL UNIQUE,
	kind       TEXT    NOT NULL CHECK (kind IN ('filesystem', 's3')),
	location   TEXT    NOT NULL,                    -- a path, or an S3 URL
	splitter   TEXT    NOT NULL,                    -- fixed at creation (ADR-004 A8)
	hash       TEXT    NOT NULL,
	created_at TEXT    NOT NULL
);

CREATE TABLE policies (
	id                INTEGER PRIMARY KEY,
	name              TEXT    NOT NULL UNIQUE,
	repository_id     INTEGER NOT NULL REFERENCES repositories (id),
	backup_interval_s INTEGER NOT NULL CHECK (backup_interval_s >= 60),
	test_interval_s   INTEGER NOT NULL CHECK (test_interval_s >= 60),
	keep_last         INTEGER NOT NULL CHECK (keep_last >= 1),
	keep_daily        INTEGER NOT NULL CHECK (keep_daily >= 0),
	keep_weekly       INTEGER NOT NULL CHECK (keep_weekly >= 0),
	rpo_target_s      INTEGER NOT NULL CHECK (rpo_target_s > 0),
	rto_target_s      INTEGER NOT NULL CHECK (rto_target_s > 0),
	max_proof_age_s   INTEGER NOT NULL CHECK (max_proof_age_s > 0),
	check_template    TEXT    NOT NULL,             -- a gaia verify template, e.g. linux-basic
	checks            TEXT    NOT NULL DEFAULT '[]',-- JSON: extra checks on top of the template
	created_at        TEXT    NOT NULL,
	updated_at        TEXT    NOT NULL
);

CREATE TABLE vms (
	id         INTEGER PRIMARY KEY,
	host_id    INTEGER NOT NULL REFERENCES hosts (id),
	name       TEXT    NOT NULL,                    -- the libvirt domain name
	uuid       TEXT    NOT NULL DEFAULT '',
	policy_id  INTEGER REFERENCES policies (id),
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL,
	UNIQUE (host_id, name)
);

CREATE TABLE disks (
	id         INTEGER PRIMARY KEY,
	vm_id      INTEGER NOT NULL REFERENCES vms (id) ON DELETE CASCADE,
	target     TEXT    NOT NULL,                    -- e.g. vda
	size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL,
	UNIQUE (vm_id, target)
);

CREATE TABLE jobs (
	id          INTEGER PRIMARY KEY,
	kind        TEXT    NOT NULL CHECK (kind IN ('backup', 'restore_test', 'verify_repository', 'state_backup')),
	vm_id       INTEGER REFERENCES vms (id),
	state       TEXT    NOT NULL CHECK (state IN ('queued', 'running', 'ok', 'failed', 'cancelled')),
	reason      TEXT    NOT NULL,                   -- why the job exists (schedule, user, missing checkpoint, ...)
	error       TEXT    NOT NULL DEFAULT '',
	created_at  TEXT    NOT NULL,
	started_at  TEXT    NOT NULL DEFAULT '',
	finished_at TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE points (
	id            INTEGER PRIMARY KEY,
	vm_id         INTEGER NOT NULL REFERENCES vms (id),
	job_id        INTEGER NOT NULL REFERENCES jobs (id),
	repository_id INTEGER NOT NULL REFERENCES repositories (id),
	kind          TEXT    NOT NULL CHECK (kind IN ('full', 'incremental')),
	taken_at      TEXT    NOT NULL,
	check_state   TEXT    NOT NULL CHECK (check_state IN ('unchecked', 'ok', 'failed')),
	bytes         INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE restore_tests (
	id          INTEGER PRIMARY KEY,
	vm_id       INTEGER NOT NULL REFERENCES vms (id),
	point_id    INTEGER NOT NULL REFERENCES points (id),
	job_id      INTEGER NOT NULL REFERENCES jobs (id),
	verdict     TEXT    NOT NULL CHECK (verdict IN ('OK', 'WARNING', 'CRITICAL', 'UNKNOWN')),
	proven_rto_s REAL,
	started_at  TEXT    NOT NULL,
	finished_at TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE evidence_bundles (
	id            INTEGER PRIMARY KEY,
	bundle_id     TEXT    NOT NULL UNIQUE,
	test_id       INTEGER REFERENCES restore_tests (id),
	vm_id         INTEGER REFERENCES vms (id),
	ledger_seq    INTEGER NOT NULL,
	bundle_sha256 TEXT    NOT NULL,
	verdict       TEXT    NOT NULL,
	created_at    TEXT    NOT NULL
);
