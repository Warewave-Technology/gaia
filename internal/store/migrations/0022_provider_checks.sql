-- The last check of each provider (design 11.5): whether Gaia reaches it
-- with its credentials, and when it last did.

-- +goose Up
create table provider_checks (
    provider   text        primary key,
    type       text        not null,
    status     text        not null check (status in ('ok', 'failed', 'skipped')),
    detail     text,
    error      text,
    latency_ms integer     not null default 0,
    checked_at timestamptz not null default now(),
    last_ok_at timestamptz
);

-- +goose Down
drop table provider_checks;
