-- name: StartRun :exec
insert into runs (id, contracts, commit, plan_hash, status)
values ($1, $2, $3, $4, 'running');

-- name: FinishRun :exec
update runs set status = $2, error = $3, finished_at = now() where id = $1;

-- name: Run :one
select * from runs where id = $1;
