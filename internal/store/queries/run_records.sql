-- name: LockRunRecords :exec
lock table run_records in share row exclusive mode;

-- name: LastRunRecord :one
select * from run_records order by seq desc limit 1;

-- name: InsertRunRecord :one
insert into run_records (run_id, body, prev_hash, record_hash)
values ($1, $2, $3, $4)
returning *;

-- name: SetRunRecordArchived :exec
update run_records set archived = $2 where run_id = $1;

-- name: RunRecords :many
select * from run_records where seq > $1 order by seq limit $2;

-- name: RunRecord :one
select * from run_records where run_id = $1;
