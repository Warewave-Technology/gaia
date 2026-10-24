-- IPAM v0: pools and the addresses allocated from them. IPAM is its own
-- service with its own database (design 12.1).

-- +goose Up
create table pools (
    cidr        cidr primary key,
    gateway     inet,
    range_start inet not null,
    range_end   inet not null,
    check (range_start <= range_end),
    check (range_start << cidr and range_end << cidr)
);

create table allocations (
    id               bigserial primary key,
    pool             cidr        not null references pools (cidr),
    ip               inet        not null,
    owner            text        not null, -- the host's UID
    hostname         text        not null,
    allocated_at     timestamptz not null default now(),
    released_at      timestamptz,
    -- A released address is not given out again before this (design 9.8).
    quarantine_until timestamptz
);
create unique index allocations_live_ip on allocations (ip) where released_at is null;
create unique index allocations_live_owner on allocations (pool, owner) where released_at is null;

-- +goose Down
drop table allocations;
drop table pools;
