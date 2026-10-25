-- The decommission flow (design 9.8): a stopped server waits until
-- delete_after; its VM stays in the stack as it was last applied
-- (vm_spec), switched off, so that it can come back unchanged.

-- +goose Up
alter table hosts add column vm_spec jsonb;
alter table hosts add column delete_after timestamptz;
create index hosts_due on hosts (delete_after) where status = 'stopped';

-- +goose Down
drop index hosts_due;
alter table hosts drop column delete_after;
alter table hosts drop column vm_spec;
