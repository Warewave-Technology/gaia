-- Drift (design 7.5): each difference between what was applied and what
-- is, once per server, area and key while it lasts.

-- +goose Up
create table drift_reports (
    id          bigserial primary key,
    contract    text        not null,
    uid         text        not null,
    hostname    text        not null,
    area        text        not null, -- registration | provisioning | configuration
    key         text        not null, -- what differs: zabbix/host, dns/a...
    want        text        not null,
    got         text        not null,
    status      text        not null default 'open', -- open | unknown | resolved | accepted
    first_seen  timestamptz not null default now(),
    last_seen   timestamptz not null default now(),
    resolved_at timestamptz,
    change_id   text
);
create unique index drift_live on drift_reports (uid, area, key) where status in ('open', 'unknown');
create index drift_contract on drift_reports (contract, status);

-- When each area was last checked, so that replicas share the work.
create table drift_scans (
    area    text primary key,
    last_at timestamptz not null
);

-- +goose Down
drop table drift_scans;
drop table drift_reports;
