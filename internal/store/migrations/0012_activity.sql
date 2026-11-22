-- Schema version 12 (T27, the v2 Activity screen): jobs are listed by kind or by state, newest first, and the last
-- hour's are counted; the T24 catalog has 3.3 M jobs, so each of these reads an index, not the table. A job's row
-- shows its point or its restore test, which are found by their job.
CREATE INDEX jobs_kind ON jobs (kind, id);
CREATE INDEX jobs_state ON jobs (state, id);
CREATE INDEX jobs_finished ON jobs (finished_at) WHERE finished_at != '';
CREATE INDEX points_job ON points (job_id);
CREATE INDEX restore_tests_job ON restore_tests (job_id);
CREATE INDEX migration_checks_job ON migration_checks (job_id);
