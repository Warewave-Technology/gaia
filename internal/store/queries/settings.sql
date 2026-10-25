-- name: LatestSettingsVersion :one
select coalesce(max(version), 0)::bigint from settings_versions;

-- name: InsertSettingsVersion :exec
insert into settings_versions (version, author, reason, proposal, content_hash) values ($1, $2, $3, $4, $5);

-- name: InsertSettingsObject :exec
insert into settings_objects (version, path, data) values ($1, $2, $3);

-- name: SettingsObjects :many
select path, data from settings_objects where version = $1 order by path;

-- name: SettingsVersion :one
select * from settings_versions where version = $1;

-- name: SettingsVersions :many
select v.*, (select count(*) from settings_objects o where o.version = v.version)::int as objects
from settings_versions v order by v.version desc limit sqlc.arg(max_rows);

-- name: InsertAudit :one
insert into audit_log (actor, actor_id, session_id, address, action, object, result, detail)
values ($1, $2, $3, $4, $5, $6, $7, $8) returning id, at;

-- name: AuditLog :many
select * from audit_log
where (sqlc.narg(actor)::text is null or actor = sqlc.narg(actor))
  and (sqlc.narg(action)::text is null or action like sqlc.narg(action) || '%')
  and (sqlc.narg(object)::text is null or object = sqlc.narg(object))
  and (sqlc.narg(since)::timestamptz is null or at >= sqlc.narg(since))
  and (sqlc.narg(before_id)::bigint is null or id < sqlc.narg(before_id))
order by id desc limit sqlc.arg(max_rows);
