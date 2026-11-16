-- Schema version 3: backups of real VMs (T15). A point is one backup of a VM: one checkpoint on the host, one
-- diskstore point per disk. Retention prunes points (their rows stay, as history); repository verifications name
-- the points they found damaged.

ALTER TABLE points ADD COLUMN state TEXT NOT NULL DEFAULT 'present' CHECK (state IN ('present', 'pruned'));
ALTER TABLE points ADD COLUMN checkpoint TEXT NOT NULL DEFAULT '';   -- the libvirt checkpoint on the host
ALTER TABLE points ADD COLUMN parent_id INTEGER REFERENCES points (id); -- NULL for a full backup
ALTER TABLE points ADD COLUMN reason TEXT NOT NULL DEFAULT '';       -- why full, or "incremental from point N"
CREATE INDEX points_vm ON points (vm_id, id);

ALTER TABLE jobs ADD COLUMN checkpoint TEXT NOT NULL DEFAULT '';     -- set once the host has created it
ALTER TABLE jobs ADD COLUMN detail TEXT NOT NULL DEFAULT '{}';       -- JSON: what the job did

CREATE TABLE point_disks (
	id            INTEGER PRIMARY KEY,
	point_id      INTEGER NOT NULL REFERENCES points (id),
	target        TEXT    NOT NULL,
	size_bytes    INTEGER NOT NULL CHECK (size_bytes >= 0),
	block_size    INTEGER NOT NULL CHECK (block_size > 0),
	store_id      TEXT    NOT NULL,                  -- the diskstore (Kopia manifest) ID of the disk's point
	changed_bytes INTEGER NOT NULL,                  -- dirty bitmap (incremental) or allocated extents (full)
	read_bytes    INTEGER NOT NULL,                  -- read from the host over NBD
	written_bytes INTEGER NOT NULL,                  -- block bytes stored (before compression)
	UNIQUE (point_id, target)
);

CREATE TABLE verifications (
	id             INTEGER PRIMARY KEY,
	repository_id  INTEGER NOT NULL REFERENCES repositories (id),
	mode           TEXT    NOT NULL CHECK (mode IN ('sampled', 'full')),
	state          TEXT    NOT NULL CHECK (state IN ('running', 'ok', 'critical', 'failed')),
	reason         TEXT    NOT NULL,
	started_at     TEXT    NOT NULL,
	finished_at    TEXT    NOT NULL DEFAULT '',
	points_checked INTEGER NOT NULL DEFAULT 0,
	blocks_read    INTEGER NOT NULL DEFAULT 0,
	bytes_read     INTEGER NOT NULL DEFAULT 0,
	affected       TEXT    NOT NULL DEFAULT '[]',    -- JSON: [{point_id, disk, problems}]
	error          TEXT    NOT NULL DEFAULT ''
);
