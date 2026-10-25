-- Discovery reports (design 8.3): what the hypervisors and monitoring
-- have, next to what Gaia records. They are not evidence and are not kept
-- long.

-- +goose Up
create table discovery_reports (
    id       bigserial primary key,
    taken_at timestamptz not null default now(),
    report   jsonb       not null
);

-- +goose Down
drop table discovery_reports;
