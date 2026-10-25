-- Servers' SSH host keys, recorded when Gaia first reaches them; from
-- then on Ansible checks them.

-- +goose Up
create table host_keys (
    uid         text        not null references hosts (uid),
    key_type    text        not null,
    public_key  text        not null,   -- authorized_keys form
    recorded_at timestamptz not null default now(),
    primary key (uid, key_type)
);

-- +goose Down
drop table host_keys;
