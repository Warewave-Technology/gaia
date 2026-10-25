-- Settings changes (design 17.3): a proposal to add, change or remove
-- settings objects, approved like a contract change. Changes maps each
-- settings path to its new YAML, or to null to remove it; the content
-- hash binds a decision to what was seen.

-- +goose Up
create table settings_proposals (
    id              text        primary key,
    base_version    bigint      not null,
    changes         jsonb       not null,
    content_hash    text        not null,
    title           text        not null,
    reason          text        not null default '',
    requested_by    text        not null,
    requested_by_id text,
    status          text        not null default 'pending'
                    check (status in ('pending', 'applied', 'rejected', 'stale', 'withdrawn')),
    critical        boolean     not null default false,
    why_critical    text[]      not null default '{}',
    impact          jsonb       not null default '[]',
    decided_by      text,
    decided_by_id   text,
    comment         text,
    applied_version bigint,
    stale_reason    text,
    created_at      timestamptz not null default now(),
    decided_at      timestamptz
);
create index settings_proposals_open on settings_proposals (created_at desc) where status = 'pending';

-- +goose Down
drop table settings_proposals;
