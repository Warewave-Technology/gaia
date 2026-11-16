-- Schema version 2: Gaia's daily backup of its own state (ADR-007 §2), and when users last logged in.

CREATE TABLE state_backups (
	id            INTEGER PRIMARY KEY,
	repository_id INTEGER NOT NULL REFERENCES repositories (id),
	state         TEXT    NOT NULL CHECK (state IN ('running', 'ok', 'failed')),
	reason        TEXT    NOT NULL,                 -- "schedule" or the user who asked
	started_at    TEXT    NOT NULL,
	finished_at   TEXT    NOT NULL DEFAULT '',
	error         TEXT    NOT NULL DEFAULT '',
	snapshot_id   TEXT    NOT NULL DEFAULT '',      -- the Kopia manifest ID
	bytes         INTEGER NOT NULL DEFAULT 0,
	sha256        TEXT    NOT NULL DEFAULT ''       -- of the archive that was stored
);

ALTER TABLE users ADD COLUMN last_login_at TEXT NOT NULL DEFAULT '';
