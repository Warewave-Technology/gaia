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
update users set enabled = $2, disabled_at = case when $2 then null else now() end where id = $1;

-- name: SetPasswordHash :exec
update users set password_hash = $2 where id = $1;

-- name: Teams :many
select distinct team from team_memberships where user_id = $1 order by team;

-- name: AddTeam :exec
insert into team_memberships (user_id, team, source) values ($1, $2, $3)
on conflict (user_id, team, source) do update set synced_at = now();

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
