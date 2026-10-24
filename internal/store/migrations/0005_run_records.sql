-- The immutable record of each run (design 10.1, 12.3). A record is
-- written once, when the run ends; each carries the hash of the one
-- before it, so a deleted or changed record breaks the chain. The body is
-- kept as the exact text that was hashed.

-- +goose Up
create table run_records (
    seq         bigint generated always as identity primary key,
    run_id      text        not null unique references runs (id),
    body        text        not null,
    prev_hash   text        not null,
    record_hash text        not null unique,
    archived    text,
    created_at  timestamptz not null default now()
);

-- +goose StatementBegin
create function run_records_immutable() returns trigger language plpgsql as $$
begin
    -- Only the archive location may be filled in, once.
    if tg_op = 'UPDATE' and old.archived is null and new.archived is not null
       and (new.seq, new.run_id, new.body, new.prev_hash, new.record_hash, new.created_at)
           is not distinct from (old.seq, old.run_id, old.body, old.prev_hash, old.record_hash, old.created_at) then
        return new;
    end if;
    raise exception 'run_records are immutable';
end
$$;
-- +goose StatementEnd

create trigger run_records_immutable before update or delete on run_records
    for each row execute function run_records_immutable();
create trigger run_records_no_truncate before truncate on run_records
    for each statement execute function run_records_immutable();

-- +goose Down
drop table run_records;
drop function run_records_immutable;
