-- The plan last applied to each contract, for what a change adds to it
-- (who gains access, design 14.3).

-- +goose Up
alter table contract_states add column applied_plan jsonb;

-- +goose Down
alter table contract_states drop column applied_plan;
