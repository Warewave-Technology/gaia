-- name: LiveHosts :many
select * from hosts
where contract = $1 and status <> 'deleted'
order by site, seq;

-- name: Host :one
select * from hosts where uid = $1;

-- name: InsertHost :exec
insert into hosts (uid, contract, hostname, site, seq, status, created_run)
values ($1, $2, $3, $4, $5, $6, $7);

-- name: SetHostStatus :exec
update hosts
set status = $2,
    updated_at = now(),
    deleted_at = case when $2 = 'deleted'::host_status then now() else deleted_at end
where uid = $1;

-- name: HoldName :exec
insert into name_leases (name, uid) values ($1, $2);

-- name: ReleaseNames :exec
update name_leases set released_at = now()
where uid = $1 and released_at is null;

-- name: NameHolder :one
select uid from name_leases where name = $1 and released_at is null;

-- name: SetExternalID :exec
insert into host_external_ids (uid, provider, kind, external_id)
values ($1, $2, $3, $4)
on conflict (uid, provider, kind) do update set external_id = excluded.external_id;

-- name: ExternalIDs :many
select * from host_external_ids where uid = $1 order by provider, kind;

-- name: HostByExternalID :one
select uid from host_external_ids where provider = $1 and kind = $2 and external_id = $3;
