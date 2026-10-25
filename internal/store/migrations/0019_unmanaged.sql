-- A server whose contract was decommissioned with deletionPolicy retain
-- (design 9.6): it lives on outside Gaia's management. It keeps its name,
-- so that Gaia does not give it to another server.

-- +goose NO TRANSACTION
-- +goose Up
alter type host_status add value if not exists 'unmanaged';

-- +goose Down
-- An enum value cannot be dropped; unmanaged hosts stay as they are.
select 1;
