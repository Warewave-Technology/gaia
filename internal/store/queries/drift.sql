-- name: SeeDrift :one
-- A difference seen now: a new report, or the live one seen again. new
-- is true when it was not seen before.
insert into drift_reports (contract, uid, hostname, area, key, want, got, status)
values (@contract, @uid, @hostname, @area, @key, @want, @got, @status)
on conflict (uid, area, key) where status in ('open', 'unknown')
do update set want = excluded.want, got = excluded.got, hostname = excluded.hostname, last_seen = now()
returning id, (xmax = 0)::bool as new;

-- name: ResolveDrift :many
-- The live reports of a contract's area that were not seen in a scan
-- (seen: their ids) are resolved.
update drift_reports set status = 'resolved', resolved_at = now()
where contract = @contract and area = @area and status in ('open', 'unknown')
  and not (id = any (@seen::bigint[]))
returning *;

-- name: LiveDrift :many
select * from drift_reports
where (sqlc.narg(contract)::text is null or contract = sqlc.narg(contract)) and status in ('open', 'unknown')
order by contract, hostname, area, key;

-- name: SetDriftChange :exec
update drift_reports set change_id = @change_id
where contract = @contract and area = @area and status in ('open', 'unknown');

-- name: DriftScanned :one
select last_at from drift_scans where area = $1;

-- name: SetDriftScanned :exec
insert into drift_scans (area, last_at) values ($1, $2)
on conflict (area) do update set last_at = excluded.last_at;
