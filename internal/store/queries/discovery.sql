-- name: InsertDiscovery :exec
insert into discovery_reports (report) values ($1);

-- name: LatestDiscovery :one
select * from discovery_reports order by taken_at desc, id desc limit 1;

-- name: PruneDiscovery :exec
-- The last ten reports are kept.
delete from discovery_reports where id not in (select id from discovery_reports order by taken_at desc, id desc limit 10);

-- name: ForgetDriftScan :exec
delete from drift_scans where area = $1;
