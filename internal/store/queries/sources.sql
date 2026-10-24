-- name: SetSourceReport :exec
insert into source_reports (source, status, commit, detail, error, ran_at) values ($1, $2, $3, $4, $5, now())
on conflict (source) do update set status = excluded.status, commit = excluded.commit, detail = excluded.detail,
    error = excluded.error, ran_at = now();

-- name: SourceReports :many
select * from source_reports order by source;
