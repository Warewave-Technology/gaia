-- Schema version 5: the recovery kit (ADR-007 B5) and rebuilds of a server from it (T17).

-- Every recovery kit the server wrote. Only the newest matters; it is "confirmed" once a copy that opens with the
-- passphrase was checked against it (gaia kit save / confirm). Every change is also a row in state_changes.
CREATE TABLE recovery_kits (
	generation   INTEGER PRIMARY KEY,                -- 1 at gaia server init, +1 for each new kit
	created_at   TEXT    NOT NULL,
	created_by   TEXT    NOT NULL,
	reason       TEXT    NOT NULL,
	sha256       TEXT    NOT NULL,                   -- of the kit file
	repositories TEXT    NOT NULL DEFAULT '[]',      -- JSON: names of the repositories it opens
	confirmed_at TEXT    NOT NULL DEFAULT '',
	confirmed_by TEXT    NOT NULL DEFAULT ''
);

-- Each rebuild of this installation by gaia server restore-state.
CREATE TABLE state_restores (
	id                   INTEGER PRIMARY KEY,
	restored_at          TEXT    NOT NULL,
	tag                  TEXT    NOT NULL UNIQUE,    -- added to names made on hosts from now on (checkpoints)
	state_backup         TEXT    NOT NULL,           -- the archive restored (repository manifest ID)
	state_backup_created TEXT    NOT NULL,
	kit_generation       INTEGER NOT NULL,
	detail               TEXT    NOT NULL DEFAULT '{}' -- JSON: what was restored and what was not
);
