-- A contract may have several networks on one site (topology); a host
-- belongs to one of them.

-- +goose Up
alter table hosts add column network text not null default '';
alter table hosts alter column network drop default;

-- +goose Down
alter table hosts drop column network;
