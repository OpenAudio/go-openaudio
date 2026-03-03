-- +migrate Up
-- Add created_at column (nullable initially; existing rows get NULL)
alter table management_keys add column if not exists created_at timestamptz;

-- Remove management_keys that predate created_at (incorrectly populated with signer fallback)
delete from management_keys where created_at is null;

-- Set default and NOT NULL for future inserts
alter table management_keys alter column created_at set default now();
alter table management_keys alter column created_at set not null;

-- +migrate Down
alter table management_keys alter column created_at drop not null;
alter table management_keys alter column created_at drop default;
alter table management_keys drop column if exists created_at;
