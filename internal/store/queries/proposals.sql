-- name: InsertSettingsProposal :exec
insert into settings_proposals (id, base_version, changes, content_hash, title, reason, requested_by, requested_by_id, critical, why_critical, impact)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: SettingsProposal :one
select * from settings_proposals where id = $1;

-- name: SettingsProposals :many
select * from settings_proposals
where (sqlc.narg(status)::text is null or status = sqlc.narg(status))
order by created_at desc limit sqlc.arg(max_rows);

-- name: DecideSettingsProposal :execrows
update settings_proposals
set status = sqlc.arg(status), decided_by = sqlc.arg(decided_by), decided_by_id = sqlc.narg(decided_by_id), comment = sqlc.narg(comment),
    applied_version = sqlc.narg(applied_version), decided_at = now()
where id = sqlc.arg(id) and status = 'pending';

-- name: StaleSettingsProposals :many
update settings_proposals
set status = 'stale', stale_reason = sqlc.arg(reason)::text, decided_at = now()
where status = 'pending' and id <> sqlc.arg(keep)::text and changes ?| sqlc.arg(paths)::text[]
returning id;

-- name: SetSettingsProposalStale :execrows
update settings_proposals set status = 'stale', stale_reason = $2, decided_at = now()
where id = $1 and status = 'pending';
