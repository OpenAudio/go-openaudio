-- +migrate Up
alter table core_validators
add column if not exists jailed boolean not null default false;

-- +migrate Down
alter table core_validators drop column if exists jailed;
