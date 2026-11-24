-- Schema version 17 (T29, 0.2.1): a point's disks are read by point. The test scheduler and the proof capacity read the
-- disk size of every VM's newest checked point (a restore reserves it in the sandbox images directory, ADR-007 C13);
-- without this index each read scans every point's disks (3,000,000 points in the T24 catalog).
CREATE INDEX point_disks_point ON point_disks (point_id);
