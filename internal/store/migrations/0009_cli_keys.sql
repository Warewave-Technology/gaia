-- Device sign-in for machines without a browser (RFC 8628, design 11.3),
-- and the keys people sign commits with (design 7.6).

-- +goose Up
create table device_codes (
    code_hash   text primary key,
    user_code   text        not null unique,
    status      text        not null default 'pending',  -- pending | approved | denied | used
    user_id     text        references users (id) on delete cascade,
    ip          text,
    user_agent  text,
    created_at  timestamptz not null default now(),
    expires_at  timestamptz not null,
    polled_at   timestamptz
);

create table signing_keys (
    fingerprint text primary key,
    user_id     text        not null references users (id) on delete cascade,
    kind        text        not null,      -- ssh | gpg
    name        text        not null,
    public_key  text        not null,
    created_at  timestamptz not null default now(),
    revoked_at  timestamptz
);
create index signing_keys_user on signing_keys (user_id);

-- +goose Down
drop table signing_keys;
drop table device_codes;
