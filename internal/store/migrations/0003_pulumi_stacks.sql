-- Pulumi keeps a stack's secrets settings (provider and the data key,
-- encrypted by OpenBao transit) next to the program, in a file that an
-- inline program's workspace loses between runs. Gaia keeps them here.

-- +goose Up
create table pulumi_stacks (
    name             text primary key,
    secrets_provider text        not null,
    encrypted_key    text        not null,
    updated_at       timestamptz not null default now()
);

-- +goose Down
drop table pulumi_stacks;
