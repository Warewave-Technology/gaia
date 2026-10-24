-- Runs are queued before they run (River jobs, design 12.4), and each run
-- reports its stages per contract (design 7.2).

-- +goose Up
alter table runs
    add column job_id       bigint,
    add column requested_by text,
    add column queued_at    timestamptz not null default now();
-- started_at becomes the time a worker took the run.
alter table runs alter column started_at drop not null, alter column started_at drop default;

create table run_stages (
    run_id      text        not null references runs (id),
    contract    text        not null,
    stage       text        not null,  -- ipam | provision | monitoring | configure | decommission | record
    status      text        not null,  -- running | succeeded | failed | cancelled
    detail      text,
    started_at  timestamptz not null default now(),
    finished_at timestamptz,
    primary key (run_id, contract, stage)
);

-- +goose Down
drop table run_stages;
update runs set started_at = queued_at where started_at is null;
alter table runs alter column started_at set default now(), alter column started_at set not null;
alter table runs drop column job_id, drop column requested_by, drop column queued_at;
