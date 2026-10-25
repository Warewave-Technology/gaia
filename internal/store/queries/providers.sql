-- name: SetProviderCheck :exec
insert into provider_checks (provider, type, status, detail, error, latency_ms, checked_at, last_ok_at)
values ($1, $2, $3, $4, $5, $6, now(), case when $3 = 'ok' then now() end)
on conflict (provider) do update set type = excluded.type, status = excluded.status, detail = excluded.detail,
    error = excluded.error, latency_ms = excluded.latency_ms, checked_at = now(),
    last_ok_at = case when excluded.status = 'ok' then now() else provider_checks.last_ok_at end;

-- name: ProviderChecks :many
select * from provider_checks order by provider;

-- name: DeleteProviderChecksExcept :exec
-- Providers no longer in the catalog.
delete from provider_checks where not (provider = any (@keep::text[]));
