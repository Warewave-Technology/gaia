-- Schema version 6: quarantine of damaged repository contents (ADR-007 B3) and login protection (T18).

-- Every repository content a verification found damaged and took out of deduplication. The repository records the same
-- list (a gaia-quarantine manifest), so Gaia's garbage collection and a rebuilt server know it too. Append-only.
CREATE TABLE quarantined_contents (
	id              INTEGER PRIMARY KEY,
	repository_id   INTEGER NOT NULL REFERENCES repositories (id),
	verification_id INTEGER NOT NULL REFERENCES verifications (id),
	content_id      TEXT    NOT NULL,
	pack            TEXT    NOT NULL,                -- the pack holding the damaged copy ('' = missing from the index)
	reason          TEXT    NOT NULL,                -- what is wrong with it
	quarantined_at  TEXT    NOT NULL,
	manifest        TEXT    NOT NULL,                -- the repository's gaia-quarantine manifest
	points          TEXT    NOT NULL DEFAULT '[]'    -- JSON: catalog point IDs that referenced it
);
CREATE INDEX quarantined_contents_repository ON quarantined_contents (repository_id, content_id);
CREATE TRIGGER quarantined_contents_no_update BEFORE UPDATE ON quarantined_contents BEGIN SELECT RAISE(ABORT, 'quarantined_contents is append-only'); END;
CREATE TRIGGER quarantined_contents_no_delete BEFORE DELETE ON quarantined_contents BEGIN SELECT RAISE(ABORT, 'quarantined_contents is append-only'); END;

-- Present points a verification did not read because they had failed already (they cannot fail twice).
ALTER TABLE verifications ADD COLUMN points_skipped INTEGER NOT NULL DEFAULT 0;

-- Why the next backup of a VM must be full ('' = no such demand). A quarantine sets it; the next full backup that passes
-- its post-backup check clears it. Each change is a row in state_changes (entity vm_full_backup).
ALTER TABLE vms ADD COLUMN full_backup_reason TEXT NOT NULL DEFAULT '';

-- Login protection: failed logins in a row since the last good one, and the end of the current lockout ('' = none).
-- Each lockout and unlock is a row in state_changes (entity user_lockout) and every failed login an audit entry.
ALTER TABLE users ADD COLUMN failed_logins INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN locked_until TEXT NOT NULL DEFAULT '';
