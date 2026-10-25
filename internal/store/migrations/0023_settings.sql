-- Gaia's settings (design 17.2) and the audit log (17.4). A settings
-- version holds every settings object in full, as the YAML a repository
-- would hold; versions are never changed or removed. The audit log is
-- written once per action and never changed either.

-- +goose Up
create table settings_versions (
    version      bigint      primary key,
    author       text        not null,
    reason       text        not null,
    proposal     text,
    content_hash text        not null,
    created_at   timestamptz not null default now()
);

create table settings_objects (
    version bigint not null references settings_versions (version),
    path    text   not null,
    data    text   not null,
    primary key (version, path)
);

create table audit_log (
    id         bigint      generated always as identity primary key,
    at         timestamptz not null default now(),
    actor      text        not null,
    actor_id   text,
    session_id text,
    address    text,
    action     text        not null,
    object     text,
    result     text        not null default 'ok',
    detail     jsonb
);
create index audit_log_actor on audit_log (actor, at desc);
create index audit_log_action on audit_log (action, at desc);
create index audit_log_object on audit_log (object, at desc);

-- +goose StatementBegin
create function append_only() returns trigger language plpgsql as $$
begin
    raise exception '% is append-only', tg_table_name;
end
$$;
-- +goose StatementEnd

create trigger settings_versions_append_only before update or delete on settings_versions
    for each row execute function append_only();
create trigger settings_versions_no_truncate before truncate on settings_versions
    for each statement execute function append_only();
create trigger settings_objects_append_only before update or delete on settings_objects
    for each row execute function append_only();
create trigger settings_objects_no_truncate before truncate on settings_objects
    for each statement execute function append_only();
create trigger audit_log_append_only before update or delete on audit_log
    for each row execute function append_only();
create trigger audit_log_no_truncate before truncate on audit_log
    for each statement execute function append_only();

-- +goose Down
drop table audit_log;
drop table settings_objects;
drop table settings_versions;
drop function append_only;
