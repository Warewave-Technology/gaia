-- name: Operation :one
select * from operations where name = $1;

-- name: InsertOperation :exec
insert into operations (name, status, spec_hash, change_id, run_id) values ($1, $2, $3, $4, $5);

-- name: SetOperationStatus :execrows
update operations set status = $2, results = coalesce(sqlc.narg(results)::jsonb, results), updated_at = now(),
    finished_at = case when $2 in ('succeeded', 'failed', 'partial', 'cancelled', 'expired') then now() else finished_at end
where name = $1 and status = any (sqlc.arg(from_statuses)::text[]);

-- name: Operations :many
select * from operations order by updated_at desc limit sqlc.arg(max_rows);
