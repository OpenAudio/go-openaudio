-- +migrate Up
-- Remove management_keys that were incorrectly populated (signer fallback when access_authorities was empty)
-- prior to fixing the manage_entity logic to only insert keys when access_authorities is explicitly set
delete from management_keys where id < 1200;

-- +migrate Down
-- Down migration: cannot restore deleted rows
select 1;
