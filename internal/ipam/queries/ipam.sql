-- name: UpsertPool :exec
insert into pools (cidr, gateway, range_start, range_end, name, labels, tags)
values (@cidr, sqlc.narg('gateway'), @range_start, @range_end, @name, @labels, @tags)
on conflict (cidr) do update
set gateway = excluded.gateway, range_start = excluded.range_start, range_end = excluded.range_end,
    name = excluded.name, labels = excluded.labels, tags = excluded.tags;

-- name: Pools :many
select * from pools order by cidr;

-- name: LockPool :one
select * from pools where cidr = @cidr for update;

-- name: LiveAllocationOf :one
select * from allocations where pool = @pool and owner = @owner and released_at is null;

-- name: UnavailableIPs :many
-- Live addresses and those still in quarantine.
select ip from allocations
where pool = @pool and (released_at is null or quarantine_until > now());

-- name: InsertAllocation :one
insert into allocations (pool, ip, owner, hostname)
values (@pool, @ip, @owner, @hostname)
returning *;

-- name: ReleaseOwner :many
update allocations
set released_at = now(), quarantine_until = now() + make_interval(secs => @quarantine_secs::float8)
where owner = @owner and released_at is null
returning *;

-- name: AllocationsOf :many
select * from allocations where owner = @owner and released_at is null order by pool;

-- name: ReassignOwner :many
-- A replaced server's addresses go to the server that replaces it
-- (design 9.5): same address, new owner.
update allocations set owner = @to_owner
where owner = @from_owner and released_at is null
returning *;
