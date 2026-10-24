-- name: StartRun :exec
-- A run that starts right away, without the queue (gaia apply).
insert into runs (id, contracts, commit, plan_hash, status, requested_by, started_at)
values ($1, $2, $3, $4, 'running', $5, now());

-- name: QueueRun :exec
insert into runs (id, contracts, commit, plan_hash, status, requested_by)
values ($1, $2, $3, $4, 'queued', $5);

-- name: SetRunJob :exec
update runs set job_id = $2 where id = $1;

-- name: TakeRun :execrows
-- A worker takes a queued run.
update runs set status = 'running', started_at = now() where id = $1 and status = 'queued';

-- name: FinishRun :exec
update runs set status = $2, error = $3, finished_at = now() where id = $1;

-- name: Run :one
select * from runs where id = $1;

-- name: Runs :many
-- Newest first; contract "" lists every run.
select * from runs
where (sqlc.arg(contract)::text = '' or sqlc.arg(contract)::text = any (contracts))
order by queued_at desc, id desc
limit sqlc.arg(max_rows);

-- name: StartStage :exec
insert into run_stages (run_id, contract, stage, status)
values ($1, $2, $3, 'running')
on conflict (run_id, contract, stage) do update set status = 'running', detail = null, started_at = now(), finished_at = null;

-- name: FinishStage :exec
update run_stages set status = $4, detail = $5, finished_at = now()
where run_id = $1 and contract = $2 and stage = $3;

-- name: RunStages :many
select * from run_stages where run_id = $1 order by started_at, contract, stage;

-- name: CancelQueuedRun :execrows
update runs set status = 'cancelled', error = $2, finished_at = now() where id = $1 and status = 'queued';
