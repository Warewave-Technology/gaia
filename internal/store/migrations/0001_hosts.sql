-- Servers Gaia manages, their IDs in other systems, the names they hold
-- and the runs that changed them (design 12.3).

-- +goose Up
create type host_status as enum ('provisioning', 'active', 'draining', 'stopped', 'decommissioning', 'deleted');

create table hosts (
    uid         text primary key,
    contract    text        not null,
    hostname    text        not null,
    site        text        not null,
    seq         int         not null check (seq > 0),
    status      host_status not null,
    created_run text,
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now(),
    deleted_at  timestamptz
);
-- A sequence number is used once per contract and site among live hosts.
create unique index hosts_live_seq on hosts (contract, site, seq) where status <> 'deleted';

-- IDs of a host in other systems: the VM in Proxmox, the address in IPAM.
-- Records are found by these, never by name (design 9.1).
create table host_external_ids (
    uid         text not null references hosts (uid),
    provider    text not null,
    kind        text not null,
    external_id text not null,
    primary key (uid, provider, kind),
    unique (provider, kind, external_id)
);

-- A hostname is held by one live host at a time; it can be reused after
-- it is released (design 9.2).
create table name_leases (
    name        text        not null,
    uid         text        not null references hosts (uid),
    held_since  timestamptz not null default now(),
    released_at timestamptz
);
create unique index name_leases_held on name_leases (name) where released_at is null;

create table runs (
    id          text primary key,
    contracts   text[]      not null,
    commit      text        not null,
    plan_hash   text        not null,
    status      text        not null,
    error       text,
    started_at  timestamptz not null default now(),
    finished_at timestamptz
);

-- +goose Down
drop table runs;
drop table name_leases;
drop table host_external_ids;
drop table hosts;
drop type host_status;
