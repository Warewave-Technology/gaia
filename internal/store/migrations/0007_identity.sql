-- People and their sessions (design 11.2-11.4, 12.3). Nothing here lets
-- anyone sign in by itself: tokens and recovery codes are kept as hashes,
-- passkeys as public keys, TOTP secrets encrypted by OpenBao transit.

-- +goose Up
create table users (
    id            text primary key,
    username      text        not null unique,
    display_name  text,
    provider      text        not null,       -- local, or an OIDC provider's name
    subject       text,                       -- OIDC sub
    password_hash text,                       -- local accounts with require: mfa
    enabled       boolean     not null default true,
    created_at    timestamptz not null default now(),
    disabled_at   timestamptz,
    unique (provider, subject)
);

-- Teams of a person: given to a local account, from an OIDC groups claim,
-- and from AD (roadmap phase 6). Roles and visibility come from them.
create table team_memberships (
    user_id   text        not null references users (id) on delete cascade,
    team      text        not null,
    source    text        not null,           -- local | oidc | ad
    synced_at timestamptz not null default now(),
    primary key (user_id, team, source)
);

create table webauthn_credentials (
    id           bytea       primary key,
    user_id      text        not null references users (id) on delete cascade,
    name         text        not null,
    credential   jsonb       not null,        -- the credential: public key, flags, AAGUID
    sign_count   bigint      not null default 0,
    created_at   timestamptz not null default now(),
    last_used_at timestamptz
);

create table totp_secrets (
    user_id      text primary key references users (id) on delete cascade,
    ciphertext   text        not null,
    confirmed_at timestamptz
);

create table recovery_codes (
    user_id   text not null references users (id) on delete cascade,
    code_hash text not null,
    used_at   timestamptz,
    primary key (user_id, code_hash)
);

create table sessions (
    id             text primary key,          -- shown to people; the token is not
    token_hash     text        not null unique,
    user_id        text        not null references users (id) on delete cascade,
    kind           text        not null,      -- ui | cli-refresh | cli-access | enroll
    parent         text        references sessions (id) on delete cascade,
    expires_at     timestamptz not null,
    idle_seconds   int,                       -- ui: ends after this long unused
    last_seen_at   timestamptz not null default now(),
    strong_auth_at timestamptz,               -- last passkey or MFA, for step-up
    ip             text,
    user_agent     text,
    created_at     timestamptz not null default now(),
    revoked_at     timestamptz
);
create index sessions_user on sessions (user_id) where revoked_at is null;

-- A ceremony in progress (a WebAuthn challenge, an OIDC state): any
-- replica can finish what another began.
create table auth_flows (
    id         text primary key,
    kind       text        not null,
    user_id    text,
    data       jsonb       not null,
    expires_at timestamptz not null
);

-- +goose Down
drop table auth_flows;
drop table sessions;
drop table recovery_codes;
drop table totp_secrets;
drop table webauthn_credentials;
drop table team_memberships;
drop table users;
