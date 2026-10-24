-- What a pool is for: a name, labels (site, env, zone, vlan...) and tags,
-- which a NetworkSource reads (design 5.2).

-- +goose Up
alter table pools add column name text not null default '';
alter table pools add column labels jsonb not null default '{}';
alter table pools add column tags text[] not null default '{}';

-- +goose Down
alter table pools drop column tags;
alter table pools drop column labels;
alter table pools drop column name;
