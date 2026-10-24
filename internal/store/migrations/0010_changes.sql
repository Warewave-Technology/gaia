-- The change engine (design 7.6): what each contract last had applied,
-- the changes Git brings, the requirements they wait for, and approvals.

-- +goose Up
create table contract_states (
    contract          text primary key,
    sync              text        not null default 'unknown',  -- synced | out-of-sync | unknown
    applied_snapshot  jsonb,
    applied_label     text,
    applied_plan_hash text,
    applied_at        timestamptz,
    error             text,          -- why the contract could not be planned
    evaluated_at      timestamptz not null default now()
);

create table changes (
    id           text primary key,
    contract     text        not null,
    source       text        not null default 'commit',    -- commit | revert | drift | retry
    snapshot     jsonb       not null,
    label        text        not null,
    plan_hash    text        not null,
    plan         jsonb       not null,
    status       text        not null,                      -- pending | approved | running | applied | failed | cancelled | rejected | superseded
    requested_by text[]      not null default '{}',
    commits      jsonb       not null default '[]',          -- the commits it brings and who signed each
    run_id       text        references runs (id),
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),
    finished_at  timestamptz
);
-- One open change per contract.
create unique index changes_open on changes (contract) where status in ('pending', 'approved', 'running');
create index changes_contract on changes (contract, created_at desc);

create table change_gates (
    change_id text not null references changes (id),
    gate      text not null,                -- validation | signature | approval | lock
    status    text not null,                -- ok | waiting | failed
    detail    jsonb not null default '{}',
    primary key (change_id, gate)
);

-- Approvals and rejections are never changed or removed: they are the
-- evidence of who allowed what (design 10.1).
create table approvals (
    id         bigint generated always as identity primary key,
    change_id  text        not null references changes (id),
    plan_hash  text        not null,
    user_id    text        not null references users (id),
    username   text        not null,
    decision   text        not null,       -- approve | reject
    comment    text,
    created_at timestamptz not null default now()
);

-- +goose Down
drop table approvals;
drop table change_gates;
drop table changes;
drop table contract_states;
