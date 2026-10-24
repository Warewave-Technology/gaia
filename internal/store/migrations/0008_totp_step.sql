-- A TOTP code is accepted once: the last time step used is kept.

-- +goose Up
alter table totp_secrets add column last_step bigint not null default 0;

-- +goose Down
alter table totp_secrets drop column last_step;
