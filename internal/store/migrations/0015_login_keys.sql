-- The SSH keys people log in to servers with (design 14.4). They are in
-- Gaia's database, not in Git: usernames are personal data (14.2).

-- +goose Up
create table login_keys (
    fingerprint text primary key,
    user_id     text        not null references users (id) on delete cascade,
    name        text        not null,
    public_key  text        not null,
    created_at  timestamptz not null default now(),
    revoked_at  timestamptz
);
create index login_keys_user on login_keys (user_id);

-- +goose Down
drop table login_keys;
