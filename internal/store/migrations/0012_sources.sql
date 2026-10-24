-- What each Source did last (design 5.4): the sync report.

-- +goose Up
create table source_reports (
    source     text primary key,            -- Kind/name
    status     text        not null,        -- unchanged | committed | refused | failed
    commit     text,                         -- Gaia's commit, when one was written
    detail     jsonb       not null default '{}',  -- added, changed, removed, problems
    error      text,
    ran_at     timestamptz not null default now()
);

-- +goose Down
drop table source_reports;
