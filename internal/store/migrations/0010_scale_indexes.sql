-- Schema version 10: what the T24 gate measured on a lab VM of the acceptance server's size (5,000 VMs, 3 M points,
-- 3.3 M jobs): the queued and running jobs were found by reading every job (the scheduler at every tick, the fleet
-- view at every request). A partial index holds only them; queries name its condition, state IN ('queued', 'running'),
-- so SQLite can use it.
CREATE INDEX jobs_open ON jobs (vm_id, kind) WHERE state IN ('queued', 'running');
-- The history strip's failed jobs of a VM.
CREATE INDEX jobs_vm_state ON jobs (vm_id, state, finished_at);
-- A VM's passing test is looked for from its present points (a few dozen), not from all its tests (hundreds).
CREATE INDEX restore_tests_point ON restore_tests (point_id, verdict);
