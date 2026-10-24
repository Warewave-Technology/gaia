-- name: ContractState :one
select * from contract_states where contract = $1;

-- name: ContractStates :many
select * from contract_states order by contract;

-- name: SetContractSync :exec
insert into contract_states (contract, sync, error, evaluated_at) values ($1, $2, $3, now())
on conflict (contract) do update set sync = excluded.sync, error = excluded.error, evaluated_at = now();

-- name: SetApplied :exec
insert into contract_states (contract, sync, applied_snapshot, applied_label, applied_plan_hash, applied_plan, applied_at)
values ($1, 'synced', $2, $3, $4, $5, now())
on conflict (contract) do update set sync = 'synced', error = null, applied_snapshot = excluded.applied_snapshot,
    applied_label = excluded.applied_label, applied_plan_hash = excluded.applied_plan_hash,
    applied_plan = excluded.applied_plan, applied_at = now();

-- name: OpenChange :one
select * from changes where contract = $1 and status in ('pending', 'approved', 'running');

-- name: LastChange :one
select * from changes where contract = $1 order by created_at desc, id desc limit 1;

-- name: InsertChange :exec
insert into changes (id, contract, source, snapshot, label, plan_hash, plan, status, requested_by, commits)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: Change :one
select * from changes where id = $1;

-- name: Changes :many
select * from changes
where (sqlc.arg(contract)::text = '' or contract = sqlc.arg(contract)::text)
  and (not sqlc.arg(open_only)::boolean or status in ('pending', 'approved', 'running'))
  and (sqlc.arg(commit_sha)::text = '' or commits @> jsonb_build_array(jsonb_build_object('commit', sqlc.arg(commit_sha)::text)))
order by created_at desc, id desc
limit sqlc.arg(max_rows);

-- name: SetChangeStatus :execrows
update changes set status = $2, updated_at = now(),
    finished_at = case when $2 in ('applied', 'failed', 'cancelled', 'rejected', 'superseded') then now() else finished_at end
where id = $1 and status = any (sqlc.arg(from_statuses)::text[]);

-- name: SetChangeSnapshot :exec
-- A newer commit that leaves the plan alone: the change follows it and
-- keeps its approvals.
update changes set snapshot = $2, label = $3, requested_by = $4, commits = $5, source = $6, updated_at = now() where id = $1;

-- name: SetChangeRun :exec
update changes set run_id = $2, status = 'running', updated_at = now() where id = $1;

-- name: SetGate :exec
insert into change_gates (change_id, gate, status, detail) values ($1, $2, $3, $4)
on conflict (change_id, gate) do update set status = excluded.status, detail = excluded.detail;

-- name: Gates :many
select * from change_gates where change_id = $1 order by gate;

-- name: InsertApproval :exec
insert into approvals (change_id, plan_hash, user_id, username, decision, comment) values ($1, $2, $3, $4, $5, $6);

-- name: Approvals :many
select * from approvals where change_id = $1 order by id;

-- name: TeamMembers :many
-- Enabled people in any of the teams.
select distinct u.id, u.username from users u join team_memberships t on t.user_id = u.id
where t.team = any (sqlc.arg(teams)::text[]) and t.role = sqlc.arg(role)::text and u.enabled order by u.username;

-- name: OpenChanges :many
select * from changes where status in ('pending', 'approved', 'running') order by contract;
