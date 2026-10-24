-- Operations (design 6.4): each runs once, by name. The spec hash is the
-- Operation as approved: a later commit that changes it is refused.

-- +goose Up
create table operations (
    name        text primary key,
    status      text        not null,     -- scheduled | running | succeeded | failed | partial | cancelled | expired
    spec_hash   text        not null,
    change_id   text        references changes (id),
    run_id      text        references runs (id),
    results     jsonb       not null default '[]',   -- per server: uid, hostname, batch, result, detail
    updated_at  timestamptz not null default now(),
    finished_at timestamptz
);

-- +goose Down
drop table operations;
