-- Schema version 15 (ADR-007 C17).
-- A host's pinned SSH key says when it was pinned, and a different key the host presented is kept, with when it was
-- first seen, until an admin checks and accepts it (host_key_changed; never accepted automatically). The time a key
-- was pinned before this version is the newest host_key state change that trusted it, else the host's updated_at.
ALTER TABLE hosts ADD COLUMN ssh_host_key_pinned_at TEXT NOT NULL DEFAULT '';
ALTER TABLE hosts ADD COLUMN presented_host_key TEXT NOT NULL DEFAULT '';     -- authorized_keys form; '' when none
ALTER TABLE hosts ADD COLUMN presented_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE hosts ADD COLUMN presented_at TEXT NOT NULL DEFAULT '';           -- when the host first presented it
UPDATE hosts SET ssh_host_key_pinned_at = COALESCE(
	(SELECT MAX(at) FROM state_changes WHERE entity = 'host_key' AND entity_id = hosts.id AND to_state = 'trusted'), updated_at)
	WHERE ssh_host_key != '';

-- The slot count the sandbox helper last reported, and when: the capacity is modelled from it while the helper does
-- not answer ("last known: 2 slots, at 14:02"), across restarts of the server. One row.
CREATE TABLE sandbox_last_known (
	id          INTEGER PRIMARY KEY CHECK (id = 1),
	slots       INTEGER NOT NULL CHECK (slots >= 0),
	reported_at TEXT    NOT NULL
);
