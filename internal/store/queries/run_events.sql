-- name: InsertRunEvent :exec
insert into run_events (run_id, seq, stage, host_uid, event, task, changed, failed, stdout, data, created_at)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: RunEvents :many
select * from run_events where run_id = $1 order by seq;

-- name: SetRunConfiguration :exec
update runs set config_commit = $2, ee_image = $3 where id = $1;

-- name: RunEventsAfter :many
select * from run_events where run_id = $1 and seq > $2 order by seq limit $3;

-- name: PruneRunEvents :execrows
-- Events live 90 days in the database (design 12.4); the archive keeps them.
delete from run_events where created_at < $1;
