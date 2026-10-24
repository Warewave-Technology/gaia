-- Structured events of a run's stages (design 12.3): one row per
-- ansible-runner event, keyed by the host's UID, secrets masked. Kept 90
-- days; raw logs go to the archive.

-- +goose Up
create table run_events (
    run_id     text        not null references runs (id),
    seq        int         not null,
    stage      text        not null,
    host_uid   text,
    event      text        not null,
    task       text,
    changed    boolean     not null default false,
    failed     boolean     not null default false,
    stdout     text,
    data       jsonb,
    created_at timestamptz not null,
    primary key (run_id, seq)
);
create index run_events_host on run_events (host_uid, created_at);

-- What a run used, for reproducing it: the config repository's commit and
-- the execution environment.
alter table runs add column config_commit text, add column ee_image text;

-- +goose Down
alter table runs drop column config_commit, drop column ee_image;
drop table run_events;
