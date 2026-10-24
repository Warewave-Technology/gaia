-- name: InsertUser :exec
insert into users (id, username, display_name, provider, subject, password_hash)
values ($1, $2, $3, $4, $5, $6);

-- name: User :one
select * from users where id = $1;

-- name: UserByName :one
select * from users where username = $1;

-- name: UserBySubject :one
select * from users where provider = $1 and subject = $2;

-- name: Users :many
select * from users order by username;

-- name: SetUserEnabled :exec
-- By an admin: the directory does not open an account an admin closed.
update users set enabled = $2, disabled_at = case when $2 then null else now() end,
    disabled_by = case when $2 then null else 'admin' end where id = $1;

-- name: SetPasswordHash :exec
update users set password_hash = $2 where id = $1;

-- name: Teams :many
select distinct team from team_memberships where user_id = $1 order by team;

-- name: AddTeam :exec
insert into team_memberships (user_id, team, source) values ($1, $2, $3)
on conflict (user_id, team, source, role) do update set synced_at = now();

-- name: ClearTeams :exec
delete from team_memberships where user_id = $1 and source = $2;

-- name: InsertCredential :exec
insert into webauthn_credentials (id, user_id, name, credential, sign_count) values ($1, $2, $3, $4, $5);

-- name: Credentials :many
select * from webauthn_credentials where user_id = $1 order by created_at;

-- name: UseCredential :exec
update webauthn_credentials set credential = $2, sign_count = $3, last_used_at = now() where id = $1;

-- name: DeleteCredential :execrows
delete from webauthn_credentials where id = $1 and user_id = $2;

-- name: InsertRecoveryCode :exec
insert into recovery_codes (user_id, code_hash) values ($1, $2);

-- name: UseRecoveryCode :execrows
update recovery_codes set used_at = now() where user_id = $1 and code_hash = $2 and used_at is null;

-- name: DeleteRecoveryCodes :exec
delete from recovery_codes where user_id = $1;

-- name: UnusedRecoveryCodes :one
select count(*) from recovery_codes where user_id = $1 and used_at is null;

-- name: SetTOTP :exec
insert into totp_secrets (user_id, ciphertext) values ($1, $2)
on conflict (user_id) do update set ciphertext = excluded.ciphertext, confirmed_at = null;

-- name: TOTP :one
select * from totp_secrets where user_id = $1;

-- name: ConfirmTOTP :exec
update totp_secrets set confirmed_at = now() where user_id = $1;

-- name: InsertSession :exec
insert into sessions (id, token_hash, user_id, kind, parent, expires_at, idle_seconds, strong_auth_at, ip, user_agent)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: SessionByToken :one
select s.*, u.username, u.enabled
from sessions s join users u on u.id = s.user_id
where s.token_hash = $1;

-- name: Session :one
select * from sessions where id = $1;

-- name: TouchSession :exec
update sessions set last_seen_at = now() where id = $1;

-- name: SetStrongAuth :exec
update sessions set strong_auth_at = now() where id = $1;

-- name: RevokeSession :execrows
-- Revoking a session revokes the sessions it issued (a CLI refresh token's
-- access tokens).
update sessions set revoked_at = now()
where (id = $1 or parent = $1) and user_id = $2 and revoked_at is null;

-- name: RevokeUserSessions :exec
update sessions set revoked_at = now() where user_id = $1 and revoked_at is null;

-- name: UserSessions :many
select * from sessions
where user_id = $1 and revoked_at is null and expires_at > now() and kind <> 'enroll'
order by last_seen_at desc;

-- name: InsertFlow :exec
insert into auth_flows (id, kind, user_id, data, expires_at) values ($1, $2, $3, $4, $5);

-- name: TakeFlow :one
-- A flow is used once.
delete from auth_flows where id = $1 and kind = $2 and expires_at > now() returning *;

-- name: PruneFlows :exec
delete from auth_flows where expires_at < now();

-- name: UseTOTPStep :execrows
-- A code is used once: only a later time step than the last one counts.
update totp_secrets set last_step = $2 where user_id = $1 and last_step < $2 and confirmed_at is not null;

-- name: PeekFlow :one
-- A flow read without using it up: an OIDC sign-in waiting for a passkey.
select * from auth_flows where id = $1 and expires_at > now();

-- name: InsertDeviceCode :exec
insert into device_codes (code_hash, user_code, ip, user_agent, expires_at) values ($1, $2, $3, $4, $5);

-- name: DeviceCodeByUserCode :one
select * from device_codes where user_code = $1 and expires_at > now();

-- name: DeviceCode :one
select * from device_codes where code_hash = $1;

-- name: DecideDeviceCode :execrows
update device_codes set status = $2, user_id = $3 where user_code = $1 and status = 'pending' and expires_at > now();

-- name: PollDeviceCode :exec
update device_codes set polled_at = now() where code_hash = $1;

-- name: UseDeviceCode :execrows
update device_codes set status = 'used' where code_hash = $1 and status = 'approved';

-- name: InsertSigningKey :exec
insert into signing_keys (fingerprint, user_id, kind, name, public_key) values ($1, $2, $3, $4, $5);

-- name: SigningKeys :many
select * from signing_keys where user_id = $1 and revoked_at is null order by created_at;

-- name: SigningKey :one
select k.*, u.username from signing_keys k join users u on u.id = k.user_id where k.fingerprint = $1;

-- name: AllSigningKeys :many
-- Every live key, for verifying commit signatures.
select k.*, u.username from signing_keys k join users u on u.id = k.user_id
where k.revoked_at is null and u.enabled;

-- name: RevokeSigningKey :execrows
update signing_keys set revoked_at = now() where fingerprint = $1 and user_id = $2 and revoked_at is null;

-- name: DirectoryUser :one
select * from users where provider = $1 and subject = $2;

-- name: DirectoryUserByName :one
select * from users where provider = $1 and directory_name = $2;

-- name: DirectoryUsers :many
select * from users where provider = $1 order by username;

-- name: InsertDirectoryUser :exec
insert into users (id, username, display_name, provider, subject, directory_name, email, enabled, disabled_by, disabled_at)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, case when $8::boolean then null else now() end);

-- name: UpdateDirectoryUser :exec
-- An account an admin closed stays closed; the directory only opens what
-- it closed.
update users set display_name = $2, directory_name = $3, email = $4,
    enabled = case when disabled_by = 'admin' then enabled else sqlc.arg(active)::boolean end,
    disabled_by = case when disabled_by = 'admin' then disabled_by when sqlc.arg(active)::boolean then null else 'directory' end,
    disabled_at = case when disabled_by = 'admin' then disabled_at when sqlc.arg(active)::boolean then null else coalesce(disabled_at, now()) end
where id = $1;

-- name: SetMemberships :exec
-- The members of a team from a source, all at once.
with gone as (
    delete from team_memberships where team = $1 and source = $2 and role = $3
      and not (user_id = any (sqlc.arg(users)::text[]))
)
insert into team_memberships (user_id, team, source, role)
select unnest(sqlc.arg(users)::text[]), $1, $2, $3
on conflict (user_id, team, source, role) do update set synced_at = now();

-- name: DropSourceTeams :exec
-- Teams a source no longer has.
delete from team_memberships where source = $1 and not (team = any (sqlc.arg(teams)::text[]));

-- name: MembershipUsers :many
select user_id from team_memberships where team = $1 and source = $2 and role = $3 order by user_id;

-- name: InsertLoginKey :exec
insert into login_keys (fingerprint, user_id, name, public_key) values ($1, $2, $3, $4);

-- name: LoginKeys :many
select * from login_keys where user_id = $1 and revoked_at is null order by created_at;

-- name: RevokeLoginKey :execrows
update login_keys set revoked_at = now() where fingerprint = $1 and user_id = $2 and revoked_at is null;

-- name: TeamLogins :many
-- Enabled members of teams with their live login keys (none: an empty
-- key), for distributing access.
select distinct u.id, u.username, t.team, coalesce(k.public_key, '')::text as public_key
from team_memberships t join users u on u.id = t.user_id
left join login_keys k on k.user_id = u.id and k.revoked_at is null
where t.team = any (sqlc.arg(teams)::text[]) and t.role = 'member' and u.enabled
order by u.username, t.team, public_key;
