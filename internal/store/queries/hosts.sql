-- name: LiveHosts :many
select * from hosts
where contract = $1 and status not in ('deleted', 'unmanaged')
order by site, seq;

-- name: Host :one
select * from hosts where uid = $1;

-- name: InsertHost :exec
insert into hosts (uid, contract, hostname, site, network, seq, status, created_run)
values ($1, $2, $3, $4, $5, $6, $7, $8);

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

-- name: ExternalIDsOfKind :many
-- IDs a provider has given out, for live hosts: a VM ID in use by Gaia.
select e.external_id from host_external_ids e join hosts h on h.uid = e.uid
where e.provider = $1 and e.kind = $2 and h.status <> 'deleted';

-- name: DeleteExternalIDs :exec
-- A deleted host's IDs go: Proxmox and IPAM give them out again. The run
-- record keeps what they were.
delete from host_external_ids where uid = $1;

-- name: ContractSummaries :many
-- Every contract Gaia has hosts or runs for, with its live hosts and its
-- last run.
select c.name::text as name,
       (select count(*) from hosts h where h.contract = c.name and h.status not in ('deleted', 'unmanaged'))::int as live_hosts,
       coalesce((select r.id from runs r where c.name = any (r.contracts) order by r.queued_at desc, r.id desc limit 1), '')::text as last_run,
       coalesce((select r.status from runs r where c.name = any (r.contracts) order by r.queued_at desc, r.id desc limit 1), '')::text as last_status
from (select contract as name from hosts union select unnest(contracts) from runs union select contract from contract_states) c
order by c.name;

-- name: HostKeys :many
select * from host_keys where uid = $1 order by key_type;

-- name: SetHostKey :exec
insert into host_keys (uid, key_type, public_key) values ($1, $2, $3)
on conflict (uid, key_type) do update set public_key = excluded.public_key, recorded_at = now();

-- name: DeleteHostKeys :exec
delete from host_keys where uid = $1;

-- name: DeleteExternalID :exec
-- One ID goes when Gaia removed what it named (a bastion target).
delete from host_external_ids where uid = $1 and provider = $2 and kind = $3;

-- name: SetVMSpec :exec
-- The VM as the last run applied it, for a server that stops.
update hosts set vm_spec = $2 where uid = $1;

-- name: StopHost :exec
-- A server stopped to be deleted after its grace period.
update hosts set status = 'stopped', delete_after = $2, updated_at = now() where uid = $1;

-- name: RestoreHost :exec
-- A stopped server wanted again.
update hosts set status = 'active', delete_after = null, updated_at = now() where uid = $1;

-- name: DueContracts :many
-- Contracts with a stopped server whose grace period is over.
select distinct contract from hosts
where status = 'stopped' and delete_after <= sqlc.arg(now)::timestamptz
order by contract;
