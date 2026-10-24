-- name: PulumiStack :one
select * from pulumi_stacks where name = $1;

-- name: SavePulumiStack :exec
insert into pulumi_stacks (name, secrets_provider, encrypted_key) values ($1, $2, $3)
on conflict (name) do update
set secrets_provider = excluded.secrets_provider, encrypted_key = excluded.encrypted_key, updated_at = now();
